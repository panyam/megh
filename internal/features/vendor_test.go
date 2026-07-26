package features

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestVendoredAssetsMatchChecksums guards the vendored webterm assets (xterm.js
// etc.) against silent drift, corruption, or a hand-edit of a minified bundle:
// it hashes the bytes actually embedded in the binary and compares them to
// vendor/SHA256SUMS. Regenerate both with vendor/update.sh (or `make vendor`).
func TestVendoredAssetsMatchChecksums(t *testing.T) {
	f, err := os.Open("vendor/SHA256SUMS")
	if err != nil {
		t.Fatalf("open vendor/SHA256SUMS: %v", err)
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// sha256sum format: "<hex>  <name>"
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed SHA256SUMS line: %q", line)
		}
		want, name := fields[0], fields[1]

		b, err := vendorFS.ReadFile("vendor/" + name)
		if err != nil {
			t.Fatalf("asset %q listed in SHA256SUMS is not embedded (add it to the go:embed in features.go): %v", name, err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 mismatch (embedded %s, SHA256SUMS %s); run vendor/update.sh", name, got, want)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no checksums found in vendor/SHA256SUMS")
	}
}
