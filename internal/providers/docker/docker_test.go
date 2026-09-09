package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers"
)

func argvOf(t *testing.T, set settings, o providers.Options) []string {
	t.Helper()
	args, err := runArgs(set, providers.PrefixName(o.Name), "megh-local:arm64", "/host/work", o)
	if err != nil {
		t.Fatalf("runArgs: %v", err)
	}
	return args
}

// joined renders the argv for substring assertions, with a separator that
// cannot appear inside an argument, so a match cannot span two of them.
func joined(args []string) string { return "\x00" + strings.Join(args, "\x00") + "\x00" }

func hasPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func TestRunArgsCarriesTheBoxContract(t *testing.T) {
	args := argvOf(t, settings{}, providers.Options{Name: "local1", PubKey: "ssh-ed25519 AAAA"})

	if !hasPair(args, "--name", "megh-local1") {
		t.Errorf("container is not stored under the megh- marker: %v", args)
	}
	if !hasPair(args, "--hostname", "local1") {
		t.Errorf("hostname must be the BARE name (C1), got: %v", args)
	}
	if !hasPair(args, "--label", "megh.managed=1") {
		t.Error("no megh.managed label; List filters on it")
	}
	if !hasPair(args, "-v", "/host/work:/workspace") {
		t.Error("the work dir is not bound at the work mount")
	}
	for _, want := range []string{"WORK_MOUNT=/workspace", "PUBLIC_KEY=ssh-ed25519 AAAA"} {
		if !hasPair(args, "-e", want) {
			t.Errorf("missing box env %q", want)
		}
	}
	if args[len(args)-1] != "megh-local:arm64" {
		t.Errorf("image must be the last argument, got %q", args[len(args)-1])
	}
}

// CONSTRAINTS C4: nothing a box runs may be reachable off this machine, and on a
// local box that means only SSH is published and only on loopback.
//
// Publishing the web surfaces was tried and reverted. It cannot work: C4 makes
// every surface bind the box's own 127.0.0.1, and a docker publish forwards to
// the container's eth0, so the host port accepts a connection and has nothing to
// hand it to. sshd is the one service that binds 0.0.0.0, which is why 22 alone
// is publishable. Reaching a surface is an SSH tunnel here as on a cloud box.
func TestRunArgsPublishesOnlyLoopbackSSH(t *testing.T) {
	args := argvOf(t, settings{}, providers.Options{Name: "local1"})
	published := 0
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-p" {
			continue
		}
		published++
		if args[i+1] != "127.0.0.1::22" {
			t.Errorf("published %q; a box may publish only loopback 22", args[i+1])
		}
	}
	if published != 1 {
		t.Errorf("published %d ports, want exactly 1", published)
	}
}

func TestParseMounts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got, err := ParseMounts(map[string]string{
		"~/projects":    "repos/projects",
		"/abs/thing":    "/root/thing",
		"/abs/readonly": "/root/ro:ro",
		"/abs/explicit": "/root/rw:rw",
	}, "/workspace")
	if err != nil {
		t.Fatalf("ParseMounts: %v", err)
	}
	by := map[string]Mount{}
	for _, m := range got {
		by[m.Host] = m
	}
	if m := by[filepath.Join(home, "projects")]; m.Box != "/workspace/repos/projects" || m.ReadOnly {
		t.Errorf("~ and relative target wrong: %+v", m)
	}
	if m := by["/abs/readonly"]; m.Box != "/root/ro" || !m.ReadOnly {
		t.Errorf(":ro not honoured: %+v", m)
	}
	if m := by["/abs/explicit"]; m.Box != "/root/rw" || m.ReadOnly {
		t.Errorf(":rw not honoured: %+v", m)
	}
	if m := by["/abs/thing"]; m.Arg() != "/abs/thing:/root/thing" {
		t.Errorf("Arg() = %q", m.Arg())
	}
}

// Docker applies binds in the order given, so a nested mount must come after
// the one it sits inside or it is shadowed rather than shadowing.
func TestParseMountsSortsNestedAfterParent(t *testing.T) {
	got, err := ParseMounts(map[string]string{
		"/host/envvars":  "/root/personal/envvars",
		"/host/personal": "/root/personal",
	}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Box != "/root/personal" || got[1].Box != "/root/personal/envvars" {
		t.Errorf("nested mount not ordered after its parent: %+v", got)
	}
}

func TestParseMountsRejectsARelativeHostPath(t *testing.T) {
	if _, err := ParseMounts(map[string]string{"relative/path": "/root/x"}, "/workspace"); err == nil {
		t.Error("a relative host path should be rejected, not silently resolved against the cwd")
	}
}

func TestParseInspect(t *testing.T) {
	b, err := parseInspect(`[{
	  "Id": "abc123def4567890",
	  "Name": "/megh-local1",
	  "Config": {"Image": "megh-local:arm64", "Labels": {"megh.managed": "1"}},
	  "State": {"Status": "running"},
	  "NetworkSettings": {"Ports": {"22/tcp": [{"HostIp": "127.0.0.1", "HostPort": "54321"}]}}
	}]`)
	if err != nil {
		t.Fatalf("parseInspect: %v", err)
	}
	if b.Name != "megh-local1" || b.DisplayName() != "local1" {
		t.Errorf("name = %q, display = %q", b.Name, b.DisplayName())
	}
	if b.PublicIP != "127.0.0.1" || b.SSHPort != 54321 || !b.SSHReady() {
		t.Errorf("ssh endpoint = %s:%d, ready = %v", b.PublicIP, b.SSHPort, b.SSHReady())
	}
	if b.Status != "RUNNING" || b.DataCenter != "local" {
		t.Errorf("status = %q, dc = %q", b.Status, b.DataCenter)
	}
}

// A stopped box has no published port. That is a listable state, not an error:
// `megh list` should show it and `megh down` should still remove it.
func TestParseInspectHandlesAStoppedBox(t *testing.T) {
	b, err := parseInspect(`[{
	  "Id": "abc", "Name": "/megh-local1",
	  "Config": {"Image": "megh-local:arm64"},
	  "State": {"Status": "exited"},
	  "NetworkSettings": {"Ports": {}}
	}]`)
	if err != nil {
		t.Fatalf("parseInspect: %v", err)
	}
	if b.SSHReady() {
		t.Error("a stopped box must not report an SSH endpoint")
	}
	if b.Status != "EXITED" {
		t.Errorf("status = %q", b.Status)
	}
}
