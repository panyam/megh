package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/panyam/megh/internal/providers"
)

const apiBase = "https://api.hetzner.cloud/v1"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// client is a minimal Hetzner Cloud REST client: just the calls megh makes.
type client struct {
	base  string
	token string
}

// apiError is Hetzner's error body ({"error":{"code","message"}}).
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("hetzner: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	if c.token == "" {
		return fmt.Errorf("%w: hetzner has no API token (set HCLOUD_TOKEN, or providers.hetzner.api_key_env in megh.yaml)", providers.ErrNotConfigured)
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &apiError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Wire types: only the fields megh reads.

type price struct {
	Location    string `json:"location"`
	PriceHourly struct {
		Gross string `json:"gross"`
	} `json:"price_hourly"`
}

type serverType struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Cores        int     `json:"cores"`
	Memory       float64 `json:"memory"`
	Disk         int     `json:"disk"`
	Architecture string  `json:"architecture"`
	Deprecation  any     `json:"deprecation"`
	Prices       []price `json:"prices"`
}

type location struct {
	Name string `json:"name"`
}

type server struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	Labels     map[string]string `json:"labels"`
	ServerType serverType        `json:"server_type"`
	Datacenter struct {
		Location location `json:"location"`
	} `json:"datacenter"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
}

type volume struct {
	ID       int64             `json:"id"`
	Name     string            `json:"name"`
	Size     int               `json:"size"`
	Location location          `json:"location"`
	Labels   map[string]string `json:"labels"`
}

type pagination struct {
	Meta struct {
		Pagination struct {
			NextPage *int `json:"next_page"`
		} `json:"pagination"`
	} `json:"meta"`
}
