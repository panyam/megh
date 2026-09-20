package runpod

import "testing"

// A pod has no SSH to be joined over until well after create, and with
// expose_ssh: false the mesh is the only way in at all, so the key travels in
// the pod env and the box joins itself at boot. That is the opposite of the
// local backend and the reason Mesh carries AtBoot.
func TestMeshJoinsAtBoot(t *testing.T) {
	m := (&Provider{}).Mesh()
	if !m.On() || m.Vendor != "tailscale" {
		t.Errorf("mesh = %+v, want tailscale", m)
	}
	if !m.AtBoot {
		t.Error("a pod is handed its key in the create-time env")
	}
}
