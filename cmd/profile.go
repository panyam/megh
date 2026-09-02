package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/panyam/megh/internal/profile"
	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage megh profiles (box key + GitHub keys + secrets per context)",
}

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a profile with a dedicated box-access key and a secrets template",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := profile.Create(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("created profile %q at %s\n", p.Name, p.Dir)
		fmt.Println("  box key: used to SSH into VMs (its pubkey is injected automatically)")
		fmt.Printf("\nNext:\n")
		fmt.Printf("  1. Add a GitHub identity:  megh profile gh add <name> --profile %s\n", p.Name)
		fmt.Printf("  2. Optionally fill secrets: %s\n", p.SecretsFile())
		fmt.Printf("  3. megh profile use %s\n", p.Name)
		return nil
	},
}

var profileUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the active profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := profile.Use(args[0]); err != nil {
			return err
		}
		fmt.Printf("active profile: %s\n", args[0])
		return nil
	},
}

var profileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List profiles (active marked with *)",
	RunE: func(cmd *cobra.Command, args []string) error {
		active := profile.ActiveName(profileFlag)
		names := profile.List()
		if len(names) == 0 {
			fmt.Println("no profiles (create one: megh profile create <name>)")
			return nil
		}
		for _, n := range names {
			mark := "  "
			if n == active {
				mark = "* "
			}
			fmt.Printf("%s%s\n", mark, n)
		}
		return nil
	},
}

var profileShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the active profile (box key, GitHub identities, secrets set)",
	RunE: func(cmd *cobra.Command, args []string) error {
		name := profile.ActiveName(profileFlag)
		p, ok := profile.Get(name)
		if !ok {
			fmt.Printf("active profile: %s (not created; using ambient env + megh.yaml)\n", name)
			return nil
		}
		fmt.Printf("active profile: %s\n", p.Name)
		fmt.Printf("dir:            %s\n", p.Dir)
		if pub, err := os.ReadFile(p.BoxPubKeyFile()); err == nil {
			fmt.Printf("box key:        %s", string(pub))
		}
		names := p.GHKeyNames()
		if len(names) == 0 {
			fmt.Println("github keys:    (none; add one with `megh profile gh add <name>`)")
		} else {
			fmt.Println("github keys:")
			for _, n := range names {
				fmt.Printf("  %s\n", n)
			}
		}
		fmt.Println("secrets (profile values overlaid on ambient env):")
		for _, k := range []string{"RUNPOD_API_KEY", "GH_MEGH_TOKEN", "TS_AUTHKEY"} {
			state := "MISSING"
			if os.Getenv(k) != "" {
				state = "set"
			}
			fmt.Printf("  %-22s %s\n", k, state)
		}
		return nil
	},
}

// --- gh subcommands ---

var profileGHCmd = &cobra.Command{
	Use:   "gh",
	Short: "Manage GitHub identity keys within a profile",
}

// ghRegister makes `profile gh add` upload the new pubkey to GitHub as well as
// minting it, which is the fiddliest step of setting up a new device.
var ghRegister bool

var profileGHAddCmd = &cobra.Command{
	Use:   "add <gh-name>",
	Short: "Generate a GitHub identity key (add its pubkey to that GitHub account)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, ok := profile.Get(profile.ActiveName(profileFlag))
		if !ok {
			return fmt.Errorf("no active profile (create one, or pass --profile)")
		}
		if err := p.AddGHKey(args[0]); err != nil {
			return err
		}
		pub, _ := os.ReadFile(p.GHPubKeyFile(args[0]))
		fmt.Printf("added GitHub identity %q to profile %q\n\n", args[0], p.Name)

		if ghRegister {
			if err := registerGHKey(p.GHPubKeyFile(args[0]), p.Name+"/"+args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "megh: %v\n\n", err)
			} else {
				fmt.Printf("registered it on the GitHub account `gh` is logged in as.\n")
				fmt.Printf("Reference it in megh.yaml repos:  - {url: ..., key: %s}\n", args[0])
				return nil
			}
		}
		fmt.Println("Add this public key to the corresponding GitHub account (once):")
		fmt.Printf("\n  %s\n", string(pub))
		fmt.Printf("  https://github.com/settings/ssh/new\n\n")
		fmt.Printf("Or let megh do it:  megh profile gh add %s --register\n", args[0])
		fmt.Printf("Then reference it in megh.yaml repos:  - {url: ..., key: %s}\n", args[0])
		return nil
	},
}

var profileGHListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List GitHub identities in the active profile (with pubkeys)",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, ok := profile.Get(profile.ActiveName(profileFlag))
		if !ok {
			return fmt.Errorf("no active profile")
		}
		names := p.GHKeyNames()
		if len(names) == 0 {
			fmt.Println("no github keys (add one: megh profile gh add <name>)")
			return nil
		}
		for _, n := range names {
			pub, _ := os.ReadFile(p.GHPubKeyFile(n))
			fmt.Printf("%s:\n  %s", n, string(pub))
		}
		return nil
	},
}

func init() {
	profileGHAddCmd.Flags().BoolVar(&ghRegister, "register", false,
		"also upload the pubkey to the GitHub account `gh` is logged in as")
	profileGHCmd.AddCommand(profileGHAddCmd, profileGHListCmd)
	profileCmd.AddCommand(profileCreateCmd, profileUseCmd, profileListCmd, profileShowCmd, profileGHCmd)
	rootCmd.AddCommand(profileCmd)
}

// registerGHKey uploads a public key to the GitHub account `gh` is logged in as.
//
// It shells out to `gh` rather than calling the API, deliberately. Adding a key
// needs the admin:public_key scope, and megh's own GH_MEGH_TOKEN is scoped to
// read:packages for image pulls; giving megh a token that can add SSH keys to
// your account, purely for a once-per-device step, is a worse trade than using
// the tool that already owns GitHub auth.
//
// This exists for the phone. Keys are never copied between machines, so a new
// device mints its own and has to enrol it, and pasting a base64 blob into a
// mobile browser is the most annoying step in the whole bootstrap.
func registerGHKey(pubFile, title string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("--register needs the gh CLI on PATH")
	}
	c := exec.Command("gh", "ssh-key", "add", pubFile, "--title", "megh "+title)
	out, err := c.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if isGHScopeError(msg) {
		return fmt.Errorf("gh lacks the admin:public_key scope; grant it with\n"+
			"  gh auth refresh -h github.com -s admin:public_key\nthen retry (%s)", msg)
	}
	return fmt.Errorf("gh ssh-key add failed: %s", msg)
}

// isGHScopeError spots the one failure worth explaining. A gh login has no
// admin:public_key scope by default, so this is what almost everyone hits first,
// and gh reports it as a bare "HTTP 403: Resource not accessible" that says
// nothing about which scope is missing or how to add it.
func isGHScopeError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "403") ||
		strings.Contains(m, "scope") ||
		strings.Contains(m, "not accessible")
}
