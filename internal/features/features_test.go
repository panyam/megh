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

// Postgres CAN run from the NFS volume (a directory created by the postgres user
// is owned by it; only root handing one over is denied). The default stays on
// local disk for two measured reasons, and moving it back would look like an
// improvement while being neither:
//
//   - 3.4x slower: 1308 tps / 3.06 ms on the volume vs 4452 / 0.90 local.
//   - Postgres does NOT protect a shared cluster. A second box on the same volume
//     warns "another server might be running" and starts anyway, because its
//     liveness check is a local PID lookup. Two boxes then diverged on one data
//     directory in testing.
//
// Dev boxes do not need a database to outlive them; share data with fixtures.
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
		t.Errorf("postgres data defaults to %q, the shared NFS volume: 3.4x slower, and a "+
			"second box will open the same cluster and corrupt it", got)
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

// KiCad's symbol and footprint libraries arrive through Recommends, not
// Depends. `apt-get install --no-install-recommends kicad` therefore succeeds
// and gives you a KiCad that opens, shows a schematic editor, and cannot place
// a single part — a failure that looks like a broken app rather than a missing
// package. eda.sh installs its X support packages with --no-install-recommends
// (leaf libraries, and mesa's recommends pull in a lot) and the apps WITHOUT
// it, deliberately. Merging the two calls would be the obvious tidy-up.
func TestEDAInstallsAppsWithRecommends(t *testing.T) {
	script, err := Script("eda")
	if err != nil {
		t.Fatalf("Script(eda): %v", err)
	}
	var checked int
	for i, line := range strings.Split(string(script), "\n") {
		code, _, _ := strings.Cut(line, "#")
		if !strings.Contains(code, "apt-get install") {
			continue
		}
		// The two calls that install the app list: the group one and the
		// per-package retry.
		if !strings.Contains(code, "${wanted}") && !strings.Contains(code, `"${p}"`) {
			continue
		}
		checked++
		if strings.Contains(code, "--no-install-recommends") {
			t.Errorf("eda.sh:%d installs the apps with --no-install-recommends, which "+
				"leaves kicad without its symbol/footprint libraries:\n\t%s",
				i+1, strings.TrimSpace(line))
		}
	}
	if checked != 2 {
		t.Errorf("expected to check 2 app-install calls in eda.sh, found %d", checked)
	}
}
