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
	"strconv"
	"strings"
	"time"

	"github.com/panyam/megh/internal/gateway"
	"github.com/panyam/megh/internal/providers"
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
	Use:   "gateway",
	Short: "Reach workers from this machine without joining the tailnet (a local docker gateway)",
	Long: `A gateway is a docker container on this machine that joins the tailnet
under its own tag (tailscale.gateway_tag, default tag:megh-gw) while this
machine stays off it. It publishes the workers' web surfaces to THIS machine's
127.0.0.1 only, routed by name:

  http://<box>.localhost:7682/   webterm (add ?arg=<session> for a tmux session)
  http://<box>.localhost:7681/   ttyd
  http://<box>.localhost:8080/   code-server
  http://<box>.localhost:6080/   noVNC

It holds no provider or GitHub keys. The tailnet ACL needs tag:megh-gw in
tagOwners and a grant to tag:megh on those ports (SETUP.md, "A gateway").`,
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
		image := gatewayImage()
		switch state := gatewayState(ctx); state {
		case "running":
			fmt.Println("gateway container is running")
		case "":
			// The container runs `megh gateway serve` from the image, so an image
			// built before this command existed can't be a gateway.
			if _, err := dockerOut(ctx, "run", "--rm", "--entrypoint", "megh", image, "gateway", "serve", "--help"); err != nil {
				return fmt.Errorf("%s has no `megh gateway serve`; pull or build a newer image (providers.docker.image, or `make image-local-slim`)", image)
			}
			if _, err := dockerOut(ctx, gatewayRunArgs(image, cfg.Tailnet)...); err != nil {
				return err
			}
			fmt.Printf("started %s from %s\n", gatewayName, image)
		default:
			if _, err := dockerOut(ctx, "start", gatewayName); err != nil {
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
			dockerOut(ctx, "exec", gatewayName, "tailscale", "logout") // best effort; the node is ephemeral anyway
			if _, err := dockerOut(ctx, "rm", "-f", gatewayName); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", gatewayName)
		}
		if gatewayPurge {
			dockerOut(ctx, "volume", "rm", gatewayVolume)
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
			fmt.Println("no gateway container (megh gateway up)")
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
			fmt.Printf("tailnet:   %s (run `megh gateway up` to rejoin; the node is ephemeral and goes once offline a while)\n", st.BackendState)
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
		"exec megh gateway serve"
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
	cmd := dockerCmd(ctx, gatewayJoinArgs()...)
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
		out, err := dockerOut(ctx, "exec", gatewayName, "tailscale", "status", "--json")
		if err == nil && json.Unmarshal([]byte(out), &st) == nil && st.BackendState != "" {
			return st, nil
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("tailscaled in %s did not answer (docker logs %s)", gatewayName, gatewayName)
		}
		time.Sleep(time.Second)
	}
}

// gatewayState is the container's docker state ("running", "exited", ...), or
// "" when there is none.
func gatewayState(ctx context.Context) string {
	out, err := dockerOut(ctx, "inspect", "-f", "{{.State.Status}}", gatewayName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gatewayImage is the docker backend's configured image, else the default.
func gatewayImage() string {
	return cmp.Or(cfg.Provider("docker").Image, cfg.DefaultImage(cmp.Or(cfg.DefaultFlavor, "slim")))
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

// dockerCmd is docker with the configured context (providers.docker.context),
// so the gateway lives in the same daemon as local boxes.
func dockerCmd(ctx context.Context, args ...string) *exec.Cmd {
	if c := cfg.Provider("docker").Context; c != "" {
		args = append([]string{"--context", c}, args...)
	}
	return exec.CommandContext(ctx, "docker", args...)
}

func dockerOut(ctx context.Context, args ...string) (string, error) {
	out, err := dockerCmd(ctx, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func init() {
	gatewayDownCmd.Flags().BoolVar(&gatewayPurge, "purge", false, "also remove the gateway's tailnet state volume")
	gatewayServeCmd.Flags().StringVar(&gatewayServeTailnet, "tailnet", "", "MagicDNS suffix (default $MEGH_GATEWAY_TAILNET)")
	gatewayServeCmd.Flags().StringVar(&gatewayServeSOCKS, "socks", "", "tailscaled SOCKS5 address (default "+gatewaySOCKS+")")
	gatewayCmd.AddCommand(gatewayUpCmd, gatewayDownCmd, gatewayStatusCmd, gatewayServeCmd)
	rootCmd.AddCommand(gatewayCmd)
}
