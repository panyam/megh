package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/panyam/megh/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	configPullRepo string
	configPullPath string
)

// ghRawFile fetches one file's raw bytes from a GitHub repo with the gh CLI,
// so a private repo works with whatever login gh has. A var for tests.
var ghRawFile = func(repo, path string) ([]byte, error) {
	out, err := exec.Command("gh", "api", "-H", "Accept: application/vnd.github.raw",
		"repos/"+repo+"/contents/"+path).Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("gh api %s/%s: %s (is gh logged in? 'gh auth login -h github.com -p https -w')", repo, path, msg)
	}
	return out, nil
}

var configPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Fetch your private megh.yaml from its repo into ~/.config/megh/megh.yaml",
	Long: `Fetch megh.yaml from the private config repo with gh and write it to
~/.config/megh/megh.yaml, the same source install.sh uses (MEGH_CONFIG_REPO,
default panyam/dotfiles, and MEGH_CONFIG_PATH, default megh/megh.yaml).

It is for a box nothing has touched yet: one launched from meghplane and entered
through Tailscale's console or webterm, where no control machine ever copied the
file in. On a box ~/.config/megh/megh.yaml is a symlink onto the volume, so the
write lands there and survives rebuilds. The content must parse as YAML before
anything is overwritten.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		repo := cmp.Or(configPullRepo, os.Getenv("MEGH_CONFIG_REPO"), "panyam/dotfiles")
		path := cmp.Or(configPullPath, os.Getenv("MEGH_CONFIG_PATH"), "megh/megh.yaml")
		data, err := ghRawFile(repo, path)
		if err != nil {
			return err
		}
		var probe config.Config
		if err := yaml.Unmarshal(data, &probe); err != nil {
			return fmt.Errorf("%s/%s is not valid megh.yaml: %w", repo, path, err)
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		wrote, err := writeThrough(filepath.Join(home, ".config", "megh", "megh.yaml"), data)
		if err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes) from %s/%s\n", wrote, len(data), repo, path)
		return nil
	},
}

// writeThrough writes data to path, or to the file path links to when it is a
// symlink, dangling or not, creating the parent directory. It returns the file
// actually written. A box's ~/.config/megh/megh.yaml is a symlink onto the
// volume whose target may not exist yet, and replacing the link with a regular
// file would strand the config on the container disk.
func writeThrough(path string, data []byte) (string, error) {
	target := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(path), link)
		}
		target = link
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return "", err
	}
	return target, nil
}

func init() {
	configPullCmd.Flags().StringVar(&configPullRepo, "repo", "", "config repo (default $MEGH_CONFIG_REPO, else panyam/dotfiles)")
	configPullCmd.Flags().StringVar(&configPullPath, "path", "", "file in the repo (default $MEGH_CONFIG_PATH, else megh/megh.yaml)")
	configCmd.AddCommand(configPullCmd)
}
