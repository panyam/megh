package runpod

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/panyam/megh/internal/providers"
)

var _ providers.Locator = (*Provider)(nil)

// cpuFlavor is RunPod's rule for one CPU family: RAM is vCPU times
// RAMMultiplier, and the container disk may be up to vCPU times
// DiskLimitPerVcpu.
type cpuFlavor struct {
	ID               string `json:"id"`
	MinVcpu          int    `json:"minVcpu"`
	MaxVcpu          int    `json:"maxVcpu"`
	RAMMultiplier    int    `json:"ramMultiplier"`
	DiskLimitPerVcpu int    `json:"diskLimitPerVcpu"`
}

// vcpuSteps are the vCPU counts megh offers within a flavor's range. RunPod
// prices each instance on its own, and one it does not sell comes back
// unpriced and is dropped, so a step that does not exist costs nothing.
var vcpuSteps = []int{2, 4, 6, 8, 12, 16, 24, 32}

var (
	flavorsMu    sync.Mutex
	flavorsCache []cpuFlavor
)

func (p *Provider) flavors(ctx context.Context) ([]cpuFlavor, error) {
	flavorsMu.Lock()
	defer flavorsMu.Unlock()
	if flavorsCache != nil {
		return flavorsCache, nil
	}
	var out struct {
		Data struct {
			CPUFlavors []cpuFlavor `json:"cpuFlavors"`
		} `json:"data"`
	}
	if err := p.graphql(ctx, "{ cpuFlavors { id minVcpu maxVcpu ramMultiplier diskLimitPerVcpu } }", &out); err != nil {
		return nil, err
	}
	flavorsCache = out.Data.CPUFlavors
	return flavorsCache, nil
}

// instanceID is RunPod's name for one machine: flavor, vCPU, RAM in GB.
func instanceID(f cpuFlavor, vcpu int) string {
	return fmt.Sprintf("%s-%d-%d", f.ID, vcpu, vcpu*f.RAMMultiplier)
}

// Offers is every machine the flavor rules allow that meets w and that RunPod
// prices in w.DC, cheapest first. RunPod prices per data center, so a DC is
// required: the volume's, normally. All the prices come from one GraphQL
// request, one alias per instance. Stock is RunPod's own hint; it said "High"
// for US-IL-1 the hour that data center refused every size, so it is shown
// and not trusted.
func (p *Provider) Offers(ctx context.Context, w providers.Want) ([]providers.Offer, error) {
	if w.DC == "" {
		return nil, fmt.Errorf("runpod prices machines per data center: name one (the volume's, or --dc)")
	}
	fls, err := p.flavors(ctx)
	if err != nil {
		return nil, err
	}
	type cand struct {
		f    cpuFlavor
		vcpu int
	}
	var cands []cand
	var q strings.Builder
	q.WriteString("{")
	for _, f := range fls {
		for _, n := range vcpuSteps {
			if n < f.MinVcpu || n > f.MaxVcpu || n < w.VCPU || n*f.RAMMultiplier < w.RAMGiB || n*f.DiskLimitPerVcpu < w.DiskGiB {
				continue
			}
			fmt.Fprintf(&q, " i%d: cpuFlavors { id specifics(input:{dataCenterId:%q, instanceId:%q}) { stockStatus securePrice } }",
				len(cands), w.DC, instanceID(f, n))
			cands = append(cands, cand{f, n})
		}
	}
	q.WriteString(" }")
	if len(cands) == 0 {
		return nil, nil
	}
	type spec struct {
		StockStatus string  `json:"stockStatus"`
		SecurePrice float64 `json:"securePrice"`
	}
	var out struct {
		Data map[string][]struct {
			Specifics *spec `json:"specifics"`
		} `json:"data"`
	}
	if err := p.graphql(ctx, q.String(), &out); err != nil {
		return nil, err
	}
	var offers []providers.Offer
	for i, c := range cands {
		for _, e := range out.Data["i"+strconv.Itoa(i)] {
			if e.Specifics == nil || e.Specifics.SecurePrice <= 0 {
				continue
			}
			offers = append(offers, providers.Offer{DC: w.DC, Type: instanceID(c.f, c.vcpu), VCPU: c.vcpu,
				RAMGiB: c.vcpu * c.f.RAMMultiplier, DiskGiB: c.vcpu * c.f.DiskLimitPerVcpu,
				PerHr: e.Specifics.SecurePrice, Stock: e.Specifics.StockStatus})
			break
		}
	}
	providers.SortOffers(offers)
	return offers, nil
}

// podShape is the vCPU count and flavor list a create request carries. A
// picked instance ("cpu5m-6-48") is that one flavor at its vCPU count; without
// one it is the requested vCPU across every flavor, preferred first, which is
// how a launch worked before the catalog.
func podShape(o providers.Options) (int, []string, error) {
	if o.Type == "" {
		return o.VCPU, cpuFlavorIDs(o.VCPU, o.RAMGiB), nil
	}
	parts := strings.Split(o.Type, "-")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "cpu") {
		return 0, nil, fmt.Errorf("runpod instance %q is not flavor-vcpu-ram (e.g. cpu3g-4-16)", o.Type)
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 0 {
		return 0, nil, fmt.Errorf("runpod instance %q has no vCPU count", o.Type)
	}
	return n, []string{parts[0]}, nil
}
