package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"

	"github.com/panyam/megh/internal/config"
	"github.com/spf13/cobra"
)

// A control plane is a machine that can SPAWN boxes, REACH them, and HYDRATE
// them. The Mac satisfies all three implicitly, by being the machine everything
// was set up on. A box satisfies none of them until each is arranged, and until
// this command existed you learned the list only by failing through it one
// prerequisite at a time -- eight of them, each surfacing as an unrelated-looking
// error a long way from its cause (a pod nobody could SSH into, a clone that
// could not resolve a host alias, a doctor probe against a name with no DNS).
//
// So the checks are not a health report. They are that list, in the order the
// work hits them, each with the command that fixes it.
type cpCheck struct {
	name   string
	state  cpState
	detail string
	fix    string
}

type cpState int

const (
	cpOK cpState = iota
	cpWarn
	cpFail
)

func (s cpState) String() string {
	switch s {
	case cpOK:
		return "ok"
	case cpWarn:
		return "warn"
	default:
		return "FAIL"
	}
}

func envSet(name string) bool { return name != "" && os.Getenv(name) != "" }

// controlPlaneChecks is pure apart from reading env and stat-ing files, so the
// ordering and verdicts can be exercised without a machine in either state.
func controlPlaneChecks(onABox bool) []cpCheck {
	var out []cpCheck
	add := func(c cpCheck) { out = append(out, c) }

	// 1. May this machine spawn at all? (C3)
	switch {
	case !onABox:
		add(cpCheck{"spawn", cpOK, "not a box; no elevation needed", ""})
	case spawnAllowed(true, false, os.Getenv(controlPlaneEnv)):
		add(cpCheck{"spawn", cpOK, "declared via " + controlPlaneEnv, ""})
	default:
		add(cpCheck{"spawn", cpFail, "this is a box and has not declared itself",
			"export " + controlPlaneEnv + "=1 where this box gets its provider credential"})
	}

	// 2. The provider credential the spawn actually needs.
	provName := cfg.DefaultProvider
	if p, ok := cfg.Providers[provName]; ok && p.APIKeyEnv != "" {
		if envSet(p.APIKeyEnv) {
			add(cpCheck{"provider key", cpOK, p.APIKeyEnv + " is set", ""})
		} else {
			add(cpCheck{"provider key", cpFail, p.APIKeyEnv + " is not set",
				"add " + p.APIKeyEnv + " to this machine's environment"})
		}
	}

	// 3. The box key. Without it `megh up` injects nothing and you get a running
	//    pod you cannot log into -- silently, which is how one was launched today.
	switch {
	case activeProfile != nil:
		if _, err := os.Stat(activeProfile.BoxKeyFile()); err == nil {
			add(cpCheck{"box key", cpOK, "profile " + activeProfile.Name, ""})
		} else {
			add(cpCheck{"box key", cpFail, "profile " + activeProfile.Name + " has no box.key",
				"megh profile create " + activeProfile.Name})
		}
	default:
		path := config.ExpandPath(cfg.SSHPubKeyFile)
		if _, err := os.Stat(path); err == nil {
			add(cpCheck{"box key", cpOK, cfg.SSHPubKeyFile, ""})
		} else {
			add(cpCheck{"box key", cpFail, cfg.SSHPubKeyFile + " does not exist",
				"megh profile create <name> && megh profile use <name>"})
		}
	}

	// 4. A GitHub identity, or hydrate clones nothing. Private keys stay here and
	//    reach a box only through the scoped agent, so this is per machine.
	switch {
	case activeProfile == nil:
		add(cpCheck{"github identity", cpWarn, "no profile; hydrate uses the ambient agent",
			"megh profile create <name> for a scoped one"})
	case len(activeProfile.GHKeyNames()) == 0:
		add(cpCheck{"github identity", cpFail, "profile " + activeProfile.Name + " has no gh keys",
			"megh profile gh add " + orDefaulted(cfg.DefaultGHKey, "<name>") + " --register"})
	case cfg.DefaultGHKey != "" && !activeProfile.HasGHKey(cfg.DefaultGHKey):
		add(cpCheck{"github identity", cpFail,
			fmt.Sprintf("default_gh_key %q missing (have: %v)", cfg.DefaultGHKey, activeProfile.GHKeyNames()),
			"megh profile gh add " + cfg.DefaultGHKey + " --register"})
	default:
		add(cpCheck{"github identity", cpOK, fmt.Sprintf("%v", activeProfile.GHKeyNames()), ""})
	}

	// 5. Whatever megh.yaml declares it needs before launching.
	for _, e := range cfg.Requires.Envs {
		if envSet(e) {
			add(cpCheck{"requires " + e, cpOK, "set", ""})
		} else {
			add(cpCheck{"requires " + e, cpFail, "not set (megh.yaml requires)",
				"add " + e + " to this machine's environment"})
		}
	}

	// 6. Tailnet. A warn, never a fail: without it boxes still launch and megh
	//    still reaches them over public SSH. What you lose is name resolution, so
	//    `megh doctor <box>` and the portal URLs stop working.
	hasNode := envSet(cfg.Tailscale.AuthKeyEnv) || cfg.Tailscale.MintKeys
	hasCP := envSet(cfg.Tailscale.APIKeyEnv) ||
		(envSet(cfg.Tailscale.ClientIDEnv) && envSet(cfg.Tailscale.ClientSecretEnv))
	switch {
	case hasNode && hasCP:
		add(cpCheck{"tailnet", cpOK, "node key and control-plane credential present", ""})
	case hasNode:
		add(cpCheck{"tailnet", cpWarn, "boxes can join; this machine cannot prune stale nodes",
			"set " + cfg.Tailscale.ClientIDEnv + " + " + cfg.Tailscale.ClientSecretEnv})
	default:
		add(cpCheck{"tailnet", cpWarn, "boxes will not join; megh falls back to public SSH",
			"set " + cfg.Tailscale.AuthKeyEnv + ", or enable tailscale.mint_keys"})
	}
	// C5 is stricter than C3 and does not bend: a tailnet control-plane credential
	// can delete any node on the tailnet, this machine's own included.
	if onABox && hasCP {
		add(cpCheck{"tailnet scope", cpWarn,
			"a box holds a tailnet control-plane credential (C5)",
			"scope the credential to tag:megh, or keep it off boxes entirely"})
	}

	// 7. The local backend is a control plane for itself and needs a docker CLI.
	if _, ok := cfg.Providers["docker"]; ok {
		if _, err := exec.LookPath("docker"); err == nil {
			add(cpCheck{"docker backend", cpOK, "docker on PATH", ""})
		} else {
			add(cpCheck{"docker backend", cpWarn, "docker is not on PATH",
				"install a docker CLI, or ignore if you only use cloud boxes"})
		}
	}
	return out
}

var doctorControlPlaneCmd = &cobra.Command{
	Use:     "control-plane",
	Aliases: []string{"cp", "controlplane"},
	Short:   "Can THIS machine spawn, reach and hydrate boxes?",
	Long: `Check whether this machine is a working control plane.

'megh doctor <box>' probes a BOX. This probes the machine you are typing on, which
is a different question and the one that bites when development moves onto boxes:
the Mac satisfies every prerequisite implicitly, a fresh box satisfies none, and
each missing piece surfaces far from its cause.

Exits non-zero if anything is FAIL. Warnings do not fail: they describe reduced
function (no tailnet, no docker CLI), not a blocked one.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := os.Stat(boxMarker)
		onABox := err == nil
		where := "control machine"
		if onABox {
			where = "a megh box"
		}
		fmt.Printf("this machine: %s\n", where)
		if activeProfile != nil {
			fmt.Printf("profile:      %s\n", activeProfile.Name)
		} else {
			fmt.Printf("profile:      (none; using ambient env + megh.yaml)\n")
		}
		fmt.Println()

		checks := controlPlaneChecks(onABox)
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "  CHECK\tSTATE\tDETAIL")
		failed := 0
		for _, c := range checks {
			if c.state == cpFail {
				failed++
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", c.name, c.state, c.detail)
		}
		w.Flush()

		var fixes []cpCheck
		for _, c := range checks {
			if c.fix != "" {
				fixes = append(fixes, c)
			}
		}
		if len(fixes) > 0 {
			fmt.Println("\nto fix:")
			for _, c := range fixes {
				fmt.Printf("  %-18s %s\n", c.name, c.fix)
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d check(s) failed; this machine cannot fully drive boxes", failed)
		}
		fmt.Println("\nthis machine can spawn, reach and hydrate boxes")
		return nil
	},
}
