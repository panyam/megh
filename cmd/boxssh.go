package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/panyam/megh/internal/profile"
)

// repoDir derives the working-copy directory from a git URL.
// git@github.com:panyam/megh.git -> megh
func repoDir(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

// aliasedURL rewrites a github.com git URL to use the per-identity host alias
// (gh-<key>) that ghSetupScript configures on the box, so the clone signs with
// that identity's forwarded key. Empty key or a non-github URL is left as-is.
func aliasedURL(url, ghKey string) string {
	if ghKey == "" {
		return url
	}
	if rest, ok := strings.CutPrefix(url, "git@github.com:"); ok {
		return "git@gh-" + ghKey + ":" + rest
	}
	if rest, ok := strings.CutPrefix(url, "ssh://git@github.com/"); ok {
		return "ssh://git@gh-" + ghKey + "/" + rest
	}
	return url
}

// ghSetupScript writes the profile's GitHub public keys and a megh-managed
// ~/.ssh/config section on the box, one Host alias (gh-<name>) per identity that
// selects that key via the forwarded agent (private keys never touch the box).
// Returns "" when the profile has no GitHub keys.
func ghSetupScript(p *profile.Profile) (string, error) {
	names := p.GHKeyNames()
	if len(names) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("set -e\nmkdir -p ~/.ssh && chmod 700 ~/.ssh\ntouch ~/.ssh/known_hosts\n")
	b.WriteString("ssh-keyscan -t ed25519,rsa github.com >> ~/.ssh/known_hosts 2>/dev/null || true\n")
	b.WriteString("sort -u ~/.ssh/known_hosts -o ~/.ssh/known_hosts\n")
	for _, n := range names {
		pub, err := os.ReadFile(p.GHPubKeyFile(n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "cat > ~/.ssh/gh-%s.pub <<'MEGHPUB'\n%sMEGHPUB\nchmod 644 ~/.ssh/gh-%s.pub\n", n, string(pub), n)
	}
	// Regenerate the megh-managed config section (idempotent).
	b.WriteString("touch ~/.ssh/config\n")
	b.WriteString("sed -i.bak '/# >>> megh gh >>>/,/# <<< megh gh <<</d' ~/.ssh/config 2>/dev/null || true\nrm -f ~/.ssh/config.bak\n")
	b.WriteString("cat >> ~/.ssh/config <<'MEGHCFG'\n# >>> megh gh >>>\n")
	for _, n := range names {
		fmt.Fprintf(&b, "Host gh-%s\n  HostName github.com\n  User git\n  IdentityFile ~/.ssh/gh-%s.pub\n  IdentitiesOnly yes\n  StrictHostKeyChecking accept-new\n", n, n)
	}
	b.WriteString("# <<< megh gh <<<\nMEGHCFG\nchmod 600 ~/.ssh/config\n")
	return b.String(), nil
}
