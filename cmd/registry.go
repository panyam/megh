package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/megh/internal/registry"
	"github.com/spf13/cobra"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Inspect the container registries megh pulls dev-env images from",
}

var registryLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List configured registries and the tags of each dev-env image",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		for _, reg := range cfg.Registries {
			fmt.Printf("%s  (%s/%s)\n", reg.Name, reg.Host, reg.Namespace)
			for _, flavor := range cfg.Flavors {
				image := "megh-" + flavor
				tags, err := registry.ListTags(ctx, reg, image)
				switch {
				case err != nil:
					fmt.Printf("  %-16s error: %v\n", image, err)
				case len(tags) == 0:
					fmt.Printf("  %-16s (no tags yet)\n", image)
				default:
					fmt.Printf("  %-16s %s\n", image, strings.Join(tags, ", "))
				}
			}
		}
		return nil
	},
}

var registryTagsRegistry string

var registryTagsCmd = &cobra.Command{
	Use:   "tags <image>",
	Short: "List tags for one image (e.g. megh-base)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, ok := cfg.Find(registryTagsRegistry)
		if !ok {
			return fmt.Errorf("unknown registry %q", registryTagsRegistry)
		}
		tags, err := registry.ListTags(context.Background(), reg, args[0])
		if err != nil {
			return err
		}
		for _, t := range tags {
			fmt.Println(t)
		}
		return nil
	},
}

func init() {
	registryTagsCmd.Flags().StringVar(&registryTagsRegistry, "registry", "",
		"registry name (default: first configured)")
	registryCmd.AddCommand(registryLsCmd, registryTagsCmd)
	rootCmd.AddCommand(registryCmd)
}
