package bpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	user    string
	pass    string
	http    *http.Client
}

type Options struct {
	BaseURL       string
	APIKey        string
	User          string
	Pass          string
	SkipTLSVerify bool
	Timeout       time.Duration
}

// Agent is the subset of a Bindplane collector the labeler cares about.
type Agent struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	HostName  string            `json:"hostname"`
	Type      string            `json:"type"`
	Version   string            `json:"version"`
	Platform  string            `json:"platform"`
	Connected bool              `json:"connected"`
	Labels    map[string]string `json:"labels"`
}

// Fleet is a Bindplane fleet. Agents join a fleet through the "fleet" label,
// whose value is the fleet's resource Name (not its DisplayName).
type Fleet struct {
	Name          string
	DisplayName   string
	Configuration string
}

func (f Fleet) Title() string {
	if f.DisplayName != "" {
		return f.DisplayName
	}
	return f.Name
}

// BulkResult is the response of PATCH /v1/agents/labels.
type BulkResult struct {
	Errors    []string `json:"errors"`
	Count     int64    `json:"count"`
	FailedIDs []string `json:"failedIds,omitempty"`
}

func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("server URL required")
	}
	if opts.APIKey == "" && opts.User == "" {
		return nil, fmt.Errorf("either API key or user/pass required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if opts.SkipTLSVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	return &Client{
		baseURL: base,
		apiKey:  opts.APIKey,
		user:    opts.User,
		pass:    opts.Pass,
		http:    &http.Client{Timeout: opts.Timeout, Transport: tr},
	}, nil
}

func (c *Client) do(req *http.Request) (int, []byte, error) {
	if c.apiKey != "" {
		req.Header.Set("X-Bindplane-Api-Key", c.apiKey)
	} else {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

const pageSize = 500

// ListAgents returns every agent matching query and selector, paging until
// the server stops returning new agents. Empty query and selector list all.
func (c *Client) ListAgents(ctx context.Context, query, selector string) ([]Agent, error) {
	var out []Agent
	seen := map[string]bool{}
	for offset := 0; ; {
		q := url.Values{}
		q.Set("query", query)
		q.Set("selector", selector)
		q.Set("offset", strconv.Itoa(offset))
		q.Set("limit", strconv.Itoa(pageSize))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/agents?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		status, body, err := c.do(req)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("bindplane GET /v1/agents: %d %s", status, truncate(string(body), 500))
		}
		var page struct {
			Agents []Agent `json:"agents"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode agents: %w", err)
		}
		added := 0
		for _, a := range page.Agents {
			if !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
				added++
			}
		}
		// Stop on an empty page, or if the server ignored offset and
		// handed back agents we already have.
		if added == 0 {
			return out, nil
		}
		offset += len(page.Agents)
	}
}

// ListFleets returns every fleet.
func (c *Client) ListFleets(ctx context.Context) ([]Fleet, error) {
	var out []Fleet
	seen := map[string]bool{}
	for offset := 0; ; {
		q := url.Values{}
		q.Set("offset", strconv.Itoa(offset))
		q.Set("limit", strconv.Itoa(pageSize))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/fleets?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		status, body, err := c.do(req)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("bindplane GET /v1/fleets: %d %s", status, truncate(string(body), 500))
		}
		var page struct {
			Fleets []struct {
				Metadata struct {
					Name        string `json:"name"`
					DisplayName string `json:"displayName"`
				} `json:"metadata"`
				Spec struct {
					Configuration string `json:"configuration"`
				} `json:"spec"`
			} `json:"fleets"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode fleets: %w", err)
		}
		added := 0
		for _, f := range page.Fleets {
			if !seen[f.Metadata.Name] {
				seen[f.Metadata.Name] = true
				out = append(out, Fleet{f.Metadata.Name, f.Metadata.DisplayName, f.Spec.Configuration})
				added++
			}
		}
		if added == 0 {
			return out, nil
		}
		offset += len(page.Fleets)
	}
}

// LabelAgents applies labels to the given agent IDs. An empty value deletes
// the label. overwrite must be true when changing or deleting an existing
// label.
func (c *Client) LabelAgents(ctx context.Context, ids []string, labels map[string]string, overwrite bool) (BulkResult, error) {
	payload := map[string]any{
		"ids":              ids,
		"labels":           labels,
		"overwrite":        overwrite,
		"includeFailedIds": true,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return BulkResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/v1/agents/labels", bytes.NewReader(data))
	if err != nil {
		return BulkResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	status, body, err := c.do(req)
	if err != nil {
		return BulkResult{}, err
	}
	var res BulkResult
	jsonErr := json.Unmarshal(body, &res)
	// 409 carries a partial-failure body in the same shape.
	if status >= 400 && status != http.StatusConflict {
		return res, fmt.Errorf("bindplane PATCH /v1/agents/labels: %d %s", status, truncate(string(body), 500))
	}
	if jsonErr != nil {
		return res, fmt.Errorf("decode label response: %w", jsonErr)
	}
	if len(res.Errors) > 0 {
		return res, fmt.Errorf("%s", strings.Join(res.Errors, "; "))
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// AgentVersion is a collector version Bindplane offers for install/upgrade.
type AgentVersion struct {
	Type            string
	Version         string
	Prerelease      bool
	ReleaseNotesURL string
}

// ListAgentVersions returns the versions Bindplane has available, excluding drafts.
func (c *Client) ListAgentVersions(ctx context.Context) ([]AgentVersion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/agent-versions", nil)
	if err != nil {
		return nil, err
	}
	status, body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("bindplane GET /v1/agent-versions: %d %s", status, truncate(string(body), 500))
	}
	var resp struct {
		AgentVersions []struct {
			Spec struct {
				Type            string `json:"type"`
				Version         string `json:"version"`
				ReleaseNotesURL string `json:"releaseNotesURL"`
				Draft           bool   `json:"draft"`
				Prerelease      bool   `json:"prerelease"`
			} `json:"spec"`
		} `json:"agentVersions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode agent versions: %w", err)
	}
	var out []AgentVersion
	for _, v := range resp.AgentVersions {
		if v.Spec.Draft || v.Spec.Version == "" {
			continue
		}
		out = append(out, AgentVersion{v.Spec.Type, v.Spec.Version, v.Spec.Prerelease, v.Spec.ReleaseNotesURL})
	}
	return out, nil
}

// UpgradeAgents requests an upgrade of the given agents to version. Bindplane
// answers 204 when the request is accepted; it silently skips agents that
// don't exist or don't support upgrades.
func (c *Client) UpgradeAgents(ctx context.Context, ids []string, version string) error {
	data, err := json.Marshal(map[string]any{"ids": ids, "version": version})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/v1/agents/version", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	status, body, err := c.do(req)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("bindplane PATCH /v1/agents/version: %d %s", status, truncate(string(body), 500))
	}
	return nil
}

// Ping verifies the server and credentials with a one-agent listing.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/agents?limit=1&offset=0", nil)
	if err != nil {
		return err
	}
	status, body, err := c.do(req)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("%d %s", status, truncate(string(body), 200))
	}
	return nil
}
