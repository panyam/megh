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

// Several features generate an on-box control script with a heredoc. An
// UNQUOTED heredoc (<<CTRL) expands ${VARS}, which is the point, but it also
// runs command substitution, which is not: a backticked word in a doc comment
// gets EXECUTED at generation time and replaced by its output. That silently
// deletes text from the generated file, and would run a real command if the
// word happened to be one. Valid bash, so `bash -n` cannot see it.
//
// Quote the delimiter (<<'CTRL') for anything that is not a variable to expand.
func TestNoCommandSubstitutionInUnquotedHeredocs(t *testing.T) {
	openHeredoc := regexp.MustCompile(`<<-?\s*('([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|([A-Za-z_][A-Za-z0-9_]*))`)
	substitution := regexp.MustCompile("`|\\$\\(")
	for _, name := range List() {
		t.Run(name, func(t *testing.T) {
			script, err := Script(name)
			if err != nil {
				t.Fatalf("Script(%q): %v", name, err)
			}
			lines := strings.Split(string(script), "\n")
			for i := 0; i < len(lines); i++ {
				m := openHeredoc.FindStringSubmatch(lines[i])
				if m == nil {
					continue
				}
				quoted, delim := m[2] != "" || m[3] != "", m[4]
				if quoted {
					// Skip to its terminator so a quoted body is not scanned.
					for i++; i < len(lines) && strings.TrimSpace(lines[i]) != m[2]+m[3]; i++ {
					}
					continue
				}
				for i++; i < len(lines) && strings.TrimSpace(lines[i]) != delim; i++ {
					if substitution.MatchString(lines[i]) {
						t.Errorf("%s.sh:%d is inside an unquoted heredoc (<<%s) and contains "+
							"command substitution, which executes when the file is written:\n\t%s",
							name, i+1, delim, strings.TrimSpace(lines[i]))
					}
				}
			}
		})
	}
}

// The RunPod volume is nfs4 with root squashed: chown is denied for every uid,
// and a 0777 directory does not help because initdb also chmods to 0700. Since
// postgres requires ownership of its data directory, pointing the default at the
// volume produces a cluster that cannot be created at all. Measured on a live
// box; this test exists so the default cannot drift back to the volume, which
// reads like an improvement and is not.
func TestPostgresDataDefaultsOffTheVolume(t *testing.T) {
	script, err := Script("postgres")
	if err != nil {
		t.Fatalf("Script(postgres): %v", err)
	}
	def := regexp.MustCompile(`PGROOT="\$\{MEGH_PG_DATA:-([^}"]+)\}"`).FindSubmatch(script)
	if def == nil {
		t.Fatal("could not find the MEGH_PG_DATA default in postgres.sh")
	}
	if got := string(def[1]); strings.HasPrefix(got, "/mnt/work") || strings.HasPrefix(got, "/workspace") {
		t.Errorf("postgres data defaults to %q, which is the NFS volume; postgres cannot own a directory there", got)
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
