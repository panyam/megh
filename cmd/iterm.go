package cmd

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/panyam/megh/internal/iterm"
	"github.com/spf13/cobra"
)

var itermCmd = &cobra.Command{
	Use:   "iterm",
	Short: "Save, load, and list iTerm2 profiles used by megh ssh",
	Long: `Profiles are stored as JSON next to megh.yaml (iterm.dir, default
iterm/profiles). Save pulls from iTerm Settings; load pushes into iTerm's
DynamicProfiles folder so iTerm picks them up immediately.

  megh iterm save megh     # iTerm UI → megh store (git-friendly)
  megh iterm load          # megh store → iTerm
  megh iterm load megh     # one profile
  megh iterm unload megh   # drop DynamicProfiles export (git store unchanged)
  megh iterm install       # seed default template if missing, then load default

Interactive ssh uses megh.yaml iterm.profile, or --iterm-profile on megh ssh.`,
}

func itermStoreDir() string {
	return cfg.ITermProfilesDir(cfgSourcePath)
}

var itermLoadCmd = &cobra.Command{
	Use:   "load [profile-name...]",
	Short: "Push saved profile(s) from megh into iTerm2",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("iTerm2 integration is macOS only")
		}
		dir := itermStoreDir()
		loaded, err := iterm.Load(dir, args...)
		if err != nil {
			return err
		}
		sort.Strings(loaded)
		fmt.Printf("loaded into iTerm2: %s\n", strings.Join(loaded, ", "))
		return nil
	},
}

var itermSaveCmd = &cobra.Command{
	Use:   "save <profile-name>",
	Short: "Copy a profile from iTerm2 into the megh store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("iTerm2 integration is macOS only")
		}
		path, err := iterm.Save(itermStoreDir(), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("saved iTerm profile %q to %s\n", args[0], path)
		fmt.Println("commit that file in dotfiles; run `megh iterm load` on your other Macs")
		return nil
	},
}

var itermListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List profiles in the megh store and iTerm DynamicProfiles export state",
	Long: `The store (iterm.dir) is git-backed JSON; it stays listed until you remove
those files from dotfiles. "DynamicProfiles export" is the file megh copies into
~/Library/Application Support/iTerm2/DynamicProfiles/. Deleting a profile in iTerm
Settings often leaves that file on disk, so megh also reports whether the name
still appears in iTerm's profile list.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := itermStoreDir()
		fmt.Printf("store: %s\n", dir)
		if runtime.GOOS != "darwin" {
			fmt.Println("iTerm2: unavailable (not macOS)")
			return nil
		}
		list, err := iterm.ListStore(dir)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("no saved profiles (try `megh iterm save megh` or `megh iterm install`)")
			return nil
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		for _, s := range list {
			fmt.Printf("  %s\t%s\n", s.Name, itermListState(s.Name))
		}
		return nil
	},
}

func itermListState(name string) string {
	export := iterm.ProfileLoaded(name)
	inPrefs := iterm.ProfileInITerm(name)
	switch {
	case export && inPrefs:
		return "DynamicProfiles export · in iTerm Settings"
	case export && !inPrefs:
		return "DynamicProfiles export only (removed in Settings; `megh iterm unload " + name + "` to drop file)"
	case !export && inPrefs:
		return "in iTerm Settings only (not exported by megh; `megh iterm load " + name + "` to sync store)"
	default:
		return "store only (`megh iterm load " + name + "`)"
	}
}

var itermUnloadCmd = &cobra.Command{
	Use:   "unload [profile-name...]",
	Short: "Remove megh DynamicProfiles export file(s); the git store is unchanged",
	Long: `Deletes megh's JSON under iTerm's DynamicProfiles folder. Use this after
removing a profile in iTerm Settings, or to stop megh from re-publishing a profile.
The files under iterm.dir in dotfiles are not touched.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("iTerm2 integration is macOS only")
		}
		removed, err := iterm.Unload(args...)
		if err != nil {
			return err
		}
		if len(removed) == 0 {
			fmt.Println("no DynamicProfiles exports removed")
			return nil
		}
		sort.Strings(removed)
		fmt.Printf("removed DynamicProfiles export: %s\n", strings.Join(removed, ", "))
		return nil
	},
}

var itermInstallCmd = &cobra.Command{
	Use:     "install",
	Short:   "Seed the default profile file if missing, then load it into iTerm2",
	Aliases: []string{"init"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("iTerm2 integration is macOS only")
		}
		set := itermSettings("")
		if err := iterm.Install(set); err != nil {
			return err
		}
		fmt.Printf("installed iTerm profile %q from %s\n", set.Profile, itermStoreDir())
		return nil
	},
}

var itermStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report iTerm integration state",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := itermStoreDir()
		fmt.Printf("store:           %s\n", dir)
		fmt.Printf("default profile: %q (auto=%v)\n", cfg.ITermProfile(), cfg.ITermAuto())
		if runtime.GOOS != "darwin" {
			fmt.Println("iTerm2:          unavailable (not macOS)")
			return nil
		}
		fmt.Printf("iTerm2:          available=%v\n", iterm.Available())
		if os.Getenv("ITERM_SESSION_ID") != "" {
			fmt.Printf("this session:    ITERM_PROFILE=%q\n", os.Getenv("ITERM_PROFILE"))
		}
		name := cfg.ITermProfile()
		fmt.Printf("default export:  %v (DynamicProfiles file)\n", iterm.ProfileLoaded(name))
		fmt.Printf("in Settings:     %v\n", iterm.ProfileInITerm(name))
		return nil
	},
}

func init() {
	itermCmd.AddCommand(itermLoadCmd, itermSaveCmd, itermUnloadCmd, itermListCmd, itermInstallCmd, itermStatusCmd)
	rootCmd.AddCommand(itermCmd)
}
