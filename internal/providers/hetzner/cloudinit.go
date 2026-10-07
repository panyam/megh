package hetzner

import (
	"fmt"
	"sort"
	"strings"
)

// boxSSHPort is where the box's own sshd is published on the VM. The VM's
// sshd is switched off: the VM is only a Docker host, and the box is the
// machine you log into.
const boxSSHPort = 2222

// bootSpec is everything the first-boot script needs.
type bootSpec struct {
	Image     string
	VolumeID  int64
	Env       map[string]string // the box's env, same keys a RunPod pod gets
	PullUser  string            // registry login; empty PullToken skips it
	PullToken string
	ExposeSSH bool
}

// shq single-quotes s for a POSIX shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cloudInit renders the user_data script that turns a fresh Ubuntu VM into a
// megh box host: Docker, the volume, then the megh image as one container
// with the same env and /workspace mount a RunPod pod gets, so the entrypoint
// runs unchanged.
//
// Three choices worth knowing. The values travel in a 0600 file sourced into
// the shell and passed by name (`-e NAME`), never on a command line, because a
// public key list spans lines and `ps` shows argv. The box is cut off from the
// metadata service (169.254.169.254), where this script, and so the pull
// token, can be read; a systemd unit reapplies the rule after a reboot, since
// Docker recreates its chains empty. And the volume is formatted by Hetzner at
// creation and automounted, so there is no "format if blank" step here that
// could ever wipe a volume.
func cloudInit(s bootSpec) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -euo pipefail\nexec >>/var/log/megh-boot.log 2>&1\n")
	b.WriteString("systemctl disable --now ssh.socket ssh.service 2>/dev/null || true\n")
	b.WriteString("export DEBIAN_FRONTEND=noninteractive\napt-get update -y\napt-get install -y docker.io\nsystemctl enable --now docker\n")

	b.WriteString(`cat >/etc/systemd/system/megh-no-metadata.service <<'UNIT'
[Unit]
Description=Keep megh boxes away from the cloud metadata service
After=docker.service
Requires=docker.service
[Service]
Type=oneshot
ExecStart=/bin/sh -c 'iptables -C DOCKER-USER -d 169.254.169.254 -j DROP 2>/dev/null || iptables -I DOCKER-USER -d 169.254.169.254 -j DROP'
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now megh-no-metadata.service
`)

	vol := fmt.Sprintf("/mnt/HC_Volume_%d", s.VolumeID)
	fmt.Fprintf(&b, "vol=%s\nfor _ in $(seq 1 90); do mountpoint -q \"$vol\" && break; sleep 2; done\n", vol)
	b.WriteString("mountpoint -q \"$vol\" || { echo \"megh: volume $vol never mounted\"; exit 1; }\n")

	names := make([]string, 0, len(s.Env))
	for k := range s.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("umask 077\ncat >/root/megh.env <<'ENV'\n")
	for _, k := range names {
		fmt.Fprintf(&b, "%s=%s\n", k, shq(s.Env[k]))
	}
	if s.PullToken != "" {
		fmt.Fprintf(&b, "MEGH_PULL_TOKEN=%s\n", shq(s.PullToken))
	}
	b.WriteString("ENV\nset -a\n. /root/megh.env\nset +a\nrm -f /root/megh.env\n")

	if s.PullToken != "" {
		registry := strings.SplitN(s.Image, "/", 2)[0]
		fmt.Fprintf(&b, "printf '%%s' \"$MEGH_PULL_TOKEN\" | docker login %s -u %s --password-stdin\nunset MEGH_PULL_TOKEN\n",
			shq(registry), shq(s.PullUser))
	}

	b.WriteString("docker run -d --name megh-box --restart unless-stopped")
	if s.ExposeSSH {
		fmt.Fprintf(&b, " -p %d:22", boxSSHPort)
	}
	b.WriteString(` -v "$vol":/workspace`)
	for _, k := range names {
		fmt.Fprintf(&b, " -e %s", k)
	}
	fmt.Fprintf(&b, " %s\n", shq(s.Image))
	if s.PullToken != "" {
		b.WriteString("docker logout " + shq(strings.SplitN(s.Image, "/", 2)[0]) + " || true\n")
	}
	return b.String()
}
