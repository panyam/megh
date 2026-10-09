package cmd

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/panyam/megh/internal/gateway"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/providers/local"
	"github.com/panyam/megh/internal/tsapi"
	"github.com/spf13/cobra"
)

// The gateway role (DESIGN.md "Roles"): a local container on the tailnet that
// forwards this machine's browser to workers, so the machine itself need not
// join. It holds a tailnet identity and nothing else.
const (
	gatewayName   = "megh-gw"
	gatewayVolume = "megh-gw-tailscale"
	gatewaySOCKS  = "127.0.0.1:1055"
)

var gatewayPurge bool

var gatewayCmd = &cobra.Command{
	Use:     "gw",
	Aliases: []string{"gateway"},
	Short:   "Reach workers from this machine without joining the tailnet (a local container gateway)",
	Long: `A gateway is a container (podman or docker) on this machine that joins the tailnet
under its own tag (tailscale.gateway_tag, default tag:megh-gw) while this
machine stays off it. It publishes the workers' web surfaces to THIS machine's
127.0.0.1 only, routed by name:

  http://<box>.localhost:7682/   webterm (add ?arg=<session> for a tmux session)
  http://<box>.localhost:7681/   ttyd
  http://<box>.localhost:8080/   code-server
  http://<box>.localhost:6080/   noVNC

megh ssh, tmux, browse, hydrate and the rest use it on their own: while it runs,
ssh to a cloud box goes by its tailnet name through the container (a
ProxyCommand), with this machine's ssh client and GitHub agent. Workers run
Tailscale SSH, which authorizes the gateway node, so no box key is needed.

It holds no provider or GitHub keys. The tailnet ACL needs tag:megh-gw in
tagOwners, a grant to tag:megh on those ports, and an ssh rule (SETUP.md
section 10).`,
}

var gatewayUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the gateway container and join it to the tailnet",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		if cfg.Tailnet == "" {
			return fmt.Errorf("set tailnet: in megh.yaml (the MagicDNS suffix, e.g. tail1234.ts.net); the gateway routes <box>.localhost to <box>.<tailnet>")
		}
		e, err := localEngine()
		if err != nil {
			return err
		}
		fmt.Printf("engine: %s (%s)\n", e.Bin, e.Source)
		warnGatewayElsewhere(ctx, e)
		image := gatewayImage()
		switch state := gatewayState(ctx); state {
		case "running":
			fmt.Println("gateway container is running")
		case "":
			// The container runs `megh gw serve` and `megh gw nc` from the image,
			// so an image older than both can't be a gateway.
			if out, err := engineOut(ctx, "run", "--rm", "--entrypoint", "megh", image, "gw", "nc", "--help"); err != nil {
				e, _ := localEngine()
				return gatewayImageCheckError(image, e.Name, out, err)
			}
			if _, err := engineOut(ctx, gatewayRunArgs(image, cfg.Tailnet)...); err != nil {
				return err
			}
			fmt.Printf("started %s from %s\n", gatewayName, image)
		default:
			if _, err := engineOut(ctx, "start", gatewayName); err != nil {
				return err
			}
			fmt.Printf("restarted %s (was %s)\n", gatewayName, state)
		}
		st, err := gatewayTailscale(ctx, 30*time.Second)
		if err != nil {
			return err
		}
		if st.BackendState != "Running" {
			if err := gatewayJoin(ctx); err != nil {
				return err
			}
		} else {
			fmt.Printf("on the tailnet as %s\n", strings.TrimSuffix(st.Self.DNSName, "."))
		}
		fmt.Print(gatewayURLs(knownBoxes(ctx)))
		return nil
	},
}

var gatewayDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the gateway: leave the tailnet and remove the container (--purge also drops its state volume)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		if gatewayState(ctx) == "" {
			fmt.Println("no gateway container")
		} else {
			engineOut(ctx, "exec", gatewayName, "tailscale", "logout") // best effort; the node is ephemeral anyway
			if _, err := engineOut(ctx, "rm", "-f", gatewayName); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", gatewayName)
		}
		if gatewayPurge {
			engineOut(ctx, "volume", "rm", gatewayVolume)
			fmt.Printf("removed volume %s\n", gatewayVolume)
		}
		return nil
	},
}

var gatewayStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Is the gateway running and on the tailnet, and the URLs it serves",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		state := gatewayState(ctx)
		if state == "" {
			fmt.Println("no gateway container (megh gw up)")
			return nil
		}
		fmt.Printf("container: %s\n", state)
		if state != "running" {
			return nil
		}
		st, err := gatewayTailscale(ctx, 5*time.Second)
		switch {
		case err != nil:
			fmt.Printf("tailnet:   unknown (%v)\n", err)
		case st.BackendState == "Running":
			fmt.Printf("tailnet:   on as %s\n", strings.TrimSuffix(st.Self.DNSName, "."))
		default:
			fmt.Printf("tailnet:   %s (run `megh gw up` to rejoin; the node is ephemeral and goes once offline a while)\n", st.BackendState)
		}
		fmt.Print(gatewayURLs(knownBoxes(ctx)))
		return nil
	},
}

var gatewayServeTailnet, gatewayServeSOCKS string

// gatewayServeCmd runs INSIDE the gateway container: the web proxy on every
// surface port, dialling workers through tailscaled's SOCKS5 server.
var gatewayServeCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Run the gateway's proxy (inside the gateway container)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tailnet := cmp.Or(gatewayServeTailnet, os.Getenv("MEGH_GATEWAY_TAILNET"))
		if tailnet == "" {
			return fmt.Errorf("no tailnet (--tailnet or MEGH_GATEWAY_TAILNET)")
		}
		p := &gateway.Proxy{Tailnet: tailnet, Dial: gateway.SOCKS5Dialer(gatewaySOCKS)}
		if gatewayServeSOCKS != "" {
			p.Dial = gateway.SOCKS5Dialer(gatewayServeSOCKS)
		}
		errc := make(chan error, len(gateway.Ports))
		for _, port := range gateway.Ports {
			// 0.0.0.0 inside the container is the C4 exception: a docker publish
			// forwards to eth0, and every publish is bound to the host's
			// 127.0.0.1, so nothing beyond the host reaches these.
			srv := &http.Server{Addr: net.JoinHostPort("0.0.0.0", strconv.Itoa(port)), Handler: p.Handler(port), ReadHeaderTimeout: 10 * time.Second}
			go func() { errc <- srv.ListenAndServe() }()
		}
		fmt.Printf("megh gateway: forwarding <box>.localhost:%v to <box>.%s\n", gateway.Ports, tailnet)
		return <-errc
	},
}

// gatewayRunArgs is the docker run for the gateway container. Every publish is
// bound to the host's 127.0.0.1, SOCKS stays inside, the tailnet state lives
// in a named volume so a restart rejoins without a key, and no key is here:
// gatewayJoin hands it over on stdin.
func gatewayRunArgs(image, tailnet string) []string {
	args := []string{"run", "-d", "--name", gatewayName, "--hostname", gatewayName,
		"--restart", "unless-stopped", "-v", gatewayVolume + ":/var/lib/tailscale",
		"-e", "MEGH_GATEWAY_TAILNET=" + tailnet}
	for _, p := range gateway.Ports {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", p, p))
	}
	script := "mkdir -p /var/run/tailscale /var/lib/tailscale; " +
		"tailscaled --tun=userspace-networking --state=/var/lib/tailscale/tailscaled.state " +
		"--socket=/var/run/tailscale/tailscaled.sock --socks5-server=" + gatewaySOCKS + " >/var/log/tailscaled.log 2>&1 & " +
		"exec megh gw serve"
	return append(args, "--entrypoint", "sh", image, "-c", script)
}

// gatewayJoinArgs runs tailscale up inside the gateway with the node key read
// from stdin.
func gatewayJoinArgs() []string {
	return []string{"exec", "-i", gatewayName, "sh", "-c",
		`tailscale up --authkey="$(cat)" --hostname=` + gatewayName + ` --accept-dns=false`}
}

// gatewayJoin mints a single-use, ephemeral key under the gateway tag and
// joins with it.
func gatewayJoin(ctx context.Context) error {
	c, err := tsClient()
	if err != nil {
		return fmt.Errorf("can't mint a key for the gateway: %w (set MEGH_TAILSCALE_CLIENT_ID and _SECRET)", err)
	}
	tag := cmp.Or(cfg.Tailscale.GatewayTag, "tag:megh-gw")
	key, err := c.MintAuthKey(ctx, tsapi.AuthKeyOptions{Box: gatewayName, Tags: []string{tag}, Expiry: 10 * time.Minute})
	if err != nil {
		return fmt.Errorf("could not mint a %s key (%v): add %s to the tailnet ACL tagOwners, owned by the OAuth client's tag (SETUP.md, \"A gateway\")", tag, err, tag)
	}
	cmd, err := engineCmd(ctx, gatewayJoinArgs()...)
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tailscale up in the gateway: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("joined the tailnet under %s\n", tag)
	return nil
}

type gatewayTS struct {
	BackendState string
	Self         struct {
		DNSName string
		Online  bool
	}
}

// gatewayTailscale waits up to wait for tailscaled in the gateway to answer.
func gatewayTailscale(ctx context.Context, wait time.Duration) (gatewayTS, error) {
	var st gatewayTS
	deadline := time.Now().Add(wait)
	for {
		out, err := engineOut(ctx, "exec", gatewayName, "tailscale", "status", "--json")
		if err == nil && json.Unmarshal([]byte(out), &st) == nil && st.BackendState != "" {
			return st, nil
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("tailscaled in %s did not answer (`<engine> logs %s`)", gatewayName, gatewayName)
		}
		time.Sleep(time.Second)
	}
}

// gatewayState is the container's docker state ("running", "exited", ...), or
// "" when there is none.
func gatewayState(ctx context.Context) string {
	out, err := engineOut(ctx, "inspect", "-f", "{{.State.Status}}", gatewayName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// warnGatewayElsewhere names a gateway container left in the OTHER engine,
// typically from before podman was detected. It is invisible to this one, so
// nothing else would mention it, yet while it runs it is still a tailnet node
// that is root on every worker.
func warnGatewayElsewhere(ctx context.Context, e local.Engine) {
	o, ok := e.OtherInstalled()
	if !ok {
		return
	}
	state, err := o.Command(ctx, "inspect", "-f", "{{.State.Status}}", gatewayName).Output()
	if err != nil {
		return // none there, or that engine isn't running
	}
	fmt.Fprintf(os.Stderr, "megh: warning: a %s container also exists under %s (%s). It is still a tailnet node with root on your workers; remove it with:\n  %s rm -f %s && %s volume rm %s\n",
		gatewayName, o.Name, strings.TrimSpace(string(state)), o.Bin, gatewayName, o.Bin, gatewayVolume)
}

// gatewayImage is tailscale.gateway_image, else the published megh-gw image
// (env/gw/Dockerfile): tailscale plus the megh binary, never a dev image.
func gatewayImage() string {
	return cmp.Or(cfg.Tailscale.GatewayImage, cfg.DefaultImage("gw"))
}

// knownBoxes is the boxes this machine can list (any backend with a key), for
// printing their URLs. None known still prints the URL shape.
func knownBoxes(ctx context.Context) []string {
	boxes, _ := providers.ListAll(ctx)
	var names []string
	for _, b := range providers.Managed(boxes) {
		names = append(names, b.DisplayName())
	}
	return names
}

func gatewayURLs(boxes []string) string {
	if len(boxes) == 0 {
		boxes = []string{"<box>"}
	}
	var b strings.Builder
	b.WriteString("open in a browser on this machine:\n")
	for _, n := range boxes {
		fmt.Fprintf(&b, "  %-12s webterm http://%s.localhost:7682/   ttyd http://%s.localhost:7681/   code http://%s.localhost:8080/\n", n, n, n, n)
	}
	b.WriteString("  add ?arg=<session> to a terminal URL for a named tmux session\n")
	return b.String()
}

// localEngine is the engine local boxes run under (podman or docker,
// providers.local.engine), so the gateway lives beside them.
// Tests replace it.
var localEngine = func() (local.Engine, error) { return local.ResolveEngine(cfg.Provider("local")) }

// engineCmd is the engine CLI with its context or connection flag.
func engineCmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	e, err := localEngine()
	if err != nil {
		return nil, err
	}
	return e.Command(ctx, args...), nil
}

func engineOut(ctx context.Context, args ...string) (string, error) {
	c, err := engineCmd(ctx, args...)
	if err != nil {
		return "", err
	}
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", filepath.Base(c.Path), args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

var gatewayNCCmd = &cobra.Command{
	Use:    "nc <host> <port>",
	Short:  "Pipe stdin/stdout to a worker over the gateway's tailnet (ssh's ProxyCommand; runs inside the container)",
	Hidden: true,
	Args:   cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return gateway.Pipe(context.Background(), gateway.SOCKS5Dialer(gatewaySOCKS), net.JoinHostPort(args[0], args[1]), os.Stdin, os.Stdout)
	},
}

var gatewayShellCmd = &cobra.Command{
	Use:   "shell",
	Short: "A shell inside the gateway container, for looking at tailscale (status, logs)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if gatewayState(context.Background()) != "running" {
			return fmt.Errorf("the gateway isn't running (megh gw up)")
		}
		c, err := engineCmd(context.Background(), gatewayShellArgs()...)
		if err != nil {
			return err
		}
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	},
}

// gatewayImageCheckError says why the image check failed: an image too old
// to have `gw nc`, a pull the registry refused (log in), or anything else,
// shown as the engine reported it rather than blamed on the login.
func gatewayImageCheckError(image, engine, out string, err error) error {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "unknown command"):
		return fmt.Errorf("%s has no `megh gw nc`; pull a newer one, or build one with `make image-local-gw` and set tailscale.gateway_image", image)
	case strings.Contains(low, "unauthorized"), strings.Contains(low, "denied"), strings.Contains(low, "authentication required"):
		return fmt.Errorf("can't pull %s: the registry refused it; a private GHCR image needs `%s login ghcr.io` with GH_MEGH_TOKEN first (%s)", image, engine, strings.TrimSpace(out))
	default:
		msg := err.Error()
		if o := strings.TrimSpace(out); o != "" && !strings.Contains(msg, o) {
			msg += ": " + o
		}
		return fmt.Errorf("can't run %s: %s", image, msg)
	}
}

// gatewayShellArgs opens sh in the gateway: the image is Alpine, with no bash.
func gatewayShellArgs() []string { return []string{"exec", "-it", gatewayName, "sh"} }

// gatewayProxyCommand is the ssh ProxyCommand that carries a connection
// through the gateway: `megh gw nc` inside the container, through the same
// engine and context or connection as `megh gw up` used.
func gatewayProxyCommand(e local.Engine) string {
	return e.CommandLine("exec", "-i", gatewayName, "megh", "gw", "nc", "%h", "%p")
}

func init() {
	gatewayDownCmd.Flags().BoolVar(&gatewayPurge, "purge", false, "also remove the gateway's tailnet state volume")
	gatewayServeCmd.Flags().StringVar(&gatewayServeTailnet, "tailnet", "", "MagicDNS suffix (default $MEGH_GATEWAY_TAILNET)")
	gatewayServeCmd.Flags().StringVar(&gatewayServeSOCKS, "socks", "", "tailscaled SOCKS5 address (default "+gatewaySOCKS+")")
	gatewayCmd.AddCommand(gatewayUpCmd, gatewayDownCmd, gatewayStatusCmd, gatewayShellCmd, gatewayServeCmd, gatewayNCCmd)
	rootCmd.AddCommand(gatewayCmd)
}
