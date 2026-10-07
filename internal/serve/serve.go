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
	"github.com/panyam/megh/internal/providers/hetzner"
	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/panyam/megh/internal/providers/vultr"
	"github.com/panyam/megh/internal/tsapi"
)

// Request headers carrying the caller's credentials. Header names, not
// cookies, so the browser never sends them on its own.
const (
	HeaderRunPodKey   = "X-Megh-Runpod-Key"
	HeaderHcloudToken = "X-Megh-Hcloud-Token"
	HeaderVultrKey    = "X-Megh-Vultr-Key"
	HeaderRegistry    = "X-Megh-Registry-Token"
	HeaderTSID        = "X-Megh-Ts-Client-Id"
	HeaderTSSecret    = "X-Megh-Ts-Client-Secret"
)

// csp allows nothing but this origin's own files: no inline script or style,
// no third-party anything, and no framing. Every response carries it.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

//go:embed web
var web embed.FS

// keyVars names the note's variable for each provider a server can build, in
// menu order. The page lists every one of them, keyed or not, so a provider
// with no key yet is something to add rather than something missing.
var keyVars = []struct{ provider, env string }{
	{"runpod", "RUNPOD_API_KEY"},
	{"hetzner", "HCLOUD_TOKEN"},
	{"vultr", "VULTR_API_KEY"},
}

// keyVar is the note variable that unlocks provider, or "" for one this
// server cannot build.
func keyVar(provider string) string {
	for _, kv := range keyVars {
		if kv.provider == provider {
			return kv.env
		}
	}
	return ""
}

// Keys are the credentials one request carried. They live for that request.
// Each provider key is optional on its own; a request needs at least one.
type Keys struct {
	RunPod         string
	Hetzner        string
	Vultr          string
	TSClientID     string
	TSClientSecret string
	// Registry is the image pull token a Hetzner or Vultr box logs in with on
	// first boot. It opens no backend on its own, so it does not count toward
	// the one-provider-key minimum.
	Registry string
}

// anyProvider reports whether k holds a key for at least one backend.
func (k Keys) anyProvider() bool { return k.RunPod != "" || k.Hetzner != "" || k.Vultr != "" }

// values is every key k holds, for scrubbing.
func (k Keys) values() []string {
	return []string{k.RunPod, k.Hetzner, k.Vultr, k.TSClientID, k.TSClientSecret, k.Registry}
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
		Hetzner:        r.Header.Get(HeaderHcloudToken),
		Vultr:          r.Header.Get(HeaderVultrKey),
		TSClientID:     r.Header.Get(HeaderTSID),
		TSClientSecret: r.Header.Get(HeaderTSSecret),
		Registry:       r.Header.Get(HeaderRegistry),
	}
	k := hdr
	var held Keys
	if s.Secrets != nil {
		var err error
		if held, err = s.Secrets(r.Context()); err != nil && s.Log != nil {
			s.Log.Printf("server keys unavailable, using the browser's: %v", err)
		}
		k.RunPod = cmp.Or(held.RunPod, hdr.RunPod)
		k.Hetzner = cmp.Or(held.Hetzner, hdr.Hetzner)
		k.Vultr = cmp.Or(held.Vultr, hdr.Vultr)
		k.TSClientID = cmp.Or(held.TSClientID, hdr.TSClientID)
		k.TSClientSecret = cmp.Or(held.TSClientSecret, hdr.TSClientSecret)
		k.Registry = cmp.Or(held.Registry, hdr.Registry)
	}
	return k, scrubber(hdr, held)
}

// keySources answers GET /api/keys: which keys the server holds, as booleans,
// every provider the page can offer with the variable that unlocks it, and the
// note's name for the registry pull token. It needs no key itself, so
// the page can decide whether to ask for any.
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
		"hetzner":   held.Hetzner != "",
		"vultr":     held.Vultr != "",
		"tailscale": held.TSClientID != "" && held.TSClientSecret != "",
		"registry":  held.Registry != "",
	}
	resp["registryEnv"] = s.RegistryEnv()
	all := make([]map[string]string, 0, len(keyVars))
	for _, kv := range keyVars {
		all = append(all, map[string]string{"name": kv.provider, "env": kv.env})
	}
	resp["providers"] = all
	writeJSON(w, http.StatusOK, resp)
}

// New returns a Server whose backends are the providers each request holds a
// key for: RunPod, Hetzner and Vultr, in that order.
func New(cfg config.Config) *Server {
	cfgFn := func() config.Config { return cfg }
	return &Server{
		Config: cfg,
		Backends: func(k Keys) []providers.Provider {
			var ps []providers.Provider
			if k.RunPod != "" {
				ps = append(ps, runpod.NewWithKey(k.RunPod))
			}
			if k.Hetzner != "" {
				ps = append(ps, hetzner.NewWithToken(cfgFn, k.Hetzner))
			}
			if k.Vultr != "" {
				ps = append(ps, vultr.NewWithKey(cfgFn, k.Vultr))
			}
			return ps
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

// RegistryEnv is the note's name for the registry pull token: megh.yaml's
// registries[0].token_env, else DefaultRegistryEnv.
func (s *Server) RegistryEnv() string {
	if len(s.Config.Registries) > 0 && s.Config.Registries[0].TokenEnv != "" {
		return s.Config.Registries[0].TokenEnv
	}
	return DefaultRegistryEnv
}

type keysCtx struct{}

// requestKeys are the merged keys api() resolved for r.
func requestKeys(r *http.Request) Keys {
	k, _ := r.Context().Value(keysCtx{}).(Keys)
	return k
}

// Handler is the whole site: the page, its script and style, and /api/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", static("web/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", static("web/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /app.css", static("web/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /api/keys", s.keySources)
	mux.HandleFunc("GET /api/boxes", s.api(s.boxes))
	mux.HandleFunc("GET /api/volumes", s.api(s.volumes))
	mux.HandleFunc("POST /api/volumes", s.api(s.createVolume))
	mux.HandleFunc("POST /api/volumes/delete", s.api(s.deleteVolume))
	mux.HandleFunc("GET /api/regions", s.api(s.regions))
	mux.HandleFunc("POST /api/regions/probe", s.api(s.probe))
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
		if !k.anyProvider() {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no provider key (RUNPOD_API_KEY, HCLOUD_TOKEN or VULTR_API_KEY): none stored on the server and none on this request; paste your keys into the page"})
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
		data, err := f(r.WithContext(context.WithValue(r.Context(), keysCtx{}, k)), svc)
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
		for _, v := range k.values() {
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
	Provider  string           `json:"provider"`
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	DC        string           `json:"dc"`
	CostPerHr float64          `json:"costPerHr"`
	Links     []lifecycle.Link `json:"links"`
	SSH       string           `json:"ssh,omitempty"`
	Tunnel    string           `json:"tunnel,omitempty"`
}

// boxes lists the boxes on every backend the request holds a key for. One
// backend failing does not hide the others: its error goes to the log, and
// the call fails only when every backend did.
func (s *Server) boxes(r *http.Request, svc *lifecycle.Service) (any, error) {
	views := []BoxView{}
	var firstErr error
	failed := 0
	for _, p := range svc.Providers {
		boxes, err := svc.List(r.Context(), p.Name(), false)
		if err != nil {
			fmt.Fprintf(svc.Err, "%s: %v\n", p.Name(), err)
			firstErr = cmp.Or(firstErr, err)
			failed++
			continue
		}
		for _, b := range boxes {
			shell, tunnel := lifecycle.SSHCommands(b)
			views = append(views, BoxView{
				Name: b.DisplayName(), Provider: p.Name(), ID: b.ID, Status: b.Status, DC: b.DataCenter,
				CostPerHr: b.CostPerHr, Links: lifecycle.BoxLinks(s.Config, b), SSH: shell, Tunnel: tunnel,
			})
		}
	}
	if failed > 0 && failed == len(svc.Providers) {
		return nil, firstErr
	}
	return views, nil
}

// defaultProvider is megh.yaml's default backend when the request holds a key
// for it, else the first backend it does hold a key for.
func (s *Server) defaultProvider(svc *lifecycle.Service) string {
	name := cmp.Or(s.Config.DefaultProvider, "runpod")
	if _, err := svc.Provider(name); err != nil && len(svc.Providers) > 0 {
		return svc.Providers[0].Name()
	}
	return name
}

// boxName is a valid box name: one DNS label, since it becomes the box's
// tailnet hostname.
var boxName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// VolumeView is one scratch volume as the launch form offers it.
type VolumeView struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	DC       string `json:"dc"`
	SizeGB   int    `json:"sizeGB"`
}

// volumes lists the scratch volumes a box can launch onto, on every backend
// the request holds a key for, which one megh.yaml names as the default, and
// those backends. A box can only attach a volume in its own data center, and
// on its own provider, so picking a volume picks both.
func (s *Server) volumes(r *http.Request, svc *lifecycle.Service) (any, error) {
	vols, errs := svc.Volumes(r.Context())
	if len(vols) == 0 && len(errs) > 0 {
		return nil, errs[0]
	}
	views := make([]VolumeView, 0, len(vols))
	for _, v := range vols {
		views = append(views, VolumeView{Provider: v.Provider, ID: v.ID, Name: v.Name, DC: v.DataCenter, SizeGB: v.Size})
	}
	names := make([]string, 0, len(svc.Providers))
	for _, p := range svc.Providers {
		names = append(names, p.Name())
	}
	def := s.defaultProvider(svc)
	return map[string]any{
		"volumes":   views,
		"default":   s.Config.Provider(def).DefaultVolume,
		"providers": names,
		"provider":  def,
	}, nil
}

// boxSize is a whole shape the page can ask for. RunPod caps the container
// disk by instance size when a volume is attached (about 20 GB at 2 vCPU, 40 at
// 4, 50 at 8), so RAM and disk are tied to the vCPU count rather than chosen
// separately and rejected at launch.
type boxSize struct{ ram, disk int }

var boxSizes = map[int]boxSize{2: {8, 20}, 4: {16, 40}, 8: {32, 50}}

func (s *Server) up(r *http.Request, svc *lifecycle.Service) (any, error) {
	var req struct {
		Name   string `json:"name"`
		Flavor string `json:"flavor"`
		VCPU   int    `json:"vcpu"`   // 0 = megh.yaml's default size
		Volume string `json:"volume"` // "" = megh.yaml's default volume and data center
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
	up := lifecycle.UpRequest{Name: req.Name, Flavor: req.Flavor, Provider: s.defaultProvider(svc), PullToken: requestKeys(r).Registry}
	if req.VCPU != 0 {
		size, ok := boxSizes[req.VCPU]
		if !ok {
			return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%d vCPU is not an offered size (2, 4 or 8)", req.VCPU)}
		}
		up.VCPU, up.RAMGiB, up.DiskGiB = req.VCPU, size.ram, size.disk
	}
	if req.Volume != "" {
		// Only a volume that exists, and the box goes to its provider and data
		// center: a box can only attach a volume there.
		vols, _ := svc.Volumes(r.Context())
		found := false
		for _, v := range vols {
			if v.ID == req.Volume {
				up.Provider, up.VolumeID, up.DataCenter, found = v.Provider, v.ID, v.DataCenter, true
				break
			}
		}
		if !found {
			return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("no volume %q on this account", req.Volume)}
		}
	}
	res, err := svc.Up(r.Context(), up)
	if errors.Is(err, runpod.ErrNoCapacity) {
		// Transient and size-dependent, not a fault: 503, with the provider's
		// advice, so the page can say "try again or pick a smaller size".
		return nil, &apiError{http.StatusServiceUnavailable, err.Error()}
	}
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
	prov, box, err := s.findBox(r, svc, req.Name)
	if err != nil {
		return nil, err
	}
	if err := svc.Down(r.Context(), prov, *box, lifecycle.DownOptions{}); err != nil {
		return nil, err
	}
	return map[string]string{"terminated": box.DisplayName()}, nil
}

// findBox finds a box by name on every backend the request holds a key for.
// The same name on two backends is refused rather than guessed, since the
// caller is about to terminate it.
func (s *Server) findBox(r *http.Request, svc *lifecycle.Service, name string) (providers.Provider, *providers.Box, error) {
	var prov providers.Provider
	var box *providers.Box
	for _, p := range svc.Providers {
		_, b, err := svc.Find(r.Context(), p.Name(), name)
		var nf *providers.NotFoundError
		if errors.As(err, &nf) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if box != nil {
			return nil, nil, &apiError{http.StatusConflict, fmt.Sprintf("%q is a box on both %s and %s; terminate it with megh down --provider", name, prov.Name(), p.Name())}
		}
		prov, box = p, b
	}
	if box == nil {
		return nil, nil, &apiError{http.StatusNotFound, (&providers.NotFoundError{Name: name}).Error()}
	}
	return prov, box, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
