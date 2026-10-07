package lifecycle

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/providers"
)

// Link is one way into a box: a label and the URL behind it.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

var surfaces = []struct {
	label string
	port  int
	path  string
}{
	{"📱 webterm", 7682, "/"},
	{"🖥️ shell", 7681, "/"},
	{"code", 8080, "/"},
	{"vnc", 6080, "/vnc.html"},
}

// BoxLinks are a box's web surfaces on the tailnet, addressed by its MagicDNS
// name (cfg.Tailnet as the suffix) with cfg.Portal.Scheme (default http).
func BoxLinks(cfg config.Config, b providers.Box) []Link {
	scheme := cfg.Portal.Scheme
	if scheme == "" {
		scheme = "http"
	}
	host := b.DisplayName()
	if cfg.Tailnet != "" {
		host += "." + cfg.Tailnet
	}
	links := make([]Link, 0, len(surfaces))
	for _, s := range surfaces {
		links = append(links, Link{s.label, fmt.Sprintf("%s://%s:%d%s", scheme, host, s.port, s.path)})
	}
	return links
}

// SSHCommands are the public-SSH ways into a box for a machine off the
// tailnet: a plain shell, and a tunnel to webterm with -L before the host
// (after it, ssh would take it as the remote command). Both are empty for a box
// with no mapped port yet, or whose endpoint is the launcher's own loopback.
func SSHCommands(b providers.Box) (shell, tunnel string) {
	if !b.SSHReady() || isLoopback(b.PublicIP) {
		return "", ""
	}
	return fmt.Sprintf("ssh -p %d root@%s", b.SSHPort, b.PublicIP),
		fmt.Sprintf("ssh -p %d -L 7682:127.0.0.1:7682 root@%s", b.SSHPort, b.PublicIP)
}

// RenderPortal builds the PORTAL.md markdown for the given boxes: tailnet links
// to each box's web surfaces, plus its public SSH lines for machines off the
// tailnet.
func RenderPortal(cfg config.Config, pods []providers.Box) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# megh boxes\n\n_updated %s · %d box(es)_\n\n",
		time.Now().UTC().Format("2006-01-02 15:04 UTC"), len(pods))
	if len(pods) == 0 {
		b.WriteString("No boxes. Run `megh up <name>` to launch one.\n")
		return b.String()
	}
	for _, p := range pods {
		meta := []string{"`" + p.Status + "`"}
		if p.DataCenter != "" {
			meta = append(meta, p.DataCenter)
		}
		if p.CostPerHr > 0 {
			meta = append(meta, fmt.Sprintf("$%.3f/hr", p.CostPerHr))
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", p.DisplayName(), strings.Join(meta, " · "))
		for _, l := range BoxLinks(cfg, p) {
			fmt.Fprintf(&b, "- [%s](%s)\n", l.Label, l.URL)
		}
		if shell, tunnel := SSHCommands(p); shell != "" {
			fmt.Fprintf(&b, "- ssh: `%s`\n", shell)
			fmt.Fprintf(&b, "- webterm off-tailnet: `%s` then http://localhost:7682\n", tunnel)
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
