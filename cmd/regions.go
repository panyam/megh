package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/panyam/megh/internal/providers/runpod"
	"github.com/spf13/cobra"
)

// Region search exists because placement on RunPod is not a lookup. A network
// volume is pinned to one data center and the pod must run in that same one, so
// a box needs a region with both volume support and rentable CPU. RunPod
// publishes no availability API, and a flavor being defined in a region is not
// the same as it being free, so the only definitive test is a real rent attempt.
// These commands automate the loop that was previously done by hand.

var (
	regionsProvider string
	regionsDCs      string
	regionsAll      bool
	regionsFirst    bool
	regionsVCPU     int
	regionsRAM      int
	regionsDisk     int
	regionsImage    string
	regionsYes      bool
	regionsVolName  string
	regionsVolSize  int
)

var regionsCmd = &cobra.Command{
	Use:     "regions",
	Aliases: []string{"region"},
	Short:   "Find a data center that can actually rent the box you want",
}

var regionsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List candidate data centers (US only unless --all)",
	Long: `List the data centers RunPod will accept for a CPU pod, read from the pods
schema in its published OpenAPI document.

This is where a pod may be PLACED, not where one is rentable right now. Only
'megh regions probe' answers that, and only by trying.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if regionsProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", regionsProvider)
		}
		ctx := context.Background()
		dcs := candidateDCs(ctx)

		// Mark the regions where scratch already exists: a volume there means no
		// new volume to create, which usually decides the placement on its own.
		held := map[string][]runpod.Volume{}
		if vols, err := runpod.Volumes(ctx); err == nil {
			for _, v := range vols {
				held[v.DataCenter] = append(held[v.DataCenter], v)
			}
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "DC\tVOLUMES")
		for _, dc := range dcs {
			var names []string
			for _, v := range held[dc] {
				names = append(names, fmt.Sprintf("%s (%s, %dGB)", v.ID, v.Name, v.Size))
			}
			fmt.Fprintf(w, "%s\t%s\n", dc, strings.Join(names, ", "))
		}
		return w.Flush()
	},
}

var regionsProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Try to rent a box in each candidate data center and report which took it",
	Long: `Probe answers "where can I launch right now" the only way RunPod allows: by
renting. For each data center it creates a pod with the requested shape and no
network volume, then terminates it immediately. A refused create leaves nothing
behind; an accepted one lives about a second, never pulls the image, and costs a
fraction of a cent.

Probes run one region at a time so at most one probe pod exists at any moment.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if regionsProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", regionsProvider)
		}
		ctx := context.Background()
		dcs := candidateDCs(ctx)
		opts, err := probeOptions(cmd)
		if err != nil {
			return err
		}
		if !regionsYes {
			fmt.Printf("probe %d data center(s) for %d vCPU / %d GB / %d GB disk?\n", len(dcs), opts.VCPU, opts.RAMGiB, opts.DiskGiB)
			fmt.Print("each probe rents and immediately terminates a pod. [y/N]: ")
			var resp string
			fmt.Scanln(&resp)
			if !strings.EqualFold(strings.TrimSpace(resp), "y") {
				fmt.Println("aborted")
				return nil
			}
		}
		results := probeAll(ctx, dcs, opts, regionsFirst)
		reportProbes(results)
		if !anyRentable(results) {
			return fmt.Errorf("no candidate data center could rent %d vCPU / %d GB right now", opts.VCPU, opts.RAMGiB)
		}
		return nil
	},
}

var regionsPlaceCmd = &cobra.Command{
	Use:   "place",
	Short: "Probe until a data center rents, then create a scratch volume there",
	Long: `Place is the whole placement loop in one command: probe candidate data centers
until one rents the requested shape, then create a scratch volume in that region
and print the 'megh up' line for it.

The volume it creates is a billable resource that outlives the command, unlike
the probe pods.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if regionsProvider != "runpod" {
			return fmt.Errorf("provider %q not implemented yet", regionsProvider)
		}
		if regionsVolName == "" {
			return fmt.Errorf("--name is required (the volume name)")
		}
		if regionsVolSize <= 0 {
			return fmt.Errorf("--size is required (the volume size in GB)")
		}
		ctx := context.Background()
		dcs := candidateDCs(ctx)
		opts, err := probeOptions(cmd)
		if err != nil {
			return err
		}
		if !regionsYes {
			fmt.Printf("probe up to %d data center(s), then create a %d GB volume %q in the first that rents?\n",
				len(dcs), regionsVolSize, regionsVolName)
			fmt.Print("the volume is billable and persists. [y/N]: ")
			var resp string
			fmt.Scanln(&resp)
			if !strings.EqualFold(strings.TrimSpace(resp), "y") {
				fmt.Println("aborted")
				return nil
			}
		}
		results := probeAll(ctx, dcs, opts, true)
		reportProbes(results)

		var winner string
		for _, r := range results {
			if r.Rentable {
				winner = r.DC
				break
			}
		}
		if winner == "" {
			return fmt.Errorf("no candidate data center could rent %d vCPU / %d GB right now; nothing was created", opts.VCPU, opts.RAMGiB)
		}
		v, err := runpod.CreateVolume(ctx, regionsVolName, regionsVolSize, winner)
		if err != nil {
			return fmt.Errorf("%s rents CPU but the volume could not be created there: %w", winner, err)
		}
		fmt.Printf("\ncreated volume %s  (%s, %dGB, %s)\n", v.ID, v.Name, v.Size, v.DataCenter)
		fmt.Printf("launch onto it: megh up <name> --volume %s --dc %s\n", v.ID, v.DataCenter)
		fmt.Printf("make it the default by setting default_volume/default_dc in megh.yaml\n")
		return nil
	},
}

// candidateDCs is the region set to probe: --dc wins, then every region RunPod
// accepts (--all), then the US subset, which is the default because that is
// where the boxes this CLI launches are meant to live.
func candidateDCs(ctx context.Context) []string {
	if regionsDCs != "" {
		var out []string
		for _, d := range strings.Split(regionsDCs, ",") {
			if d = strings.TrimSpace(d); d != "" {
				out = append(out, strings.ToUpper(d))
			}
		}
		return out
	}
	all := runpod.DataCenters(ctx)
	if regionsAll {
		return all
	}
	if us := runpod.USDataCenters(all); len(us) > 0 {
		return us
	}
	return all
}

// probeOptions resolves the box shape to probe from the same precedence chain
// `megh up` uses, so a probe tests the box you would actually launch.
func probeOptions(cmd *cobra.Command) (runpod.Options, error) {
	p := cfg.Provider(regionsProvider)
	image := resolve(cmd, "image", regionsImage, "MEGH_IMAGE", "", cfg.DefaultImage(cfg.DefaultFlavor))
	if image == "" {
		return runpod.Options{}, fmt.Errorf("no image resolved (set --image or $MEGH_IMAGE)")
	}
	return runpod.Options{
		Image:   image,
		VCPU:    resolveInt(cmd, "vcpu", regionsVCPU, p.VCPU, 2),
		RAMGiB:  resolveInt(cmd, "ram", regionsRAM, p.RAM, 8),
		DiskGiB: resolveInt(cmd, "disk", regionsDisk, p.Disk, 20),
	}, nil
}

// probeAll walks the regions in order, one at a time. stopAtFirst returns as
// soon as a region rents, which is what placement wants; the full sweep is for
// seeing the whole picture.
func probeAll(ctx context.Context, dcs []string, opts runpod.Options, stopAtFirst bool) []runpod.ProbeResult {
	var out []runpod.ProbeResult
	for _, dc := range dcs {
		o := opts
		o.DataCenter = dc
		fmt.Printf("probing %s ... ", dc)
		r := runpod.Probe(ctx, o)
		fmt.Println(r.Reason())
		out = append(out, r)
		if stopAtFirst && r.Rentable {
			break
		}
	}
	return out
}

func reportProbes(results []runpod.ProbeResult) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "\nDC\tVERDICT")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\n", r.DC, r.Reason())
	}
	w.Flush()
	// An orphan is the one outcome that costs money if ignored, so say it twice.
	for _, r := range results {
		if r.Orphan != nil {
			fmt.Printf("\nWARNING: probe box %s (%s) in %s is still running and billing. Terminate it: megh down %s\n",
				r.Name, r.PodID, r.DC, r.PodID)
		}
	}
}

func anyRentable(results []runpod.ProbeResult) bool {
	for _, r := range results {
		if r.Rentable {
			return true
		}
	}
	return false
}

func init() {
	for _, c := range []*cobra.Command{regionsListCmd, regionsProbeCmd, regionsPlaceCmd} {
		c.Flags().StringVar(&regionsProvider, "provider", "runpod", "provider (runpod)")
		c.Flags().StringVar(&regionsDCs, "dc", "", "comma-separated data centers to consider (default: the US regions)")
		c.Flags().BoolVar(&regionsAll, "all", false, "consider every region RunPod accepts, not just the US ones")
	}
	for _, c := range []*cobra.Command{regionsProbeCmd, regionsPlaceCmd} {
		f := c.Flags()
		f.IntVar(&regionsVCPU, "vcpu", 0, "vCPU count to probe for (default: config, else 2)")
		f.IntVar(&regionsRAM, "ram", 0, "RAM in GiB to probe for (default: config, else 8)")
		f.IntVar(&regionsDisk, "disk", 0, "container disk in GiB to probe for (default: config, else 20)")
		f.StringVar(&regionsImage, "image", "", "container image for the probe pod (default: the megh default image)")
		f.BoolVarP(&regionsYes, "yes", "y", false, "skip the confirmation prompt")
	}
	regionsProbeCmd.Flags().BoolVar(&regionsFirst, "first", false, "stop at the first region that rents")
	regionsPlaceCmd.Flags().StringVar(&regionsVolName, "name", "", "name for the volume to create")
	regionsPlaceCmd.Flags().IntVar(&regionsVolSize, "size", 0, "size of the volume to create, in GB")

	regionsCmd.AddCommand(regionsListCmd, regionsProbeCmd, regionsPlaceCmd)
	rootCmd.AddCommand(regionsCmd)
}
