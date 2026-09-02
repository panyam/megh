package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

// megh portal builds a bookmarkable "box + URLs" index and force-pushes it to a
// branch of a private repo. GitHub renders a Markdown file in a private repo with
// clickable links for the signed-in owner, so on a phone you bookmark one URL and
// tap through to a box's web surfaces instead of typing https://<box>:<port>.
// `megh up`/`down` refresh it (best-effort) so the list stays current.

var portalCmd = &cobra.Command{
	Use:   "portal",
	Short: "Publish a bookmarkable box+URL index (PORTAL.md) to your private repo",
	Long: `Generate PORTAL.md — every live box with clickable links to its web surfaces —
and force-push it to portal.branch of portal.repo (set in megh.yaml). Bookmark the
rendered file on that branch (GitHub renders Markdown in a private repo for you),
so on mobile you tap a link instead of typing https://<box>:<port>.

Surface URLs use <box>.<tailnet> (set 'tailnet:' in megh.yaml) and portal.scheme
(http, or https once you enable HTTPS certs on your tailnet). megh up/down refresh
it automatically when portal.repo is set.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cfg.Portal.Repo == "" {
			return fmt.Errorf("set portal.repo in megh.yaml (a private repo to push PORTAL.md to)")
		}
		ctx := context.Background()
		pods, err := runpod.List(ctx)
		if err != nil {
			return err
		}
		if err := pushPortal(renderPortal(runpod.ManagedPods(pods))); err != nil {
			return err
		}
		fmt.Printf("portal published. Bookmark: %s\n", portalBookmarkURL())
		return nil
	},
}

// renderPortal builds the PORTAL.md markdown for the given boxes.
func renderPortal(pods []runpod.Pod) string {
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
		b.WriteString("\n")
	}
	return b.String()
}

// pushPortal force-pushes PORTAL.md to portal.branch of portal.repo via a throwaway
// git repo (no persistent scratch state, no touching the user's working tree).
func pushPortal(md string) error {
	// Both the explicit `megh portal` and the up/down refresh come through here,
	// so the check belongs at this choke point rather than in either caller.
	if portalRepoIsPublic(cfg.Portal.Repo) {
		return fmt.Errorf("refusing to publish: %s is PUBLIC.\n"+
			"PORTAL.md lists every box by name with its tailnet URLs and is rewritten on\n"+
			"every up and down, so publishing it there is a standing disclosure that\n"+
			"re-creates itself. Point portal.repo at a private repo.", repoSlug(cfg.Portal.Repo))
	}
	repo := cfg.Portal.Repo
	if repo == "" {
		return fmt.Errorf("portal.repo not set")
	}
	branch := cfg.Portal.Branch
	if branch == "" {
		branch = "portal"
	}
	tmp, err := os.MkdirTemp("", "megh-portal-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "PORTAL.md"), []byte(md), 0o644); err != nil {
		return err
	}
	git := func(args ...string) error {
		c := exec.Command("git", args...)
		c.Dir = tmp
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	steps := [][]string{
		{"init", "-q"},
		{"checkout", "-q", "-b", branch},
		{"add", "PORTAL.md"},
		{"-c", "user.email=megh@localhost", "-c", "user.name=megh", "commit", "-q", "-m", "portal " + time.Now().UTC().Format(time.RFC3339)},
		{"remote", "add", "origin", repo},
		{"push", "-q", "--force", "origin", branch},
	}
	for _, s := range steps {
		if err := git(s...); err != nil {
			return err
		}
	}
	return nil
}

// portalBookmarkURL is a best-effort github.com URL to the rendered PORTAL.md, from
// the configured repo (git@host:owner/name.git -> https://github.com/owner/name/...).
func portalBookmarkURL() string {
	branch := cfg.Portal.Branch
	if branch == "" {
		branch = "portal"
	}
	m := regexp.MustCompile(`[:/]([^/:]+)/([^/]+?)(?:\.git)?$`).FindStringSubmatch(cfg.Portal.Repo)
	if len(m) == 3 {
		return fmt.Sprintf("https://github.com/%s/%s/blob/%s/PORTAL.md", m[1], m[2], branch)
	}
	return fmt.Sprintf("%s (branch %s, PORTAL.md)", cfg.Portal.Repo, branch)
}

// publishPortalBestEffort refreshes the portal after up/down. It never fails the
// caller; it just notes a problem on stderr. No-op unless portal.repo is set.
// portalRepoIsPublic reports whether the portal target is a public GitHub repo.
//
// PORTAL.md lists every box by name with its full tailnet URLs, and it is
// rewritten on every up and down. Pushing that to a public repo is a standing
// disclosure that re-creates itself, which is exactly what happened when the
// megh repo went public while portal.repo still named a branch of it: the file
// was world-readable within seconds and nothing said so.
//
// Failure to determine visibility returns false. This guard exists to catch an
// obvious mistake, not to be the thing standing between you and a leak, so it
// must not block a legitimate push just because `gh` is missing or offline.
func portalRepoIsPublic(repo string) bool {
	slug := repoSlug(repo)
	if slug == "" {
		return false
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	out, err := exec.Command("gh", "repo", "view", slug, "--json", "visibility", "-q", ".visibility").Output()
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "public")
}

// repoSlug pulls owner/name out of the git URL forms megh.yaml uses.
func repoSlug(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.LastIndex(u, ":"); i >= 0 {
		u = u[i+1:]
	} else if i := strings.Index(u, "github.com/"); i >= 0 {
		u = u[i+len("github.com/"):]
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func publishPortalBestEffort() {
	if cfg.Portal.Repo == "" {
		return
	}
	pods, err := runpod.List(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "megh: portal refresh skipped: %v\n", err)
		return
	}
	if err := pushPortal(renderPortal(runpod.ManagedPods(pods))); err != nil {
		fmt.Fprintf(os.Stderr, "megh: portal refresh failed: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "megh: portal refreshed (%s)\n", portalBookmarkURL())
}

func init() {
	rootCmd.AddCommand(portalCmd)
}
