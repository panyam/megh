package cmd

import (
	"context"
	"os"

	"github.com/panyam/megh/internal/lifecycle"
	"github.com/panyam/megh/internal/providers"
)

// newService is the lifecycle service as the CLI runs it: every registered
// backend, credentials from this machine's environment, progress on stdout and
// warnings on stderr. Built per call so it sees the cfg PersistentPreRunE loaded.
func newService() *lifecycle.Service {
	return &lifecycle.Service{
		Config:    cfg,
		Providers: providers.All(),
		Tailscale: tsClient,
		Out:       os.Stdout,
		Err:       os.Stderr,
	}
}

// mintBoxAuthKey is lifecycle.Service.MintAuthKey for `megh mesh join`.
func mintBoxAuthKey(ctx context.Context, box string) string {
	return newService().MintAuthKey(ctx, box)
}

// bootAuthKey is lifecycle.Service.BootAuthKey.
func bootAuthKey(ctx context.Context, m providers.Mesh, box string) string {
	return newService().BootAuthKey(ctx, m, box)
}
