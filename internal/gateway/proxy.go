// Package gateway is megh's gateway role: a local container that is on the
// tailnet while its host is not, forwarding the host's browser to workers'
// web surfaces. It holds no provider or GitHub keys, only a tailnet identity,
// and its ports are published to the host's 127.0.0.1 alone (see DESIGN.md,
// "Roles", and the C4 exception in CONSTRAINTS.md).
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Ports are the worker surfaces the gateway forwards: ttyd, webterm,
// code-server and noVNC. Nothing else on a worker is reachable through it.
var Ports = []int{7681, 7682, 8080, 6080}

// boxLabel is a box name: one DNS label, as megh up enforces.
var boxLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Proxy routes http://<box>.localhost:<port>/... to <box>.<Tailnet>:<port>
// through Dial (tailscaled's SOCKS5 server). Browsers resolve *.localhost to
// 127.0.0.1, so one published port serves every box by name.
type Proxy struct {
	Tailnet string // MagicDNS suffix, e.g. tail1234.ts.net
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLS verifies a worker serving HTTPS; nil means the system roots, which
	// is right for the tailnet's Let's Encrypt certificates.
	TLS *tls.Config

	schemes sync.Map // target -> "http" | "https"
}

// Handler serves one surface port. It refuses (421) any Host that is not
// <box>.localhost:<port> for this port, which is what stops a DNS-rebinding
// page from using the proxy to reach the tailnet, and refuses (403) a request
// whose Origin is another site, or a WebSocket with no Origin, which is what
// stops any page the user visits from typing into a box's terminal.
func (p *Proxy) Handler(port int) http.Handler {
	transport := &http.Transport{DialContext: p.Dial, TLSClientConfig: p.TLS, ForceAttemptHTTP2: false}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := pr.In.Context().Value(targetKey{}).(string)
			scheme, _ := p.schemes.Load(target)
			pr.Out.URL.Scheme = scheme.(string)
			pr.Out.URL.Host = target
			pr.Out.Host = target
			pr.SetXForwarded()
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			target, _ := r.Context().Value(targetKey{}).(string)
			p.schemes.Delete(target) // probe again next time: the worker may have changed
			http.Error(w, fmt.Sprintf("megh gateway: can't reach %s (is the box up and on the tailnet?): %v", target, err), http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		box, ok := boxFor(r.Host, port)
		if !ok {
			http.Error(w, "megh gateway: use http://<box>.localhost:"+strconv.Itoa(port)+"/", http.StatusMisdirectedRequest)
			return
		}
		if !sameOrigin(r) {
			http.Error(w, "megh gateway: cross-origin request refused", http.StatusForbidden)
			return
		}
		target := net.JoinHostPort(box+"."+p.Tailnet, strconv.Itoa(port))
		if _, known := p.schemes.Load(target); !known {
			scheme, err := p.probe(r.Context(), target)
			if err != nil {
				http.Error(w, fmt.Sprintf("megh gateway: can't reach %s (is the box up and on the tailnet?): %v", target, err), http.StatusBadGateway)
				return
			}
			p.schemes.Store(target, scheme)
		}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, target)))
	})
}

type targetKey struct{}

// probe answers whether target serves HTTPS or plain HTTP. A worker's
// `tailscale serve` picks one per port depending on whether the tailnet has
// certificates on (internal/tsops/ts-up.sh), so the gateway can't know ahead.
// A TLS handshake that gets back something other than a TLS record means
// plain HTTP; any other failure is an error.
func (p *Proxy) probe(ctx context.Context, target string) (string, error) {
	conn, err := p.Dial(ctx, "tcp", target)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	cfg := &tls.Config{}
	if p.TLS != nil {
		cfg = p.TLS.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName, _, _ = net.SplitHostPort(target)
	}
	err = tls.Client(conn, cfg).HandshakeContext(ctx)
	var notTLS tls.RecordHeaderError
	switch {
	case err == nil:
		return "https", nil
	case errors.As(err, &notTLS):
		return "http", nil
	default:
		return "", err
	}
}

// boxFor is the box a Host names, if it is exactly <box>.localhost:<port>.
func boxFor(host string, port int) (string, bool) {
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != strconv.Itoa(port) {
		return "", false
	}
	box, ok := strings.CutSuffix(h, ".localhost")
	return box, ok && boxLabel.MatchString(box)
}

// sameOrigin is true when the request carries no foreign Origin. A browser
// always sends Origin on a WebSocket, so one without it is refused too.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return !strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}
