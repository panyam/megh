package vmhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// disk is one block device the fake lsblk reports.
type disk struct {
	name  string
	bytes int64
	mount string   // non-empty: something is mounted from it
	parts []string // partitions under it
}

// runPrepare runs PrepareVolume for a gb-sized volume against fake lsblk,
// blkid, mkfs.ext4 and mount, and returns the exit error and the log of what
// would have been done to which device.
func runPrepare(t *testing.T, gb int, disks []disk, blkidRC int) (log string, err error) {
	t.Helper()
	bash, lerr := exec.LookPath("bash")
	if lerr != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	logf := filepath.Join(dir, "log")

	var list, mounts, parts strings.Builder
	for _, d := range disks {
		list.WriteString(d.name + " " + itoa(d.bytes) + " disk\n")
		if d.mount != "" {
			mounts.WriteString("/dev/" + d.name + ") echo " + d.mount + " ;;\n")
		}
		parts.WriteString("/dev/" + d.name + ") echo " + d.name)
		for _, p := range d.parts {
			parts.WriteString("; echo " + p)
		}
		parts.WriteString(" ;;\n")
	}
	lsblk := "#!/bin/bash\n" +
		"case \"$*\" in\n" +
		"  '-b -dn -o NAME,SIZE,TYPE') cat <<'EOF'\n" + list.String() + "EOF\n;;\n" +
		"  '-n -o MOUNTPOINT '*) case \"$4\" in\n" + mounts.String() + "  *) echo ;; esac ;;\n" +
		"  '-n -o NAME '*) case \"$4\" in\n" + parts.String() + "  esac ;;\n" +
		"esac\n"
	write := func(name, body string) { os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755) }
	write("lsblk", lsblk)
	write("blkid", "#!/bin/bash\nif [ \"$1\" = -p ]; then echo \"blkid -p $2\" >> "+logf+"; exit "+itoa(int64(blkidRC))+"; fi\necho uuid-1234\n")
	write("mkfs.ext4", "#!/bin/bash\necho \"mkfs $*\" >> "+logf+"\n")
	write("mount", "#!/bin/bash\necho \"mount $*\" >> "+logf+"\n")
	write("sleep", "#!/bin/bash\nexit 0\n")

	mnt := filepath.Join(dir, "mnt")
	script := PrepareVolume + "megh_prepare_volume " + itoa(int64(gb)) + " " + mnt + "\n"
	cmd := exec.Command(bash, "-c", strings.ReplaceAll(script, "/etc/fstab", filepath.Join(dir, "fstab")))
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "MEGH_DISK_WAIT=2"}
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(logf)
	return string(b) + "\n--- output\n" + string(out), err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

const gib = 1 << 30

var bootDisk = disk{name: "vda", bytes: 25 * gib, mount: "/", parts: []string{"vda1"}}

func TestABlankVolumeIsFormattedOnceAndMounted(t *testing.T) {
	log, err := runPrepare(t, 50, []disk{bootDisk, {name: "vdb", bytes: 50 * gib}}, 2)
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if !strings.Contains(log, "mkfs -q -L megh-vol /dev/vdb") || !strings.Contains(log, "mount /dev/vdb") {
		t.Errorf("want format then mount of vdb:\n%s", log)
	}
}

// The whole point: a volume with data on it is mounted, never formatted.
func TestAVolumeWithAFilesystemIsNeverFormatted(t *testing.T) {
	log, err := runPrepare(t, 50, []disk{bootDisk, {name: "vdb", bytes: 50 * gib}}, 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if strings.Contains(log, "mkfs") {
		t.Errorf("formatted a volume that had a filesystem:\n%s", log)
	}
	if !strings.Contains(log, "mount /dev/vdb") {
		t.Errorf("should have mounted it:\n%s", log)
	}
}

// blkid failing for any reason but "no signature" stops the boot untouched.
func TestABlkidErrorStopsWithoutFormatting(t *testing.T) {
	for _, rc := range []int{1, 4, 8} {
		log, err := runPrepare(t, 50, []disk{bootDisk, {name: "vdb", bytes: 50 * gib}}, rc)
		if err == nil || strings.Contains(log, "mkfs") || strings.Contains(log, "mount /dev") {
			t.Errorf("blkid exit %d: err=%v\n%s", rc, err, log)
		}
	}
}

func TestOnlyAnUnpartitionedUnmountedDiskOfTheExactSizeIsEverTouched(t *testing.T) {
	for name, disks := range map[string][]disk{
		"wrong size":  {bootDisk, {name: "vdb", bytes: 40 * gib}},
		"partitioned": {bootDisk, {name: "vdb", bytes: 50 * gib, parts: []string{"vdb1"}}},
		"mounted":     {bootDisk, {name: "vdb", bytes: 50 * gib, mount: "/data"}},
		"boot only":   {bootDisk},
	} {
		log, err := runPrepare(t, 50, disks, 2)
		if err == nil || strings.Contains(log, "mkfs") || strings.Contains(log, "blkid -p") {
			t.Errorf("%s: err=%v\n%s", name, err, log)
		}
	}
}

// A provider that reports sizes in GB rather than GiB still matches.
func TestAVolumeSizedInGBMatchesToo(t *testing.T) {
	log, err := runPrepare(t, 50, []disk{bootDisk, {name: "vdc", bytes: 50 * 1000000000}}, 2)
	if err != nil || !strings.Contains(log, "mkfs -q -L megh-vol /dev/vdc") {
		t.Errorf("err=%v\n%s", err, log)
	}
}

func TestScriptUsesTheRightVolumeStep(t *testing.T) {
	auto := Script(Spec{Image: "ghcr.io/a/b", Volume: Volume{AutomountPath: "/mnt/HC_Volume_7"}})
	if !strings.Contains(auto, "'/mnt/HC_Volume_7'") || strings.Contains(auto, "megh_prepare_volume") || strings.Contains(auto, "mkfs") {
		t.Errorf("automount script:\n%s", auto)
	}
	blk := Script(Spec{Image: "ghcr.io/a/b", Volume: Volume{BlockSizeGB: 50}})
	if !strings.Contains(blk, "megh_prepare_volume 50 \"$vol\"") || !strings.Contains(blk, `-v "$vol":/workspace`) {
		t.Errorf("block script:\n%s", blk)
	}
	if none := Script(Spec{Image: "x/y"}); !strings.Contains(none, "no volume given") {
		t.Errorf("no volume must refuse:\n%s", none)
	}
}

func TestShqRoundTripsThroughBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, v := range []string{"plain", "it's", "a\nb", `$HOME "x" \n`} {
		out, err := exec.Command(bash, "-c", "x="+Shq(v)+"; printf '%s' \"$x\"").Output()
		if err != nil || string(out) != v {
			t.Errorf("%q came back %q (%v)", v, out, err)
		}
	}
}
