package lifecycle

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// RenderPortal builds the PORTAL.md markdown for the given boxes: tailnet links
// to each box's web surfaces, plus its public SSH lines for machines off the
// tailnet. Links use cfg.Tailnet as the MagicDNS suffix and cfg.Portal.Scheme
// (default http).
func RenderPortal(cfg config.Config, pods []providers.Box) string {
	scheme := cfg.Portal.Scheme
	if scheme == "" {
		scheme = "http"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# megh boxes\n\n_updated %s · %d box(es)_\n\n",
		time.Now().UTC().Format("2006-01-02 15:04 UTC"), len(pods))
	if len(pods) == 0 {
		b.WriteString("No boxes. Run `megh up <name>` to launch one.\n")
		return b.String()
	}
	surfaces := []struct {
		label string
		port  int
		path  string
	}{
		{"📱 webterm", 7682, "/"},
		{"🖥️ shell", 7681, "/"},
		{"code", 8080, "/"},
		{"vnc", 6080, "/vnc.html"},
	}
	for _, p := range pods {
		name := p.DisplayName()
		host := name
		if cfg.Tailnet != "" {
			host = name + "." + cfg.Tailnet
		}
		meta := []string{"`" + p.Status + "`"}
		if p.DataCenter != "" {
			meta = append(meta, p.DataCenter)
		}
		if p.CostPerHr > 0 {
			meta = append(meta, fmt.Sprintf("$%.3f/hr", p.CostPerHr))
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", name, strings.Join(meta, " · "))
		for _, s := range surfaces {
			fmt.Fprintf(&b, "- [%s](%s://%s:%d%s)\n", s.label, scheme, host, s.port, s.path)
		}
		// The tailnet links above are useless to a machine that is not on the
		// tailnet; public SSH is its way in, with any key in extra_pubkeys. A
		// local box's endpoint is this machine's loopback, so it is left out.
		if p.SSHReady() && !isLoopback(p.PublicIP) {
			fmt.Fprintf(&b, "- ssh: `ssh -p %d root@%s`\n", p.SSHPort, p.PublicIP)
			fmt.Fprintf(&b, "- webterm off-tailnet: `ssh -p %d -L 7682:127.0.0.1:7682 root@%s` then http://localhost:7682\n",
				p.SSHPort, p.PublicIP)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// isLoopback reports whether an endpoint only means something on this machine.
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
