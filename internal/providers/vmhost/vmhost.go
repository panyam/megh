// Package vmhost renders the first-boot script that turns a fresh Ubuntu VM
// into a megh box host: Docker, the box's volume, then the megh image as one
// container with the same env and /workspace mount a RunPod pod gets, so the
// entrypoint runs unchanged. The VM providers (Hetzner, Vultr) differ only in
// how their volume arrives, which Volume describes.
package vmhost

import (
	"fmt"
	"sort"
	"strings"
)

// BoxSSHPort is where the box's own sshd is published on the VM. The VM's
// sshd is switched off: the VM is only a Docker host, and the box is the
// machine you log into.
const BoxSSHPort = 2222

// Volume says how the box's volume reaches the VM. Exactly one field is set.
type Volume struct {
	// AutomountPath is where the provider has already formatted and mounted
	// the volume (Hetzner: /mnt/HC_Volume_<id>). The script only waits for it.
	AutomountPath string
	// BlockSizeGB is a raw block device of this size that the script must
	// find, format only if it is blank, and mount (Vultr). See PrepareVolume.
	BlockSizeGB int
}

// Spec is everything the first-boot script needs.
type Spec struct {
	Image     string
	Volume    Volume
	Env       map[string]string // the box's env, the keys a RunPod pod gets
	PullUser  string            // registry login; empty PullToken skips it
	PullToken string
	ExposeSSH bool
}

// blockMount is where a raw block volume is mounted on the VM.
const blockMount = "/mnt/megh-volume"

// Shq single-quotes s for a POSIX shell.
func Shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// PrepareVolume is the shell that finds, formats if blank, and mounts a raw
// block volume. It is the one step in megh that can destroy data, so it is
// strict and leaves a failed boot rather than a guess:
//
//   - The disk is the one whole disk whose size equals the volume's (as GiB or
//     as GB, whichever the provider reports), with no partitions and nothing
//     mounted from it. It is never chosen by name.
//   - `blkid -p` exit 0 means a filesystem exists: mount it, never format.
//     Exit 2 means definitely blank: format once. Any other exit stops the
//     boot without formatting.
//   - The mount goes into /etc/fstab (nofail), so after a reboot the volume
//     is mounted before Docker restarts the box onto it.
//
// MEGH_DISK_WAIT (default 150 tries, 2s apart) bounds the wait for the disk,
// which attaches after the VM exists.
const PrepareVolume = `megh_find_disk() {
  local want_gib=$(( $1 * 1073741824 )) want_gb=$(( $1 * 1000000000 ))
  lsblk -b -dn -o NAME,SIZE,TYPE | while read -r name size type; do
    [ "$type" = disk ] || continue
    [ "$size" = "$want_gib" ] || [ "$size" = "$want_gb" ] || continue
    dev=/dev/$name
    [ -z "$(lsblk -n -o MOUNTPOINT "$dev" | tr -d '[:space:]')" ] || continue
    [ "$(lsblk -n -o NAME "$dev" | wc -l)" -eq 1 ] || continue
    echo "$dev"
  done
}
megh_prepare_volume() {
  local gb=$1 mnt=$2 dev="" i rc=0
  for i in $(seq 1 "${MEGH_DISK_WAIT:-150}"); do
    dev=$(megh_find_disk "$gb" | head -n 1)
    [ -n "$dev" ] && break
    sleep 2
  done
  if [ -z "$dev" ]; then echo "megh: no unpartitioned, unmounted ${gb} GB disk appeared; not formatting anything"; return 1; fi
  blkid -p "$dev" >/dev/null 2>&1 || rc=$?
  case "$rc" in
    0) echo "megh: $dev has a filesystem; mounting it as is" ;;
    2) echo "megh: $dev is blank; formatting it once"; mkfs.ext4 -q -L megh-vol "$dev" ;;
    *) echo "megh: blkid -p $dev exited $rc; refusing to format"; return 1 ;;
  esac
  mkdir -p "$mnt"
  mount "$dev" "$mnt"
  local uuid
  uuid=$(blkid -s UUID -o value "$dev" 2>/dev/null || true)
  if [ -n "$uuid" ] && ! grep -q "$uuid" /etc/fstab 2>/dev/null; then
    echo "UUID=$uuid $mnt ext4 defaults,nofail 0 2" >> /etc/fstab
  fi
  [ -e "$mnt/.megh-volume" ] || date -u > "$mnt/.megh-volume"
}
`

// Script renders the user_data script.
//
// The values travel in a 0600 file sourced into the shell and passed by name
// (`-e NAME`), never on a command line, because a public key list spans lines
// and `ps` shows argv. The box is cut off from the metadata service
// (169.254.169.254), where this script, and so the pull token, can be read; a
// systemd unit reapplies that rule after a reboot, since Docker recreates its
// chains empty.
func Script(s Spec) string {
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

	switch {
	case s.Volume.AutomountPath != "":
		fmt.Fprintf(&b, "vol=%s\nfor _ in $(seq 1 90); do mountpoint -q \"$vol\" && break; sleep 2; done\n", Shq(s.Volume.AutomountPath))
		b.WriteString("mountpoint -q \"$vol\" || { echo \"megh: volume $vol never mounted\"; exit 1; }\n")
	case s.Volume.BlockSizeGB > 0:
		b.WriteString(PrepareVolume)
		fmt.Fprintf(&b, "vol=%s\nmegh_prepare_volume %d \"$vol\"\n", blockMount, s.Volume.BlockSizeGB)
	default:
		b.WriteString("echo 'megh: no volume given'; exit 1\n")
	}

	names := make([]string, 0, len(s.Env))
	for k := range s.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("umask 077\ncat >/root/megh.env <<'ENV'\n")
	for _, k := range names {
		fmt.Fprintf(&b, "%s=%s\n", k, Shq(s.Env[k]))
	}
	if s.PullToken != "" {
		fmt.Fprintf(&b, "MEGH_PULL_TOKEN=%s\n", Shq(s.PullToken))
	}
	b.WriteString("ENV\nset -a\n. /root/megh.env\nset +a\nrm -f /root/megh.env\n")

	registry := strings.SplitN(s.Image, "/", 2)[0]
	if s.PullToken != "" {
		fmt.Fprintf(&b, "printf '%%s' \"$MEGH_PULL_TOKEN\" | docker login %s -u %s --password-stdin\nunset MEGH_PULL_TOKEN\n",
			Shq(registry), Shq(s.PullUser))
	}

	b.WriteString("docker run -d --name megh-box --restart unless-stopped")
	if s.ExposeSSH {
		fmt.Fprintf(&b, " -p %d:22", BoxSSHPort)
	}
	b.WriteString(` -v "$vol":/workspace`)
	for _, k := range names {
		fmt.Fprintf(&b, " -e %s", k)
	}
	fmt.Fprintf(&b, " %s\n", Shq(s.Image))
	if s.PullToken != "" {
		b.WriteString("docker logout " + Shq(registry) + " || true\n")
	}
	return b.String()
}
