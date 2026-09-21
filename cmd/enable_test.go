package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/features"
)

// /dev/null IS a character device, so the os.ModeCharDevice test that looks like
// the right one says "terminal" for it. `megh enable </dev/null` then printed a
// menu and prompted at an input that answers EOF, which reads as the command
// refusing to list. Anything non-interactive must take the listing path.
func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("isTerminal(/dev/null) is true; the chooser will prompt where nothing can answer")
	}
}

func TestPickFromList(t *testing.T) {
	names := []string{"code", "vnc", "playwright"}
	for _, tc := range []struct {
		answer, want, wantErr string
	}{
		{answer: "2\n", want: "vnc"},
		{answer: "  3  \n", want: "playwright"},
		{answer: "vnc\n", want: "vnc"},       // the name works too: it is what every other call uses
		{answer: "\n", want: ""},             // Enter cancels
		{answer: "", want: ""},               // EOF cancels
		{answer: "0\n", wantErr: "pick 1-3"}, // a zero-based guess
		{answer: "9\n", wantErr: "pick 1-3"},
		{answer: "vnk\n", wantErr: `unknown feature "vnk"`},
	} {
		var out bytes.Buffer
		got, err := pickFromList(strings.NewReader(tc.answer), &out, names)
		switch {
		case tc.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("answer %q: error = %v, want one containing %q", tc.answer, err, tc.wantErr)
			}
		case err != nil:
			t.Errorf("answer %q: unexpected error %v", tc.answer, err)
		case got != tc.want:
			t.Errorf("answer %q: got %q, want %q", tc.answer, got, tc.want)
		}
	}
}

// The menu is only useful if it says what each feature does, and the summary
// comes from the script's own header rather than a third copy of the list.
func TestPickFromListShowsWhatEachFeatureDoes(t *testing.T) {
	var out bytes.Buffer
	if _, err := pickFromList(strings.NewReader("\n"), &out, []string{"vnc"}); err != nil {
		t.Fatalf("pickFromList: %v", err)
	}
	if want := features.Describe("vnc"); !strings.Contains(out.String(), summarize(want)) {
		t.Errorf("the menu does not carry vnc's own summary %q:\n%s", want, out.String())
	}
}

// The hand-written help text and the embedded scripts are two lists of the same
// features. A feature added without a help line is invisible to anyone reading
// --help, and a help line for a feature that was removed sends them to an
// "unknown feature" error.
func TestEnableHelpNamesEveryFeature(t *testing.T) {
	help := enableCmd.Long
	for _, name := range features.List() {
		if !strings.Contains(help, "megh enable "+name) {
			t.Errorf("`megh enable --help` never mentions the %q feature", name)
		}
	}
}
