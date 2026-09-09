package providers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fake is a Provider that returns canned boxes, so the shared resolution rules
// can be tested without any backend or network.
type fake struct {
	name    string
	tailnet bool
	boxes   []Box
	vols    []Volume
	listErr error
}

func (f *fake) Name() string  { return f.name }
func (f *fake) Tailnet() bool { return f.tailnet }
func (f *fake) List(context.Context) ([]Box, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.boxes, nil
}
func (f *fake) Up(context.Context, Options) (Result, error) { return nil, nil }
func (f *fake) Terminate(context.Context, string) error     { return nil }
func (f *fake) Volumes(context.Context) ([]Volume, error)   { return f.vols, nil }
func (f *fake) CreateVolume(context.Context, string, int, string) (*Volume, error) {
	return nil, nil
}
func (f *fake) DeleteVolume(context.Context, string) error { return nil }

func boxes(names ...string) []Box {
	out := make([]Box, 0, len(names))
	for i, n := range names {
		out = append(out, Box{ID: "id" + string(rune('0'+i)), Name: n})
	}
	return out
}

// TestFindAcceptsTheBareName is CONSTRAINTS C1: the megh- prefix is an internal
// marker, so a lookup must resolve the name the user actually typed. This is the
// half that used to live in runpod.Find; it moved here precisely so a second
// backend cannot reimplement it and drift.
func TestFindAcceptsTheBareName(t *testing.T) {
	p := &fake{name: "fake", boxes: boxes("megh-work", "megh-spare")}
	for _, q := range []string{"work", "megh-work", "id0"} {
		got, err := Find(context.Background(), p, q)
		if err != nil {
			t.Fatalf("Find(%q): %v", q, err)
		}
		if got.Name != "megh-work" {
			t.Errorf("Find(%q) = %q, want megh-work", q, got.Name)
		}
	}
}

// A box without the marker is not megh's, whatever it is called. Without this
// the prefix would stop being a filter and `megh down` could terminate a pod
// megh never created.
func TestFindIgnoresUnmanagedBoxes(t *testing.T) {
	p := &fake{name: "fake", boxes: boxes("work", "someone-elses-pod")}
	if _, err := Find(context.Background(), p, "work"); err == nil {
		t.Fatal("Find resolved an unprefixed box; the megh- marker is not being enforced")
	}
}

func TestFindReportsAmbiguityWithIDs(t *testing.T) {
	// Two boxes whose bare names collide: one stored bare-prefixed, one whose
	// full name equals the other's display name is impossible, so use ids.
	p := &fake{name: "fake", boxes: []Box{
		{ID: "a", Name: "megh-work"},
		{ID: "b", Name: "megh-work"},
	}}
	_, err := Find(context.Background(), p, "work")
	if err == nil {
		t.Fatal("want an ambiguity error")
	}
	for _, want := range []string{"ambiguous", "a", "b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q; the user cannot pick an id from it", err, want)
		}
	}
}

func TestSoleNeedsExactlyOneBox(t *testing.T) {
	ctx := context.Background()
	if _, err := Sole(ctx, &fake{name: "f"}); err == nil {
		t.Error("Sole with no boxes should error")
	}
	if _, err := Sole(ctx, &fake{name: "f", boxes: boxes("megh-a", "megh-b")}); err == nil {
		t.Error("Sole with two boxes should error")
	}
	got, err := Sole(ctx, &fake{name: "f", boxes: boxes("megh-only")})
	if err != nil || got.DisplayName() != "only" {
		t.Errorf("Sole with one box = %v, %v; want the bare name 'only'", got, err)
	}
}

func TestManagedKeepsOnlyPrefixed(t *testing.T) {
	got := Managed(boxes("megh-a", "foreign", "megh-b"))
	if len(got) != 2 {
		t.Fatalf("Managed kept %d of 3, want 2", len(got))
	}
	for _, b := range got {
		if !strings.HasPrefix(b.Name, NamePrefix) {
			t.Errorf("Managed kept %q, which has no megh- marker", b.Name)
		}
	}
}

func TestPrefixNameIsIdempotent(t *testing.T) {
	if got := PrefixName("work"); got != "megh-work" {
		t.Errorf("PrefixName(work) = %q", got)
	}
	if got := PrefixName("megh-work"); got != "megh-work" {
		t.Errorf("PrefixName is not idempotent: %q", got)
	}
}

// The old dispatch said "provider %q not implemented yet" for every miss, which
// told the user nothing. An unknown name is nearly always a typo or a backend
// that only exists on a newer binary, so the error names what IS registered.
func TestForNamesTheAvailableProviders(t *testing.T) {
	withRegistry(t, func() {
		Register(&fake{name: "alpha"})
		Register(&fake{name: "beta"})
		_, err := For("alpah")
		if err == nil {
			t.Fatal("want an error for an unknown provider")
		}
		for _, want := range []string{"alpah", "alpha", "beta"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})
}

func TestRegisterRejectsADuplicate(t *testing.T) {
	withRegistry(t, func() {
		Register(&fake{name: "dup"})
		defer func() {
			if recover() == nil {
				t.Error("registering the same name twice should panic, not silently overwrite")
			}
		}()
		Register(&fake{name: "dup"})
	})
}

// ListAll must return what it CAN reach. A user with no RUNPOD_API_KEY still has
// working local boxes, and failing the whole listing on one backend's error
// would hide them.
func TestListAllReturnsPartialResultsAndErrors(t *testing.T) {
	withRegistry(t, func() {
		Register(&fake{name: "broken", listErr: errors.New("no credential")})
		Register(&fake{name: "working", boxes: boxes("megh-local1")})
		got, errs := ListAll(context.Background())
		if len(got) != 1 || got[0].Name != "megh-local1" {
			t.Errorf("ListAll boxes = %v, want the one reachable box", got)
		}
		if len(errs) != 1 {
			t.Fatalf("ListAll errs = %v, want the one backend failure surfaced", errs)
		}
	})
}

// withRegistry runs fn against an empty registry and restores the real one, so
// these tests neither see nor disturb whatever cmd/ registered.
func withRegistry(t *testing.T, fn func()) {
	t.Helper()
	mu.Lock()
	saved := providers
	providers = map[string]Provider{}
	mu.Unlock()
	defer func() {
		mu.Lock()
		providers = saved
		mu.Unlock()
	}()
	fn()
}
