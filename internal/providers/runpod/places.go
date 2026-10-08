package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/megh/internal/providers"
)

// graphqlEndpoint is RunPod's GraphQL API, the only one that describes data
// centers. Overridable for tests.
var graphqlEndpoint = "https://api.runpod.io/graphql"

var _ providers.Placer = (*Provider)(nil)

// Places names every RunPod data center. RunPod's API reports only the country
// ("United States" for all of US-*), so a US id's state code, its second
// field, supplies the state: "US-IL-1" -> "Illinois, US". Asked once per
// process; the key travels in the Authorization header, never the URL.
func (p *Provider) Places(ctx context.Context) (map[string]string, error) {
	p.placesMu.Lock()
	defer p.placesMu.Unlock()
	if p.places != nil {
		return p.places, nil
	}
	var out struct {
		Data struct {
			DataCenters []struct {
				ID       string `json:"id"`
				Location string `json:"location"`
			} `json:"dataCenters"`
		} `json:"data"`
	}
	if err := p.graphql(ctx, "{ dataCenters { id location } }", &out); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(out.Data.DataCenters))
	for _, dc := range out.Data.DataCenters {
		m[dc.ID] = place(dc.ID, dc.Location)
	}
	p.places = m
	return m, nil
}

// place is a data center's display name: "<State>, US" for a US id whose state
// code is known, else the country RunPod reported.
func place(id, country string) string {
	parts := strings.Split(id, "-")
	if len(parts) >= 2 && parts[0] == "US" {
		if s, ok := usStates[parts[1]]; ok {
			return s + ", US"
		}
	}
	return country
}

var usStates = map[string]string{
	"AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas", "CA": "California",
	"CO": "Colorado", "CT": "Connecticut", "DE": "Delaware", "FL": "Florida", "GA": "Georgia",
	"HI": "Hawaii", "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "IA": "Iowa",
	"KS": "Kansas", "KY": "Kentucky", "LA": "Louisiana", "ME": "Maine", "MD": "Maryland",
	"MA": "Massachusetts", "MI": "Michigan", "MN": "Minnesota", "MS": "Mississippi", "MO": "Missouri",
	"MT": "Montana", "NE": "Nebraska", "NV": "Nevada", "NH": "New Hampshire", "NJ": "New Jersey",
	"NM": "New Mexico", "NY": "New York", "NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio",
	"OK": "Oklahoma", "OR": "Oregon", "PA": "Pennsylvania", "RI": "Rhode Island", "SC": "South Carolina",
	"SD": "South Dakota", "TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VT": "Vermont",
	"VA": "Virginia", "WA": "Washington", "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming",
	"DC": "District of Columbia",
}

// graphql runs one query against RunPod's GraphQL API and decodes the reply
// into out. The key travels in the Authorization header, never the URL.
func (p *Provider) graphql(ctx context.Context, query string, out any) error {
	key := keyFor(p.with(ctx))
	if key == "" {
		return fmt.Errorf("%w: runpod has no API key (set RUNPOD_API_KEY)", providers.ErrNotConfigured)
	}
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("runpod: graphql HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("runpod: graphql: %w", err)
	}
	return nil
}
