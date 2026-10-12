// Package blocky is a small client for blocky's REST API.
package blocky

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errStatsDisabled is returned by Stats when blocky runs without statistics.enable.
var errStatsDisabled = errors.New("statistics disabled (statistics.enable)")

type Client struct {
	base *url.URL
	http *http.Client
}

func New(baseURL string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse blocky url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("blocky url %q needs scheme and host", baseURL)
	}
	// No client timeout: callers bound each request with their context, and a
	// list refresh can legitimately take minutes.
	return &Client{base: u, http: &http.Client{}}, nil
}

// Host is the host name to show users, without port or credentials.
func (c *Client) Host() string { return c.base.Hostname() }

// BlockingStatus mirrors api.BlockingStatus.
type BlockingStatus struct {
	Enabled         bool     `json:"enabled"`
	DisabledGroups  []string `json:"disabledGroups,omitempty"`
	AutoEnableInSec int      `json:"autoEnableInSec,omitempty"`
}

// Stats is the part of api.Stats (rolling 24h window) the UI shows.
type Stats struct {
	Start   time.Time `json:"start"`
	Summary struct {
		Queries       int `json:"queries"`
		Blocked       int `json:"blocked"`
		Cached        int `json:"cached"`
		AvgResponseMs int `json:"avgResponseMs"`
	} `json:"summary"`
	PerHour []struct {
		Hour    time.Time `json:"hour"`
		Queries int       `json:"queries"`
		Blocked int       `json:"blocked"`
	} `json:"perHour"`
	TopBlockedDomains []NameCount `json:"topBlockedDomains"`
	TopClients        []NameCount `json:"topClients"`
	Lists             struct {
		Denylist  map[string]int `json:"denylist"`
		Allowlist map[string]int `json:"allowlist"`
	} `json:"lists"`
}

type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (c *Client) BlockingStatus(ctx context.Context) (BlockingStatus, error) {
	var s BlockingStatus
	err := c.do(ctx, http.MethodGet, "/api/blocking/status", nil, &s)
	return s, err
}

func (c *Client) EnableBlocking(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/blocking/enable", nil, nil)
}

// DisableBlocking disables blocking. A zero duration disables until re-enabled;
// no groups disables all groups.
func (c *Client) DisableBlocking(ctx context.Context, d time.Duration, groups []string) error {
	q := url.Values{}
	if d > 0 {
		q.Set("duration", d.String())
	}
	if len(groups) > 0 {
		q.Set("groups", strings.Join(groups, ","))
	}
	return c.do(ctx, http.MethodGet, "/api/blocking/disable", q, nil)
}

func (c *Client) RefreshLists(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/lists/refresh", nil, nil)
}

func (c *Client) FlushCache(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/cache/flush", nil, nil)
}

func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := c.do(ctx, http.MethodGet, "/api/stats", nil, &s)
	return s, err
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, out any) error {
	u := c.base.JoinPath(path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL, it may hold credentials
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable && path == "/api/stats" {
		return errStatsDisabled
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("blocky: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
