package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/panyam/megh/internal/providers"
	"github.com/spf13/cobra"
)

// `megh mesh` is a box's membership in the overlay network named by
// `providers.<name>.mesh`. It is a separate step from `megh up` on purpose.
//
// Creating a box and putting it on a network are different decisions, and
// joining is the one that can fail for reasons that have nothing to do with the
// box: a tailnet ACL, an expired credential, a tag that does not exist. Folding
// it into `up` made a launch wait on all of that and then report a half-success.
// Joining afterwards also means the node key travels on the stdin of one SSH
// call instead of living in the container's stored env for the life of the box.
//
// A cloud pod is the exception and still joins at boot (Mesh.AtBoot): there is
// no SSH to join it over yet, and with expose_ssh: false the mesh is the only
// way in. `mesh join` still works there, as a re-key.
//
// Diagnosis and repair stay under `megh doctor ts`, which is Tailscale-specific
// by nature. This group is membership, and it reads the same whatever vendor
// `mesh:` names.

var (
	meshProvider string
	meshAuthKey  string
	meshLocal    bool
)

// meshHelperAction maps a `megh mesh` verb to the ts-up.sh action behind it.
//
// `join` covers what used to be two commands. `doctor ts start` and `doctor ts
// setkey` both ran the script's `up`, differing only in whether a fresh key was
// injected, so they were one action wearing two names: `megh mesh join` reuses
// the box's stored auth, and `--authkey` (or a minted key) re-keys it.
func meshHelperAction(verb string) (string, error) {
	switch verb {
	case "join":
		return "up", nil
	case "status":
		return "status", nil
	case "logs":
		return "logs", nil
	case "restart":
		return "restart", nil
	}
	return "", fmt.Errorf("no bring-up action for %q", verb)
}

var meshCmd = &cobra.Command{
	Use:   "mesh <join|leave|ls|status|logs|restart|gc>",
	Short: "Everything about a box's membership in the overlay network",
	Long: `Manage a box's membership in the mesh named by providers.<name>.mesh.

  join     bring the box up on the mesh and serve its surfaces there;
           --authkey (or a minted key) re-keys a box whose key went stale
  leave    log the box out; an ephemeral node disappears with it
  ls       every box, whether it is on, its node address and served ports
  status   what the box's own daemon reports
  logs     the bring-up log, the daemon log and status (what failed)
  restart  bounce the daemon, then bring it back up
  gc       delete nodes left behind by boxes that no longer exist
           (acts on the control plane, not on a box; see megh mesh gc -h)

A box on the mesh is reachable from any device on it, which an SSH tunnel
(megh browse) never is: a tunnel only reaches the machine that opened it.

These verbs used to live under "megh doctor ts", which grouped them by vendor
and put a routine step (join this box) behind a word that means repair. Health
probes stay where they were: "megh doctor <box>" for a box, and
"megh doctor control-plane" for this machine.`,
}

// meshOn resolves the provider and refuses early when it has no mesh, naming the
// setting to add. Without this the failure is a successful-looking join followed
// by a box that nothing can reach by name.
func meshOn(cmd *cobra.Command) (providers.Provider, providers.Mesh, error) {
	prov, err := resolveProvider(cmd, meshProvider)
	if err != nil {
		return nil, providers.Mesh{}, err
	}
	m := prov.Mesh()
	if !m.On() {
		return nil, m, fmt.Errorf("%s boxes join no mesh; set providers.%s.mesh: %s in megh.yaml",
			prov.Name(), prov.Name(), providers.MeshTailscale)
	}
	return prov, m, nil
}

// meshJoinKey resolves the node key to hand the box: --authkey, else one minted
// for this box, else the ambient static key. Empty is a legitimate answer and
// not an error: the script then reuses the auth the box already has, which is
// what re-running a join on a box that is already on the mesh does.
func meshJoinKey(ctx context.Context, box string) string {
	if meshAuthKey != "" {
		return meshAuthKey
	}
	if key := mintBoxAuthKey(ctx, box); key != "" {
		return key
	}
	if key := os.Getenv("TS_AUTHKEY"); key != "" {
		return key
	}
	fmt.Fprintf(os.Stderr, "megh: no key to offer; reusing whatever auth %s already has\n", box)
	return ""
}

var meshLeaveCmd = &cobra.Command{
	Use:   "leave [box]",
	Short: "Log a box out of the mesh (an ephemeral node disappears)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, _, err := meshOn(cmd)
		if err != nil {
			return err
		}
		ctx := context.Background()
		pod, err := providers.FindOrSole(ctx, prov, args)
		if err != nil {
			return err
		}
		d := dialFor(pod)
		if err := d.preflight(pod); err != nil {
			return err
		}
		if msg := meshLeave(ctx, d, pod.DisplayName()); msg != "" {
			fmt.Println(msg)
		}
		return nil
	},
}

// meshLogoutScript logs the box out, and says so only when the box was actually
// connected. `megh down` used to announce a departure for any backend with a
// mesh, including a local box that never joined one.
const meshLogoutScript = `sock=/var/run/tailscale/tailscaled.sock
[ -S "$sock" ] || exit 0
timeout 10 tailscale --socket="$sock" status >/dev/null 2>&1 || exit 0
timeout 15 tailscale --socket="$sock" logout >/dev/null 2>&1 || true
echo left
`

// meshLeave runs the logout on a box and returns what to tell the user, or ""
// when there is nothing worth saying.
func meshLeave(ctx context.Context, d dial, box string) string {
	out, err := sshCaptureIn(ctx, d.keyFor(cfg.SSHKeyFile), d, "bash -s", strings.NewReader(meshLogoutScript))
	return meshLeaveMessage(box, out, err)
}

func meshLeaveMessage(box, out string, err error) string {
	if err != nil {
		return fmt.Sprintf("note: could not reach %s to leave the mesh", box)
	}
	if strings.TrimSpace(out) == "left" {
		return fmt.Sprintf("%s left the mesh", box)
	}
	return ""
}

// meshProbeScript asks a box the four things `mesh ls` prints, in one round
// trip, as key=value lines. Parsing tailscale's own table would make a column
// here depend on a format tailscale is free to change; a key it stops emitting
// leaves an empty cell instead of shifting every other field.
//
// jq is used where it exists and skipped where it does not, the same way
// ts-up.sh treats it, so the probe works on any image.
const meshProbeScript = `sock=/var/run/tailscale/tailscaled.sock
ts() { tailscale --socket="$sock" "$@"; }
command -v tailscale >/dev/null 2>&1 || { echo "state=absent"; exit 0; }
[ -S "$sock" ] || { echo "state=Stopped"; exit 0; }
js=$(timeout 10 ts status --json 2>/dev/null) || { echo "state=Stopped"; exit 0; }
echo "state=$(printf '%s' "$js" | grep -o '"BackendState": *"[^"]*"' | head -1 | cut -d'"' -f4)"
echo "ip=$(ts ip -4 2>/dev/null | head -1)"
if command -v jq >/dev/null 2>&1; then
  echo "name=$(printf '%s' "$js" | jq -r '.Self.DNSName // ""' | sed 's/\.$//')"
  echo "ports=$(ts serve status --json 2>/dev/null | jq -r '((.TCP // {}) | keys | join(",")) // ""')"
fi
exit 0
`

// meshState is what a box reports about its own membership.
type meshState struct {
	Up    bool
	IP    string
	Name  string
	Ports string
}

func parseMeshProbe(out string) meshState {
	var s meshState
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || v == "" {
			continue
		}
		switch k {
		case "state":
			s.Up = v == "Running"
		case "ip":
			s.IP = v
		case "name":
			s.Name = v
		case "ports":
			s.Ports = v
		}
	}
	return s
}

// meshBoxCmd builds a subcommand that runs one bring-up action against a box.
// The verbs differ only in the action and in whether they offer a key, so they
// are one function rather than four near-copies.
func meshBoxCmd(verb, short string, withKey bool) *cobra.Command {
	c := &cobra.Command{
		Use:   verb + " [box]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action, err := meshHelperAction(verb)
			if err != nil {
				return err
			}
			// --local is how the entrypoint brings a box up on its own boot: no
			// provider, no SSH, no config to consult. It deliberately skips the
			// mesh-configured check, because a box's baked megh.yaml knows nothing
			// about the control machine's settings.
			if meshLocal {
				key := meshAuthKey
				if key == "" {
					key = os.Getenv("TS_AUTHKEY")
				}
				return tsBringUpLocal(action, key)
			}
			prov, _, err := meshOn(cmd)
			if err != nil {
				return err
			}
			ctx := context.Background()
			pod, err := providers.FindOrSole(ctx, prov, args)
			if err != nil {
				return err
			}
			if withKey {
				pod = awaitSSHReady(ctx, prov, pod)
			}
			d := dialFor(pod)
			if err := d.preflight(pod); err != nil {
				return err
			}
			key := ""
			if withKey {
				key = meshJoinKey(ctx, pod.DisplayName())
			}
			fmt.Fprintf(os.Stderr, "megh: mesh %s on %s\n", verb, pod.DisplayName())
			return tsBringUp(d, pod.DisplayName(), key, action)
		},
	}
	c.Flags().StringVar(&meshProvider, "provider", "", "provider (default: config default_provider, else runpod)")
	if withKey {
		c.Flags().StringVar(&meshAuthKey, "authkey", "", "node key to join with (default: minted, else $TS_AUTHKEY)")
		c.Flags().BoolVar(&meshLocal, "local", false, "run on the box itself instead of ssh-ing to one")
	}
	return c
}

var meshLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "Show which boxes are on the mesh, with their node address and ports",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		prov, m, err := meshOn(cmd)
		if err != nil {
			return err
		}
		ctx := context.Background()
		pods, err := prov.List(ctx)
		if err != nil {
			return err
		}
		pods = providers.Managed(pods)
		if len(pods) == 0 {
			fmt.Println("no boxes")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tMESH\tSTATE\tADDRESS\tSERVING")
		for _, p := range pods {
			d := dialFor(&p)
			state, addr, ports := "unreachable", "-", "-"
			if err := d.preflight(&p); err == nil {
				out, err := sshCaptureIn(ctx, d.keyFor(cfg.SSHKeyFile), d, "bash -s", strings.NewReader(meshProbeScript))
				if err == nil {
					got := parseMeshProbe(out)
					state = "off"
					if got.Up {
						state = "on"
					}
					if addr = got.Name; addr == "" {
						addr = got.IP
					}
					if addr == "" {
						addr = "-"
					}
					if got.Ports != "" {
						ports = got.Ports
					}
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.DisplayName(), m.Vendor, state, addr, ports)
		}
		return w.Flush()
	},
}

func init() {
	for _, c := range []*cobra.Command{meshLeaveCmd, meshLsCmd} {
		c.Flags().StringVar(&meshProvider, "provider", "", "provider (default: config default_provider, else runpod)")
		meshCmd.AddCommand(c)
	}
	meshCmd.AddCommand(
		meshBoxCmd("join", "Bring a box up on the mesh, or re-key one whose key went stale", true),
		meshBoxCmd("status", "What the box's own daemon reports", false),
		meshBoxCmd("logs", "The bring-up log, the daemon log and status (what failed)", false),
		meshBoxCmd("restart", "Bounce the daemon, then bring it back up", false),
		newTSGCCmd(),
	)
	rootCmd.AddCommand(meshCmd)
}
