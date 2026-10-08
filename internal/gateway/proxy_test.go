package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSOCKS is a SOCKS5 server that records the host:port each CONNECT asked
// for and connects every one of them to backend, standing in for tailscaled.
type fakeSOCKS struct {
	ln      net.Listener
	mu      sync.Mutex
	targets []string
}

func newFakeSOCKS(t *testing.T, backend string) *fakeSOCKS {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSOCKS{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, backend)
		}
	}()
	return f
}

func (f *fakeSOCKS) serve(c net.Conn, backend string) {
	defer c.Close()
	r := bufio.NewReader(c)
	hdr := make([]byte, 2)
	io.ReadFull(r, hdr)
	io.ReadFull(r, make([]byte, hdr[1]))
	c.Write([]byte{5, 0})
	req := make([]byte, 4)
	io.ReadFull(r, req)
	if req[3] != 3 {
		c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	n, _ := r.ReadByte()
	host := make([]byte, n)
	io.ReadFull(r, host)
	pb := make([]byte, 2)
	io.ReadFull(r, pb)
	f.mu.Lock()
	f.targets = append(f.targets, string(host)+":"+strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	f.mu.Unlock()
	up, err := net.Dial("tcp", backend)
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	go func() {
		io.Copy(up, r)
		up.(*net.TCPConn).CloseWrite() // pass the client's half-close on, as tailscaled does
	}()
	io.Copy(c, up)
}

func (f *fakeSOCKS) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...)
}

// setup runs a backend (a page and a WebSocket-style upgrade that echoes),
// a fake SOCKS server in front of it, and the gateway proxy for port 7682.
func setup(t *testing.T) (*fakeSOCKS, *httptest.Server) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			rw.Flush()
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo:" + line)
			rw.Flush()
			return
		}
		io.WriteString(w, "page for "+r.URL.RequestURI())
	}))
	t.Cleanup(backend.Close)
	socks := newFakeSOCKS(t, backend.Listener.Addr().String())
	p := &Proxy{Tailnet: "tail123.ts.net", Dial: SOCKS5Dialer(socks.ln.Addr().String())}
	gw := httptest.NewServer(p.Handler(7682))
	t.Cleanup(gw.Close)
	return socks, gw
}

func get(t *testing.T, gw *httptest.Server, host string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", gw.URL+"/?arg=book", nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// <box>.localhost:<port> reaches <box>.<tailnet>:<port>, query and all.
func TestProxyRoutesByBoxNameToTheTailnet(t *testing.T) {
	socks, gw := setup(t)
	resp := get(t, gw, "dev.localhost:7682", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "page for /?arg=book" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	// One dial probes for TLS, one carries the request.
	want := "[dev.tail123.ts.net:7682 dev.tail123.ts.net:7682]"
	if a := fmt.Sprint(socks.asked()); a != want {
		t.Errorf("dialled %v, want %v", a, want)
	}
	// The scheme is remembered and the connection kept, so a second request dials nothing.
	resp = get(t, gw, "dev.localhost:7682", nil)
	io.ReadAll(resp.Body)
	if a := fmt.Sprint(socks.asked()); a != want {
		t.Errorf("second request dialled again: %v", a)
	}
}

// Only <box>.localhost on the port it arrived at is routed: any other Host is
// how a DNS-rebinding page would reach the tailnet through this proxy.
func TestProxyRefusesEveryOtherHost(t *testing.T) {
	socks, gw := setup(t)
	for _, h := range []string{"evil.com:7682", "localhost:7682", "dev.localhost:8080", "a.b.localhost:7682", "Dev_X.localhost:7682", "dev.localhost", "dev.localhost.evil.com:7682"} {
		if resp := get(t, gw, h, nil); resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%s: %d", h, resp.StatusCode)
		}
	}
	if a := socks.asked(); len(a) != 0 {
		t.Errorf("a refused request dialled %v", a)
	}
}

// A page on any other site must not drive the terminal: a cross-origin request
// is refused, and a WebSocket must say where it comes from.
func TestProxyRefusesCrossOriginAndOriginlessWebSockets(t *testing.T) {
	socks, gw := setup(t)
	ws := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}
	for _, c := range []map[string]string{
		{"Origin": "https://evil.com"},
		{"Origin": "http://other.localhost:7682"},
		merge(ws, map[string]string{"Origin": "https://evil.com"}),
		ws,
	} {
		if resp := get(t, gw, "dev.localhost:7682", c); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%v: %d", c, resp.StatusCode)
		}
	}
	if a := socks.asked(); len(a) != 0 {
		t.Errorf("a refused request dialled %v", a)
	}
}

// ttyd's terminal is a WebSocket, so a same-origin upgrade must pass through
// and carry data both ways.
func TestProxyCarriesASameOriginWebSocket(t *testing.T) {
	_, gw := setup(t)
	c, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET /ws?arg=book HTTP/1.1\r\nHost: dev.localhost:7682\r\nOrigin: http://dev.localhost:7682\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: x\r\nSec-WebSocket-Version: 13\r\n\r\n")
	r := bufio.NewReader(c)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status %q", status)
	}
	for {
		l, _ := r.ReadString('\n')
		if l == "\r\n" || l == "" {
			break
		}
	}
	io.WriteString(c, "hello\n")
	got, _ := r.ReadString('\n')
	if got != "echo:hello\n" {
		t.Errorf("got %q", got)
	}
}

func TestSOCKS5DialerReportsARefusedConnect(t *testing.T) {
	socks := newFakeSOCKS(t, "127.0.0.1:1") // nothing listens there
	if _, err := SOCKS5Dialer(socks.ln.Addr().String())(context.Background(), "tcp", "dev.tail.ts.net:7682"); err == nil {
		t.Error("expected an error")
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// A worker on a tailnet with HTTPS certificates serves TLS on the same port,
// and one without serves plain HTTP; the proxy speaks whichever it finds.
func TestProxySpeaksTLSToAWorkerServingHTTPS(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tls page for "+r.Host)
	}))
	t.Cleanup(backend.Close)
	socks := newFakeSOCKS(t, backend.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(backend.Certificate())
	p := &Proxy{Tailnet: "tail123.ts.net", Dial: SOCKS5Dialer(socks.ln.Addr().String()),
		TLS: &tls.Config{RootCAs: roots, ServerName: "example.com"}}
	gw := httptest.NewServer(p.Handler(7682))
	t.Cleanup(gw.Close)
	resp := get(t, gw, "dev.localhost:7682", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "tls page for dev.tail123.ts.net:7682" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}

// Pipe is ssh's ProxyCommand: stdin to the worker, the worker to stdout, and
// the end of stdin passed on as a half-close so the far side sees EOF.
func TestPipeCarriesBothWaysThroughSOCKS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		got, _ := io.ReadAll(c) // returns only once the client half-closes
		io.WriteString(c, "got:"+string(got))
	}()
	socks := newFakeSOCKS(t, ln.Addr().String())
	var out strings.Builder
	err = Pipe(context.Background(), SOCKS5Dialer(socks.ln.Addr().String()), "dev.tail123.ts.net:22", strings.NewReader("SSH-2.0-test"), &out)
	if err != nil || out.String() != "got:SSH-2.0-test" {
		t.Fatalf("got %q, %v", out.String(), err)
	}
	if a := fmt.Sprint(socks.asked()); a != "[dev.tail123.ts.net:22]" {
		t.Errorf("dialled %v", a)
	}
}
