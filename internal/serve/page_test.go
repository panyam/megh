package serve

import (
	"os/exec"
	"testing"
)

// The page's key parser is JavaScript, so this runs it under node against the
// pasted-note shapes in testdata/parse_keys.js. Skipped where node is absent.
func TestPageKeyParser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "testdata/parse_keys.js", "web/app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("parseKeys cases failed:\n%s", out)
	}
}
