package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/panyam/megh/internal/tsapi"
	"github.com/spf13/cobra"
)

// A terminated box leaves its tailnet node behind whenever the node-side
// `tailscale logout` in `megh down` could not run, which is precisely when the
// box was unreachable or was killed out of band. Tailscale then refuses to
// reuse the name, so the next box of that name comes up as <name>-1 and the
// suffix climbs with every rebuild. These helpers delete the node from the
// control plane, which is the only path that works when the box is already gone.

var (
	tsAPIKey     string
	tsTailnet    string
	tsGCYes      bool
	tsGCDryRun   bool
	tsGCStale    time.Duration
	tsGCTag      string
	tsGCProvider string
)

// tsClient builds a control-plane client from the flag, then the environment,
// with the tailnet from --tailnet or megh.yaml.
func tsClient() (*tsapi.Client, error) {
	tailnet := tsTailnet
	if tailnet == "" {
		tailnet = cfg.Tailnet
	}
	if tsAPIKey != "" {
		return tsapi.New(tsAPIKey, tailnet)
	}
	return tsapi.NewWithCreds(tsapi.Creds{
		APIKey:       os.Getenv(cfg.Tailscale.APIKeyEnv),
		ClientID:     os.Getenv(cfg.Tailscale.ClientIDEnv),
		ClientSecret: os.Getenv(cfg.Tailscale.ClientSecretEnv),
	}, tailnet)
}

// liveBoxNames is the set of box names that currently exist at the provider. It
// is the guard on every delete: a node whose box is still running is never
// debris, whatever its name looks like.
func liveBoxNames(ctx context.Context) (map[string]bool, error) {
	pods, err := runpod.List(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, p := range runpod.ManagedPods(pods) {
		live[p.DisplayName()] = true
	}
	return live, nil
}

// pruneNodesFor deletes the tailnet nodes belonging to one box name, including
// the -N variants that accumulated under it. A variant whose name matches a
// DIFFERENT live box is left alone; `box` itself is always fair game, since the
// caller has just terminated it.
func pruneNodesFor(ctx context.Context, c *tsapi.Client, box string, live map[string]bool) (deleted, kept []string, err error) {
	devices, err := c.Devices(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range devices {
		name := d.BareName()
		if !tsapi.MatchName(d, box) {
			continue
		}
		if name != box && live[name] {
			kept = append(kept, name)
			continue
		}
		if err := c.Delete(ctx, d.ID); err != nil {
			return deleted, kept, fmt.Errorf("delete %s (%s): %w", name, d.ID, err)
		}
		deleted = append(deleted, name)
	}
	return deleted, kept, nil
}

// pruneNodesBestEffort is the `megh down` path. Cleaning the tailnet must never
// turn a successful termination into a failed command, so every problem here is
// a note rather than an error, and no key configured at all is silent.
func pruneNodesBestEffort(ctx context.Context, box string) {
	c, err := tsClient()
	if err != nil {
		return // no API key configured; the SSH logout above was the only path
	}
	live, err := liveBoxNames(ctx)
	if err != nil {
		live = map[string]bool{} // provider unreachable: fall back to name matching alone
	}
	delete(live, box) // we just terminated it
	deleted, _, err := pruneNodesFor(ctx, c, box, live)
	switch {
	case err != nil:
		fmt.Printf("note: could not remove %s from the tailnet (%v)\n", box, err)
	case len(deleted) > 0:
		fmt.Printf("removed %d tailnet node(s): %s\n", len(deleted), strings.Join(deleted, ", "))
	}
}

var tsGCCmd = &cobra.Command{
	Use:   "gc [name...]",
	Short: "Delete tailnet nodes left behind by boxes that no longer exist",
	Long: `Remove stale Tailscale nodes from the control plane.

A node outlives its box whenever the logout in 'megh down' could not run: the
box was unreachable, or it was killed outside megh. Tailscale will not reuse a
name an offline node still holds, so the next box of that name joins as
<name>-1 and the suffix climbs on every rebuild.

  megh doctor ts gc devbox     delete devbox and its devbox-1/-2/... variants
  megh doctor ts gc            sweep every stale node with no live box

Naming a box is the precise form and is what you want for known debris. The
bare sweep is a best guess: megh keeps no local state, so it cannot prove a node
was ever one of its boxes. It therefore lists candidates and asks first, and it
only ever considers nodes that are offline and have no matching live box. Narrow
it with --tag if you mint auth keys with a tag.

A node for a box that is still running is never deleted.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if tsGCProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", tsGCProvider)
		}
		ctx := context.Background()
		c, err := tsClient()
		if err != nil {
			return err
		}
		live, err := liveBoxNames(ctx)
		if err != nil {
			return fmt.Errorf("could not list boxes to check against: %w", err)
		}

		devices, err := c.Devices(ctx)
		if err != nil {
			return err
		}
		// A tag-scoped credential can only delete devices carrying its tag, so a
		// bare sweep that offered anything else would list candidates it cannot
		// actually remove. Default the sweep to the configured tag and say so,
		// which also means the sweep structurally cannot propose a personal
		// device. Naming boxes explicitly still bypasses this, since that is how
		// you clear untagged debris from before tagging existed.
		tag := tsGCTag
		if tag == "" && len(args) == 0 && cfg.Tailscale.Tag != "" && !cmd.Flags().Changed("tag") {
			tag = cfg.Tailscale.Tag
			fmt.Printf("sweeping only %s devices (pass --tag \"\" to consider every node)\n", tag)
		}
		candidates := gcCandidates(devices, args, live, time.Now(), tsGCStale, tag)
		if len(candidates) == 0 {
			fmt.Println("no stale tailnet nodes")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tOS\tLAST SEEN\tID")
		for _, d := range candidates {
			seen := "unknown"
			if !d.LastSeen.IsZero() {
				seen = units(time.Since(d.LastSeen)) + " ago"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.BareName(), d.OS, seen, d.ID)
		}
		w.Flush()

		if tsGCDryRun {
			fmt.Printf("\ndry run: %d node(s) would be deleted\n", len(candidates))
			return nil
		}
		if !tsGCYes {
			fmt.Printf("\ndelete these %d tailnet node(s)? [y/N]: ", len(candidates))
			var resp string
			fmt.Scanln(&resp)
			if !strings.EqualFold(strings.TrimSpace(resp), "y") {
				fmt.Println("aborted")
				return nil
			}
		}
		var failed int
		for _, d := range candidates {
			if err := c.Delete(ctx, d.ID); err != nil {
				fmt.Printf("  %s: %v\n", d.BareName(), err)
				if isScopeError(err) {
					fmt.Printf("      the credential is tag-scoped, so it cannot delete an untagged device;\n")
					fmt.Printf("      remove it in the admin console, or use a credential with wider scope\n")
				}
				failed++
				continue
			}
			fmt.Printf("  deleted %s\n", d.BareName())
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d deletes failed", failed, len(candidates))
		}
		return nil
	},
}

// gcCandidates picks the nodes to offer for deletion. With names, it matches
// those boxes and their -N variants. Without, it sweeps nodes that are stale
// and unclaimed. Either way a node whose name is a live box is excluded, and so
// is anything outside --tag when one is given.
// isScopeError spots the failure mode a tag-scoped credential produces when
// asked to touch a device outside its tag, so the message explains the cause
// rather than showing a bare status code.
func isScopeError(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "403") || strings.Contains(m, "forbidden") ||
		strings.Contains(m, "scope") || strings.Contains(m, "not permitted")
}

func gcCandidates(devices []tsapi.Device, names []string, live map[string]bool, now time.Time, staleAfter time.Duration, tag string) []tsapi.Device {
	var out []tsapi.Device
	for _, d := range devices {
		name := d.BareName()
		if live[name] {
			continue
		}
		if tag != "" && !slices.Contains(d.Tags, tag) {
			continue
		}
		if len(names) > 0 {
			matched := false
			for _, n := range names {
				if tsapi.MatchName(d, n) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		} else if !d.Stale(now, staleAfter) {
			// The bare sweep cannot tell a megh box from your laptop, so it only
			// ever considers nodes that have been gone a while.
			continue
		}
		out = append(out, d)
	}
	return out
}

// units renders a duration the way a person reads it, coarsely.
func units(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func init() {
	f := tsGCCmd.Flags()
	f.StringVar(&tsAPIKey, "api-key", "", "Tailscale API access token (default: $MEGH_TAILSCALE_API_KEY)")
	f.StringVar(&tsTailnet, "tailnet", "", "tailnet to operate on (default: megh.yaml tailnet, else the token's own)")
	f.BoolVarP(&tsGCYes, "yes", "y", false, "skip the confirmation prompt")
	f.BoolVar(&tsGCDryRun, "dry-run", false, "list what would be deleted and stop")
	f.DurationVar(&tsGCStale, "stale-after", 15*time.Minute, "how long a node must have been offline to be swept (bare sweep only)")
	f.StringVar(&tsGCTag, "tag", "", "only consider nodes carrying this tag (e.g. tag:megh)")
	f.StringVar(&tsGCProvider, "provider", "runpod", "provider (runpod)")
	tsCmd.AddCommand(tsGCCmd)
}
