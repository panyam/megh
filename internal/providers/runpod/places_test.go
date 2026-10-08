package runpod

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RunPod's API gives only the country, so the state comes from the id: US
// data center ids carry a state code. An id it cannot decode keeps the country.
func TestPlaceDecodesTheUSStateFromTheID(t *testing.T) {
	for id, want := range map[string]string{
		"US-IL-1": "Illinois, US", "US-TX-7": "Texas, US", "US-WA-2": "Washington, US",
		"US-ZZ-1": "United States", "AP-JP-1": "Japan", "CA-MTL-1": "Canada",
	} {
		country := map[string]string{"US": "United States", "AP": "Japan", "CA": "Canada"}[strings.SplitN(id, "-", 2)[0]]
		if got := place(id, country); got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
}

func TestPlacesAsksGraphQLOnceWithTheKeyInAHeader(t *testing.T) {
	var calls int
	var auth, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		query = string(b)
		io.WriteString(w, `{"data":{"dataCenters":[{"id":"US-IL-1","location":"United States"},{"id":"AP-JP-1","location":"Japan"}]}}`)
	}))
	t.Cleanup(srv.Close)
	saved := graphqlEndpoint
	graphqlEndpoint = srv.URL
	t.Cleanup(func() { graphqlEndpoint = saved })

	p := NewWithKey("rpa_test")
	for i := 0; i < 2; i++ {
		got, err := p.Places(context.Background())
		if err != nil || got["US-IL-1"] != "Illinois, US" || got["AP-JP-1"] != "Japan" {
			t.Fatalf("got %v %v", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("asked %d times, want once (cached)", calls)
	}
	if auth != "Bearer rpa_test" || strings.Contains(query, "rpa_test") {
		t.Errorf("key must travel in the header only: auth=%q query=%q", auth, query)
	}
}
