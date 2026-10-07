// Command meghplane is the hosted web control plane, built and run by App
// Engine (see app.yaml at the repo root). It is internal/serve behind IAP: every
// request must carry a valid IAP assertion for this app from an email listed in
// megh.yaml's serve.allowed_emails, and it refuses to start if it cannot check.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/panyam/megh/internal/config"
	"github.com/panyam/megh/internal/serve"
)

const metadataURL = "http://metadata.google.internal/computeMetadata/v1/project/numeric-project-id"

func main() {
	cfg, src, err := config.Load(os.Getenv("MEGH_CONFIG"))
	if err != nil {
		log.Fatalf("meghplane: config: %v", err)
	}
	log.Printf("meghplane: config from %s", src)

	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		log.Fatal("meghplane: GOOGLE_CLOUD_PROJECT is unset; this binary runs on App Engine (use `megh serve` locally)")
	}
	num, err := projectNumber(context.Background())
	if err != nil {
		log.Fatalf("meghplane: project number: %v", err)
	}
	authorize, err := serve.NewIAP(serve.IAPConfig{
		Audience:      fmt.Sprintf("/projects/%s/apps/%s", num, project),
		AllowedEmails: cfg.Serve.AllowedEmails,
	})
	if err != nil {
		log.Fatalf("meghplane: %v", err)
	}

	srv := serve.New(cfg)
	srv.Authorize = authorize
	if cfg.Serve.Secret != "" {
		sec := &serve.GCPSecret{Name: cfg.Serve.Secret, Project: project, RegistryEnv: srv.RegistryEnv()}
		srv.Secrets = sec.Keys
		// Report what the secret holds at startup (names only, never values),
		// but start either way: a missing or unreadable secret falls back to
		// keys pasted into the page.
		k, err := sec.Keys(context.Background())
		switch {
		case err != nil:
			log.Printf("meghplane: secret %s unreadable, browser keys only: %v", cfg.Serve.Secret, err)
		default:
			log.Printf("meghplane: secret %s holds runpod=%t hetzner=%t vultr=%t tailscale=%t registry=%t", cfg.Serve.Secret,
				k.RunPod != "", k.Hetzner != "", k.Vultr != "", k.TSClientID != "" && k.TSClientSecret != "", k.Registry != "")
		}
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("meghplane: listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, srv.Handler()))
}

// projectNumber asks the metadata server, which every App Engine instance
// has, so the IAP audience needs no configuration.
func projectNumber(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata server: HTTP %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(b)), nil
}
