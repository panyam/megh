package hetzner

import (
	"fmt"

	"github.com/panyam/megh/internal/providers/vmhost"
)

// boxSSHPort is where the box's own sshd is published on the VM.
const boxSSHPort = vmhost.BoxSSHPort

// bootSpec is the Hetzner view of vmhost.Spec: the volume is identified by id,
// because Hetzner formats it at creation and automounts it at a path derived
// from that id, so the boot script never formats anything.
type bootSpec struct {
	Image     string
	VolumeID  int64
	Env       map[string]string
	PullUser  string
	PullToken string
	ExposeSSH bool
}

func shq(s string) string { return vmhost.Shq(s) }

func cloudInit(s bootSpec) string {
	return vmhost.Script(vmhost.Spec{
		Image:     s.Image,
		Volume:    vmhost.Volume{AutomountPath: fmt.Sprintf("/mnt/HC_Volume_%d", s.VolumeID)},
		Env:       s.Env,
		PullUser:  s.PullUser,
		PullToken: s.PullToken,
		ExposeSSH: s.ExposeSSH,
	})
}
