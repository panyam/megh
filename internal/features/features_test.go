package features

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Feature scripts are piped straight to a `bash -s` on a live box, so a syntax
// error is not caught by `go build` or by anything else until it has already
// been shipped and run. Parse every one at build time instead.
func TestFeatureScriptsParse(t *testing.T) {
	for _, name := range List() {
		t.Run(name, func(t *testing.T) {
			script, err := Script(name)
			if err != nil {
				t.Fatalf("Script(%q): %v", name, err)
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(string(script))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s.sh is not valid bash: %v\n%s", name, err, out)
			}
		})
	}
}

// RunPod's public proxy is open and unauthenticated, so a feature that binds a
// wildcard address puts a dev service (a root shell, a database) on the public
// internet. Every surface must bind loopback and be reached over Tailscale or an
// SSH tunnel. Encoded here so it is enforced rather than remembered.
//
// See CONSTRAINTS.md C4.
func TestFeatureScriptsBindLoopback(t *testing.T) {
	// A wildcard bind address in any form: 0.0.0.0, ::, or a bare *.
	wildcard := regexp.MustCompile(`(^|[^0-9.])0\.0\.0\.0|\[::\]|bind[_-]?addr(ess)?\s*[:=]\s*['"]?\*`)
	for _, name := range List() {
		t.Run(name, func(t *testing.T) {
			script, err := Script(name)
			if err != nil {
				t.Fatalf("Script(%q): %v", name, err)
			}
			for i, line := range strings.Split(string(script), "\n") {
				code, _, _ := strings.Cut(line, "#") // ignore prose in comments
				if wildcard.MatchString(code) {
					t.Errorf("%s.sh:%d binds a wildcard address, must be 127.0.0.1:\n\t%s",
						name, i+1, strings.TrimSpace(line))
				}
			}
		})
	}
}
