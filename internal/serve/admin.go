package serve

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/panyam/megh/internal/lifecycle"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/providers/runpod"
)

// Prober is the region search a backend may offer. RunPod's does; a backend
// without one makes the region endpoints answer 501.
type Prober interface {
	DataCenters(ctx context.Context) []string
	Probe(ctx context.Context, o providers.Options) runpod.ProbeResult
}

func proberOf(svc *lifecycle.Service) (Prober, error) {
	prov, err := svc.Provider("")
	if err != nil {
		return nil, err
	}
	p, ok := prov.(Prober)
	if !ok {
		return nil, &apiError{http.StatusNotImplemented, prov.Name() + " has no region search"}
	}
	return p, nil
}

// volumeName is a RunPod volume name the page may create: one DNS-label-ish
// token, so it reads the same in the console, the menu and `megh storage`.
var volumeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Volume sizes the page offers, in GB. A volume bills monthly whether or not a
// box uses it, so the choices stay small and explicit.
var volumeSizes = []int{20, 50, 100, 200}

// createVolume makes a scratch volume in one data center. It is how the page
// moves to a region that has capacity: the volume pins where boxes can start.
func (s *Server) createVolume(r *http.Request, svc *lifecycle.Service) (any, error) {
	var req struct {
		Name   string `json:"name"`
		SizeGB int    `json:"sizeGB"`
		DC     string `json:"dc"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !volumeName.MatchString(req.Name) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%q is not a valid volume name (lowercase letters, digits and dashes)", req.Name)}
	}
	if !slices.Contains(volumeSizes, req.SizeGB) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%d GB is not an offered size %v", req.SizeGB, volumeSizes)}
	}
	if p, err := proberOf(svc); err == nil && !slices.Contains(p.DataCenters(r.Context()), req.DC) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%q is not a data center RunPod offers", req.DC)}
	}
	v, err := svc.CreateVolume(r.Context(), "", req.Name, req.SizeGB, req.DC)
	if err != nil {
		return nil, err
	}
	return VolumeView{ID: v.ID, Name: v.Name, DC: v.DataCenter, SizeGB: v.Size}, nil
}

// deleteVolume deletes a volume only when the request repeats its name in
// confirm, so a stray or replayed request cannot take one out. RunPod itself
// refuses a volume still attached to a box.
func (s *Server) deleteVolume(r *http.Request, svc *lifecycle.Service) (any, error) {
	var req struct {
		ID      string `json:"id"`
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	vols, _ := svc.Volumes(r.Context())
	i := slices.IndexFunc(vols, func(v providers.Volume) bool { return v.ID == req.ID })
	if i < 0 {
		return nil, &apiError{http.StatusNotFound, fmt.Sprintf("no volume %q on this account", req.ID)}
	}
	if req.Confirm == "" || req.Confirm != vols[i].Name {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("type the volume's name (%s) to delete it", vols[i].Name)}
	}
	if err := svc.DeleteVolume(r.Context(), "", req.ID); err != nil {
		return nil, err
	}
	return map[string]string{"deleted": vols[i].Name}, nil
}

// regions lists data centers to probe: the US ones by default, every one with
// ?all=1, plus megh.yaml's default so the page can mark it.
func (s *Server) regions(r *http.Request, svc *lifecycle.Service) (any, error) {
	p, err := proberOf(svc)
	if err != nil {
		return nil, err
	}
	all := p.DataCenters(r.Context())
	dcs := all
	if r.URL.Query().Get("all") != "1" {
		if us := runpod.USDataCenters(all); len(us) > 0 {
			dcs = us
		}
	}
	return map[string]any{"dcs": dcs, "default": s.Config.Provider("runpod").DefaultDC}, nil
}

// ProbeView is one data center's answer to a probe.
type ProbeView struct {
	DC       string `json:"dc"`
	Rentable bool   `json:"rentable"`
	Verdict  string `json:"verdict"`
	// OrphanID is set when the probe pod was created but could not be
	// terminated, so it is billing; the page offers to terminate it by id.
	OrphanID string `json:"orphanId,omitempty"`
}

// probe tries to rent one box in one data center and terminates it at once.
// The page drives a sweep one data center per request, so every request is
// short and progress shows as it goes; "place" is that loop stopping at the
// first rentable region, then createVolume there.
func (s *Server) probe(r *http.Request, svc *lifecycle.Service) (any, error) {
	var req struct {
		DC   string `json:"dc"`
		VCPU int    `json:"vcpu"` // 0 = megh.yaml's default size
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	p, err := proberOf(svc)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(p.DataCenters(r.Context()), req.DC) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%q is not a data center RunPod offers", req.DC)}
	}
	cfgP := s.Config.Provider("runpod")
	o := providers.Options{
		DataCenter: req.DC,
		Image:      s.Config.DefaultImage(s.Config.DefaultFlavor),
		VCPU:       firstPositive(cfgP.VCPU, 2),
		RAMGiB:     firstPositive(cfgP.RAM, 8),
		DiskGiB:    firstPositive(cfgP.Disk, 20),
	}
	if req.VCPU != 0 {
		size, ok := boxSizes[req.VCPU]
		if !ok {
			return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%d vCPU is not an offered size (2, 4 or 8)", req.VCPU)}
		}
		o.VCPU, o.RAMGiB, o.DiskGiB = req.VCPU, size.ram, size.disk
	}
	if o.Image == "" {
		return nil, &apiError{http.StatusBadRequest, "no image to probe with: set registries[0].namespace in megh.yaml"}
	}
	res := p.Probe(r.Context(), o)
	v := ProbeView{DC: res.DC, Rentable: res.Rentable, Verdict: res.Reason()}
	if res.Orphan != nil {
		v.OrphanID = res.PodID
	}
	return v, nil
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
