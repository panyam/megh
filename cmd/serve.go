package cmd

import (
	"fmt"
	"net"
	"net/http"

	"github.com/panyam/megh/internal/serve"
	"github.com/spf13/cobra"
)

var serveAddr string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the web control plane on this machine (loopback only)",
	Long: `Run meghplane's page and API locally: list, launch and terminate RunPod
boxes from a browser.

Keys come from the page, not from this machine: paste your control-plane note
into it and the browser sends them with each request. Nothing checks who is
asking, which is why the address must be loopback. The hosted copy
(cmd/meghplane on App Engine) sits behind IAP and verifies its signed header.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		host, _, err := net.SplitHostPort(serveAddr)
		if err != nil {
			return fmt.Errorf("--addr %q: %w", serveAddr, err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("--addr %q is not loopback; `megh serve` has no sign-in, so it only listens on 127.0.0.1 or ::1", serveAddr)
		}
		fmt.Printf("meghplane on http://%s (ctrl-c to stop)\n", serveAddr)
		return http.ListenAndServe(serveAddr, serve.New(cfg).Handler())
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:8080", "listen address (loopback only)")
	rootCmd.AddCommand(serveCmd)
}
