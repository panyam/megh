package serve

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"

	"github.com/panyam/megh/internal/lifecycle"
	"github.com/panyam/megh/internal/providers"
	"github.com/panyam/megh/internal/providers/runpod"
)

// Prober is RunPod's region search: rent a box for a second to prove a data
// center has capacity. A backend with neither this nor a providers.Locator
// makes the region endpoints answer 501.
type Prober interface {
	DataCenters(ctx context.Context) []string
	Probe(ctx context.Context, o providers.Options) runpod.ProbeResult
}

// backend is the named provider, or the request's default one for "", as a
// 400 when the request holds no key for it.
func (s *Server) backend(svc *lifecycle.Service, name string) (providers.Provider, error) {
	name = cmp.Or(name, s.defaultProvider(svc))
	prov, err := svc.Provider(name)
	if err != nil {
		if env := keyVar(name); env != "" {
			return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("no %s key on the server or in this tab: add %s under Keys", name, env)}
		}
		return nil, &apiError{http.StatusBadRequest, err.Error()}
	}
	return prov, nil
}

func proberOf(prov providers.Provider) (Prober, error) {
	p, ok := prov.(Prober)
	if !ok {
		return nil, &apiError{http.StatusNotImplemented, prov.Name() + " has no region probe"}
	}
	return p, nil
}

// dataCenters is where prov can place a volume a box could then start on:
// RunPod's data centers (probed, since it publishes no catalog of what is
// free), or every location a Locator sells anything in. ok is false when the
// backend can say neither.
func dataCenters(ctx context.Context, prov providers.Provider) (dcs []string, ok bool, err error) {
	if p, isProber := prov.(Prober); isProber {
		return p.DataCenters(ctx), true, nil
	}
	if l, isLocator := prov.(providers.Locator); isLocator {
		offers, err := l.Offers(ctx, providers.Want{})
		for _, o := range providers.CheapestPerDC(offers) {
			dcs = append(dcs, o.DC)
		}
		return dcs, true, err
	}
	return nil, false, nil
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
		Provider string `json:"provider"` // "" = the default backend
		Name     string `json:"name"`
		SizeGB   int    `json:"sizeGB"`
		DC       string `json:"dc"`
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
	prov, err := s.backend(svc, req.Provider)
	if err != nil {
		return nil, err
	}
	dcs, known, err := dataCenters(r.Context(), prov)
	if err != nil {
		return nil, err
	}
	if known && !slices.Contains(dcs, req.DC) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%q is not a data center %s offers", req.DC, prov.Name())}
	}
	v, err := svc.CreateVolume(r.Context(), prov.Name(), req.Name, req.SizeGB, req.DC)
	if err != nil {
		return nil, err
	}
	return VolumeView{Provider: v.Provider, ID: v.ID, Name: v.Name, DC: v.DataCenter, SizeGB: v.Size}, nil
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
	if err := svc.DeleteVolume(r.Context(), vols[i].Provider, req.ID); err != nil {
		return nil, err
	}
	return map[string]string{"deleted": vols[i].Name}, nil
}

// regions answers "where can a box run" for one backend (?provider=, default
// the request's default). RunPod publishes no catalog of what is free, so it
// answers with data centers to probe: the US ones by default, every one with
// ?all=1. A catalog backend (Vultr, Hetzner) answers with the cheapest machine
// per location meeting ?vcpu/ram/disk, cheapest first. Either way megh.yaml's
// default data center comes back so the page can mark it.
func (s *Server) regions(r *http.Request, svc *lifecycle.Service) (any, error) {
	q := r.URL.Query()
	prov, err := s.backend(svc, q.Get("provider"))
	if err != nil {
		return nil, err
	}
	def := s.Config.Provider(prov.Name()).DefaultDC
	if p, ok := prov.(Prober); ok {
		all := p.DataCenters(r.Context())
		dcs := all
		if q.Get("all") != "1" {
			if us := runpod.USDataCenters(all); len(us) > 0 {
				dcs = us
			}
		}
		return map[string]any{"provider": prov.Name(), "dcs": dcs, "default": def, "places": placesOf(r.Context(), prov)}, nil
	}
	l, ok := prov.(providers.Locator)
	if !ok {
		return nil, &apiError{http.StatusNotImplemented, prov.Name() + " has no region search"}
	}
	want, err := wantFrom(q)
	if err != nil {
		return nil, err
	}
	all, err := l.Offers(r.Context(), want)
	if err != nil {
		return nil, err
	}
	offers := providers.CheapestPerDC(all)
	dcs := make([]string, 0, len(offers))
	for _, of := range offers {
		dcs = append(dcs, of.DC)
	}
	return map[string]any{"provider": prov.Name(), "dcs": dcs, "offers": offers, "default": def, "places": placesOf(r.Context(), prov)}, nil
}

// offers answers GET /api/offers: every machine one backend sells at or above
// ?vcpu/ram/disk, in ?dc or everywhere, cheapest first, for the launch form's
// table. RunPod prices per data center, so it needs ?dc (the volume's).
func (s *Server) offers(r *http.Request, svc *lifecycle.Service) (any, error) {
	q := r.URL.Query()
	prov, err := s.backend(svc, q.Get("provider"))
	if err != nil {
		return nil, err
	}
	l, ok := prov.(providers.Locator)
	if !ok {
		return nil, &apiError{http.StatusNotImplemented, prov.Name() + " has no catalog"}
	}
	want, err := wantFrom(q)
	if err != nil {
		return nil, err
	}
	want.DC = q.Get("dc")
	if _, needsDC := prov.(Prober); needsDC && want.DC == "" {
		return nil, &apiError{http.StatusBadRequest, prov.Name() + " prices machines per data center: pick a volume (or pass dc)"}
	}
	all, err := l.Offers(r.Context(), want)
	if err != nil {
		return nil, err
	}
	if all == nil {
		all = []providers.Offer{}
	}
	return map[string]any{"provider": prov.Name(), "offers": all, "places": placesOf(r.Context(), prov)}, nil
}

// wantFrom reads ?vcpu, ?ram and ?disk (absent = no minimum) and checks them
// against what the form offers.
func wantFrom(q url.Values) (providers.Want, error) {
	n := func(k string) (int, error) {
		v, err := strconv.Atoi(cmp.Or(q.Get(k), "0"))
		if err != nil {
			return 0, &apiError{http.StatusBadRequest, fmt.Sprintf("%s=%q is not a number", k, q.Get(k))}
		}
		return v, nil
	}
	vcpu, err := n("vcpu")
	if err != nil {
		return providers.Want{}, err
	}
	ram, err := n("ram")
	if err != nil {
		return providers.Want{}, err
	}
	disk, err := n("disk")
	if err != nil {
		return providers.Want{}, err
	}
	return minimums(vcpu, ram, disk)
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
		Provider string `json:"provider"` // "" = the default backend
		DC       string `json:"dc"`
		VCPU     int    `json:"vcpu"` // minimums; 0 = megh.yaml's default
		RAM      int    `json:"ram"`
		Disk     int    `json:"disk"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	prov, err := s.backend(svc, req.Provider)
	if err != nil {
		return nil, err
	}
	p, err := proberOf(prov)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(p.DataCenters(r.Context()), req.DC) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("%q is not a data center %s offers", req.DC, prov.Name())}
	}
	want, err := minimums(req.VCPU, req.RAM, req.Disk)
	if err != nil {
		return nil, err
	}
	cfgP := s.Config.Provider(prov.Name())
	o := providers.Options{
		VCPU:    firstPositive(want.VCPU, cfgP.VCPU, 2),
		RAMGiB:  firstPositive(want.RAMGiB, cfgP.RAM, 8),
		DiskGiB: firstPositive(want.DiskGiB, cfgP.Disk, 20),
	}
	o.DataCenter = req.DC
	o.Image = s.Config.DefaultImage(s.Config.DefaultFlavor)
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
