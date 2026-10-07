package runpod

import (
	"errors"
	"strings"
	"testing"

	"github.com/panyam/megh/internal/providers"
)

// The reply RunPod sent for a 4 vCPU launch into a full data center: one line
// per CPU flavor tried, as an HTTP 500.
var noCapacityBody = []byte(`{"error":"create pod: There are no longer any instances available with the requested specifications. Please refresh and try again.\nThere are no longer any instances available with the requested specifications. Please refresh and try again.\nThere are no longer any instances available with the requested specifications. Please refresh and try again.","status":500}`)

func TestCreateErrorNamesACapacityGap(t *testing.T) {
	err := createError(500, noCapacityBody, providers.Options{VCPU: 4, RAMGiB: 16, DataCenter: "US-IL-1"})
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("want ErrNoCapacity, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"4 vCPU", "US-IL-1", "all 6 CPU types", "smaller box", "megh regions probe --dc US-IL-1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "Please refresh") {
		t.Errorf("the repeated raw lines should not reach the user: %s", msg)
	}
}

// At the smallest size there is no smaller box to suggest; the way out is
// another data center.
func TestCreateErrorAtTheSmallestSizePointsElsewhere(t *testing.T) {
	msg := createError(500, noCapacityBody, providers.Options{VCPU: 2, RAMGiB: 8, DataCenter: "US-IL-1"}).Error()
	if strings.Contains(msg, "smaller box") {
		t.Errorf("no smaller box exists at 2 vCPU: %s", msg)
	}
	for _, want := range []string{"2 vCPU", "megh regions probe", "megh regions place"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

func TestCreateErrorKeepsOtherFailuresRaw(t *testing.T) {
	err := createError(401, []byte(`{"error":"unauthorized"}`), providers.Options{VCPU: 4})
	if errors.Is(err, ErrNoCapacity) || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("got %v", err)
	}
}
