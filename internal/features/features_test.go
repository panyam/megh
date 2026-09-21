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

// A launcher written into a QUOTED heredoc is invisible to TestFeatureScriptsParse:
// `bash -n` on the outer script treats the body as opaque text, so a syntax error
// in it ships and fails on the box, at the moment someone wants the viewer. Parse
// the generated file too.
func TestGeneratedLaunchersParse(t *testing.T) {
	for name, delim := range map[string]string{"playwright": "PWUI"} {
		t.Run(name, func(t *testing.T) {
			script, err := Script(name)
			if err != nil {
				t.Fatalf("Script(%q): %v", name, err)
			}
			body := heredocBody(string(script), delim)
			if body == "" {
				t.Fatalf("%s.sh has no <<'%s' heredoc; did the launcher move?", name, delim)
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(body)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("the %s launcher is not valid bash: %v\n%s", name, err, out)
			}
		})
	}
}

// C4: every surface binds the box's loopback and is reached through a tunnel or
// `tailscale serve`. The viewer is the first one started by hand rather than by
// a service line, so its bind address is a variable in a generated script, where
// nothing else would notice it being widened to 0.0.0.0.
func TestPlaywrightViewerBindsLoopback(t *testing.T) {
	script, err := Script("playwright")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	body := heredocBody(string(script), "PWUI")
	if !strings.Contains(body, "host=127.0.0.1") {
		t.Errorf("the viewer must bind 127.0.0.1 (C4); no such bind in:\n%s", body)
	}
	if strings.Contains(body, "0.0.0.0") {
		t.Errorf("the viewer binds 0.0.0.0, which publishes it on every interface (C4):\n%s", body)
	}
}

// heredocBody returns the lines between `<<'delim'` and its terminator.
func heredocBody(script, delim string) string {
	lines := strings.Split(script, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "<<'"+delim+"'") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == delim {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return ""
}

// Emit-only exists so the IMAGE can bake the launcher without installing a
// browser stack at build time. Its guard has to sit AFTER the launcher is
// written and BEFORE the first install, and both halves of that are silent when
// wrong: above the write it bakes nothing, below the npm line it turns a 2 KB
// artifact into a several-hundred-megabyte image layer.
func TestPlaywrightEmitOnlyStopsBeforeInstalling(t *testing.T) {
	script, err := Script("playwright")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	s := string(script)
	write := strings.Index(s, "chmod 0755 /usr/local/bin/pw-ui")
	guard := strings.Index(s, `if [ "${MEGH_PLAYWRIGHT_EMIT_ONLY:-0}" = "1" ]`)
	install := strings.Index(s, "npm install -g playwright")
	switch {
	case write < 0:
		t.Fatal("the launcher is no longer written by playwright.sh")
	case guard < 0:
		t.Fatal("playwright.sh has no MEGH_PLAYWRIGHT_EMIT_ONLY guard; the image bakes nothing")
	case install < 0:
		t.Fatal("playwright.sh no longer installs playwright")
	case guard < write:
		t.Error("the emit-only guard runs BEFORE the launcher is written, so an emit-only build bakes nothing")
	case install < guard:
		t.Error("the emit-only guard runs AFTER the install, so an emit-only build pulls chromium")
	}
}

// Every feature's script states what it does in its header, and the chooser
// reads that rather than a second hand-written list. A new feature whose header
// does not follow the convention shows up in the chooser as a bare name, which
// is exactly the case this catches.
func TestEveryFeatureDescribesItself(t *testing.T) {
	for _, name := range List() {
		desc := Describe(name)
		switch {
		case desc == "":
			t.Errorf("%s.sh has no `# Feature: %s — <summary>` header", name, name)
		case strings.Contains(desc, "\n"):
			t.Errorf("%s: summary spans lines, so a chooser row cannot hold it: %q", name, desc)
		}
		t.Logf("%-11s %s", name, desc)
	}
}
