// Package serve is the web control plane: a page and a small JSON API over
// internal/lifecycle, run locally by `megh serve` and hosted by cmd/meghplane.
//
// Keys come from two places, merged per key on every request. On meghplane the
// server may hold them in Secret Manager (Server.Secrets), which is what makes
// the page usable from a managed browser whose extensions could read anything
// pasted into it. Whatever the server does not hold, the page supplies: it
// keeps the user's keys in the tab's sessionStorage and sends them as headers
// on every API call. Either way each request builds its own backends from the
// merged keys and drops them when it returns.
//
// The API's write calls require application/json, which another site cannot
// make a browser send cross-origin without a CORS preflight, and this server
// never answers one. That is the CSRF defence, and it holds whether or not any
// key travels in a header.
package serve

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/lifecycle"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/panyam/megh/internal/tsapi"
)

// Request headers carrying the caller's credentials. Header names, not
// cookies, so the browser never sends them on its own.
const (
	HeaderRunPodKey = "X-Megh-Runpod-Key"
	HeaderTSID      = "X-Megh-Ts-Client-Id"
	HeaderTSSecret  = "X-Megh-Ts-Client-Secret"
)

// csp allows nothing but this origin's own files: no inline script or style,
// no third-party anything, and no framing. Every response carries it.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

//go:embed web
var web embed.FS

// Keys are the credentials one request carried. They live for that request.
type Keys struct {
	RunPod         string
	TSClientID     string
	TSClientSecret string
}

// Server is the control plane. The zero value is not usable; build one with New.
type Server struct {
	Config config.Config
	// Backends builds the providers for one request from its keys.
	Backends func(Keys) []providers.Provider
	// Tailscale builds the request's control-plane client factory, or returns
	// nil when the request carried no Tailscale credential (minting then warns
	// and the box launches without joining the tailnet).
	Tailscale func(Keys) func() (*tsapi.Client, error)
	// Authorize says who is asking, or why they may not. Nil admits everyone,
	// which only `megh serve` uses, and only on loopback.
	Authorize func(*http.Request) (string, error)
	// Secrets returns keys the server holds (Secret Manager on meghplane). Each
	// non-empty field overrides the browser's header for that key; an empty one
	// falls back to it. Nil means the browser supplies everything.
	Secrets func(context.Context) (Keys, error)
	Log     *log.Logger
}

// keysFor merges the server's keys over the request's headers, field by
// field, and returns a scrubber covering every value either side supplied.
func (s *Server) keysFor(r *http.Request) (Keys, func(string) string) {
	hdr := Keys{
		RunPod:         r.Header.Get(HeaderRunPodKey),
		TSClientID:     r.Header.Get(HeaderTSID),
		TSClientSecret: r.Header.Get(HeaderTSSecret),
	}
	k := hdr
	var held Keys
	if s.Secrets != nil {
		var err error
		if held, err = s.Secrets(r.Context()); err != nil && s.Log != nil {
			s.Log.Printf("server keys unavailable, using the browser's: %v", err)
		}
		k.RunPod = cmp.Or(held.RunPod, hdr.RunPod)
		k.TSClientID = cmp.Or(held.TSClientID, hdr.TSClientID)
		k.TSClientSecret = cmp.Or(held.TSClientSecret, hdr.TSClientSecret)
	}
	return k, scrubber(hdr, held)
}

// keySources answers GET /api/keys: which keys the server holds, as booleans.
// It needs no key itself, so the page can decide whether to ask for any.
func (s *Server) keySources(w http.ResponseWriter, r *http.Request) {
	var held Keys
	resp := map[string]any{}
	if s.Secrets != nil {
		var err error
		if held, err = s.Secrets(r.Context()); err != nil {
			resp["error"] = "server keys unavailable; paste yours"
		}
	}
	resp["server"] = map[string]bool{
		"runpod":    held.RunPod != "",
		"tailscale": held.TSClientID != "" && held.TSClientSecret != "",
	}
	writeJSON(w, http.StatusOK, resp)
}

// New returns a Server backed by RunPod with each request's own key.
func New(cfg config.Config) *Server {
	return &Server{
		Config: cfg,
		Backends: func(k Keys) []providers.Provider {
			return []providers.Provider{runpod.NewWithKey(k.RunPod)}
		},
		Tailscale: func(k Keys) func() (*tsapi.Client, error) {
			if k.TSClientID == "" || k.TSClientSecret == "" {
				return nil
			}
			return func() (*tsapi.Client, error) {
				return tsapi.NewWithCreds(tsapi.Creds{ClientID: k.TSClientID, ClientSecret: k.TSClientSecret}, cfg.Tailnet)
			}
		},
		Log: log.Default(),
	}
}

// Handler is the whole site: the page, its script and style, and /api/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", static("web/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", static("web/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /app.css", static("web/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /api/keys", s.keySources)
	mux.HandleFunc("GET /api/boxes", s.api(s.boxes))
	mux.HandleFunc("POST /api/up", s.api(s.up))
	mux.HandleFunc("POST /api/down", s.api(s.down))
	return secure(s.logged(s.authorized(mux)))
}

func static(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := web.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(b)
	}
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// logged records method, path and status only: never headers (the keys) and
// never the query string.
func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if s.Log != nil {
			s.Log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

func (s *Server) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Authorize != nil {
			if _, err := s.Authorize(r); err != nil {
				if s.Log != nil {
					s.Log.Printf("refused %s %s: %v", r.Method, r.URL.Path, err)
				}
				http.Error(w, "not authorized", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// apiError is an error with the HTTP status it should produce.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

type apiFunc func(r *http.Request, svc *lifecycle.Service) (any, error)

// api runs one API call with a lifecycle.Service built from that request's
// keys, and answers {"data": ..., "log": ...} or {"error": ..., "log": ...}.
// Every string that goes back is scrubbed of the request's own key values, in
// case a backend ever echoes one in an error.
func (s *Server) api(f apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		k, scrub := s.keysFor(r)
		if k.RunPod == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no RunPod key: none stored on the server and none on this request; paste your keys into the page"})
			return
		}
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send application/json"})
			return
		}
		var out bytes.Buffer
		svc := &lifecycle.Service{
			Config:    s.Config,
			Providers: s.Backends(k),
			Tailscale: s.Tailscale(k),
			Out:       &out,
			Err:       &out,
		}
		data, err := f(r, svc)
		if err != nil {
			status := http.StatusBadGateway
			var ae *apiError
			if errors.As(err, &ae) {
				status = ae.status
			}
			writeJSON(w, status, map[string]string{"error": scrub(err.Error()), "log": scrub(out.String())})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "log": scrub(out.String())})
	}
}

func scrubber(sets ...Keys) func(string) string {
	var secrets []string
	for _, k := range sets {
		for _, v := range []string{k.RunPod, k.TSClientID, k.TSClientSecret} {
			if len(v) >= 4 {
				secrets = append(secrets, v)
			}
		}
	}
	return func(s string) string {
		for _, v := range secrets {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
		return s
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// decode reads a small JSON body into v, refusing unknown fields.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &apiError{http.StatusBadRequest, "bad request body: " + err.Error()}
	}
	return nil
}

// BoxView is one box as the page shows it.
type BoxView struct {
	Name      string           `json:"name"`
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	DC        string           `json:"dc"`
	CostPerHr float64          `json:"costPerHr"`
	Links     []lifecycle.Link `json:"links"`
	SSH       string           `json:"ssh,omitempty"`
	Tunnel    string           `json:"tunnel,omitempty"`
}

func (s *Server) boxes(r *http.Request, svc *lifecycle.Service) (any, error) {
	boxes, err := svc.List(r.Context(), "", false)
	if err != nil {
		return nil, err
	}
	views := make([]BoxView, 0, len(boxes))
	for _, b := range boxes {
		shell, tunnel := lifecycle.SSHCommands(b)
		views = append(views, BoxView{
			Name: b.DisplayName(), ID: b.ID, Status: b.Status, DC: b.DataCenter,
			CostPerHr: b.CostPerHr, Links: lifecycle.BoxLinks(s.Config, b), SSH: shell, Tunnel: tunnel,
		})
	}
	return views, nil
}

// boxName is a valid box name: one DNS label, since it becomes the box's
// tailnet hostname.
var boxName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

func (s *Server) up(r *http.Request, svc *lifecycle.Service) (any, error) {
	var req struct {
		Name   string `json:"name"`
		Flavor string `json:"flavor"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !boxName.MatchString(req.Name) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%q is not a valid box name (lowercase letters, digits and dashes)", req.Name)}
	}
	if req.Flavor != "" && !contains(s.Config.Flavors, req.Flavor) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("unknown flavor %q", req.Flavor)}
	}
	res, err := svc.Up(r.Context(), lifecycle.UpRequest{Name: req.Name, Flavor: req.Flavor})
	if err != nil {
		return nil, err
	}
	return map[string]string{"summary": res.Summary()}, nil
}

func (s *Server) down(r *http.Request, svc *lifecycle.Service) (any, error) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	// Always by name: the CLI's "the only box" fallback is too easy to hit
	// from a button.
	if req.Name == "" {
		return nil, &apiError{http.StatusBadRequest, "name the box to terminate"}
	}
	prov, box, err := svc.Find(r.Context(), "", req.Name)
	if err != nil {
		var nf *providers.NotFoundError
		if errors.As(err, &nf) {
			return nil, &apiError{http.StatusNotFound, err.Error()}
		}
		return nil, err
	}
	if err := svc.Down(r.Context(), prov, *box, lifecycle.DownOptions{}); err != nil {
		return nil, err
	}
	return map[string]string{"terminated": box.DisplayName()}, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
