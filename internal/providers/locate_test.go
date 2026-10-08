package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func locate(ps []Provider, args ...string) (Provider, *Box, error) {
	return Locate(context.Background(), ps, args)
}

func TestLocateFindsTheBoxOnWhicheverBackendHasIt(t *testing.T) {
	rp := &fake{name: "runpod", boxes: boxes("megh-pod")}
	vu := &fake{name: "vultr", boxes: boxes("megh-dev")}
	p, b, err := locate([]Provider{rp, vu}, "dev")
	if err != nil || p.Name() != "vultr" || b.Name != "megh-dev" {
		t.Fatalf("got %v %v %v", p, b, err)
	}
}

// A name on two backends is refused rather than guessed, because the caller
// may be about to terminate it.
func TestLocateRefusesANameOnTwoBackends(t *testing.T) {
	rp := &fake{name: "runpod", boxes: boxes("megh-dev")}
	vu := &fake{name: "vultr", boxes: boxes("megh-dev")}
	_, _, err := locate([]Provider{rp, vu}, "dev")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || !strings.Contains(err.Error(), "runpod") || !strings.Contains(err.Error(), "vultr") {
		t.Fatalf("got %v", err)
	}
}

func TestLocateSkipsBackendsWithNoCredential(t *testing.T) {
	none := &fake{name: "hetzner", listErr: fmt.Errorf("%w: no token", ErrNotConfigured)}
	vu := &fake{name: "vultr", boxes: boxes("megh-dev")}
	if p, _, err := locate([]Provider{none, vu}, "dev"); err != nil || p.Name() != "vultr" {
		t.Fatalf("got %v %v", p, err)
	}
}

// A failing backend does not matter when another one has the box, and does
// when none does: "not found" would then be a guess.
func TestLocateReportsABackendFailureOnlyWhenTheBoxIsNotFound(t *testing.T) {
	down := &fake{name: "runpod", listErr: errors.New("runpod: HTTP 500")}
	vu := &fake{name: "vultr", boxes: boxes("megh-dev")}
	if p, _, err := locate([]Provider{down, vu}, "dev"); err != nil || p.Name() != "vultr" {
		t.Fatalf("found elsewhere: got %v %v", p, err)
	}
	_, _, err := locate([]Provider{down, vu}, "nope")
	var nf *NotFoundError
	if err == nil || errors.As(err, &nf) || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("not found with a backend down must surface the failure, got %v", err)
	}
}

func TestLocateNotFoundNamesWhatItSearched(t *testing.T) {
	rp := &fake{name: "runpod"}
	vu := &fake{name: "vultr", boxes: boxes("megh-pod")}
	_, _, err := locate([]Provider{rp, vu}, "dev")
	var nf *NotFoundError
	if !errors.As(err, &nf) || strings.Join(nf.Searched, ",") != "runpod,vultr" {
		t.Fatalf("got %v", err)
	}
	none := &fake{name: "hetzner", listErr: ErrNotConfigured}
	_, _, err = locate([]Provider{none}, "dev")
	if !errors.As(err, &nf) || len(nf.Searched) != 0 {
		t.Fatalf("no configured backend: got %v", err)
	}
}

func TestLocateWithNoNameNeedsExactlyOneBoxAcrossBackends(t *testing.T) {
	rp := &fake{name: "runpod"}
	vu := &fake{name: "vultr", boxes: boxes("megh-dev")}
	if p, b, err := locate([]Provider{rp, vu}); err != nil || p.Name() != "vultr" || b.Name != "megh-dev" {
		t.Fatalf("one box: got %v %v %v", p, b, err)
	}
	rp.boxes = boxes("megh-pod")
	if _, _, err := locate([]Provider{rp, vu}); err == nil || !strings.Contains(err.Error(), "2 boxes") {
		t.Fatalf("two boxes: got %v", err)
	}
}
