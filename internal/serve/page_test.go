package serve

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

type keyCase struct {
	Name string            `json:"name"`
	In   string            `json:"in"`
	Want map[string]string `json:"want"`
}

func loadKeyCases(t *testing.T) []keyCase {
	t.Helper()
	b, err := os.ReadFile("testdata/keyblock_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []keyCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// The server reads a note stored in Secret Manager with the same rules the
// page uses for a pasted one.
func TestParseKeyBlockMatchesTheSharedCases(t *testing.T) {
	for _, c := range loadKeyCases(t) {
		got := ParseKeyBlock(c.In)
		want := Keys{RunPod: c.Want["runpod"], TSClientID: c.Want["tsid"], TSClientSecret: c.Want["tssecret"]}
		if got != want {
			t.Errorf("%s: got %+v, want %+v", c.Name, got, want)
		}
	}
}

// The page's key parser is JavaScript, so this runs it under node against the
// same cases. Skipped where node is absent.
func TestPageKeyParser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "testdata/parse_keys.js", "web/app.js", "testdata/keyblock_cases.json").CombinedOutput()
	if err != nil {
		t.Fatalf("parseKeys cases failed:\n%s", out)
	}
}
