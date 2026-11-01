// Package client is the Go client for the Hangar HTTP API, shared by the CLI
// and the MCP server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hangar/internal/api"
	"hangar/internal/apierr"
	"hangar/internal/engine"
	"hangar/internal/store"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	// Apply waits for the container's startup grace period, so allow time.
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return apierr.New(503, "server_unreachable", "cannot reach Hangar at %s: %v", c.BaseURL, err).
			WithHint("start it with `hangar server` or set HANGAR_URL")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var env struct {
			Error *apierr.Error `json:"error"`
		}
		if json.Unmarshal(data, &env) == nil && env.Error != nil {
			env.Error.Status = resp.StatusCode
			return env.Error
		}
		return apierr.New(resp.StatusCode, "http_error", "%s %s: %s", method, path, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func esc(s string) string { return url.PathEscape(s) }

func (c *Client) ListApps(ctx context.Context) ([]store.App, error) {
	var out []store.App
	return out, c.do(ctx, "GET", "/v1/apps", nil, &out)
}

func (c *Client) GetApp(ctx context.Context, name string) (*engine.AppView, error) {
	var out engine.AppView
	return &out, c.do(ctx, "GET", "/v1/apps/"+esc(name), nil, &out)
}

func (c *Client) Plan(ctx context.Context, req engine.PlanRequest) (*store.Plan, error) {
	var out store.Plan
	return &out, c.do(ctx, "POST", "/v1/plans", req, &out)
}

func (c *Client) GetPlan(ctx context.Context, id string) (*store.Plan, error) {
	var out store.Plan
	return &out, c.do(ctx, "GET", "/v1/plans/"+esc(id), nil, &out)
}

func (c *Client) Apply(ctx context.Context, id, reason string) (*engine.ApplyResult, error) {
	var out engine.ApplyResult
	return &out, c.do(ctx, "POST", "/v1/plans/"+esc(id)+"/apply", api.ApplyRequest{Reason: reason}, &out)
}

func (c *Client) Approve(ctx context.Context, id string) (*store.Plan, error) {
	var out store.Plan
	return &out, c.do(ctx, "POST", "/v1/plans/"+esc(id)+"/approve", nil, &out)
}

func (c *Client) Rollback(ctx context.Context, app, to, reason string) (*engine.ApplyResult, error) {
	var out engine.ApplyResult
	return &out, c.do(ctx, "POST", "/v1/apps/"+esc(app)+"/rollback", api.RollbackRequest{Reason: reason, ToDeployment: to}, &out)
}

func (c *Client) Logs(ctx context.Context, app string, tail int, since string) (*engine.LogsResult, error) {
	q := url.Values{}
	if tail > 0 {
		q.Set("tail", fmt.Sprint(tail))
	}
	if since != "" {
		q.Set("since", since)
	}
	var out engine.LogsResult
	return &out, c.do(ctx, "GET", "/v1/apps/"+esc(app)+"/logs?"+q.Encode(), nil, &out)
}

func (c *Client) Diagnose(ctx context.Context, app string) (*engine.Diagnosis, error) {
	var out engine.Diagnosis
	return &out, c.do(ctx, "GET", "/v1/apps/"+esc(app)+"/diagnose", nil, &out)
}

func (c *Client) Deployments(ctx context.Context, app string, limit int) ([]store.Deployment, error) {
	var out []store.Deployment
	return out, c.do(ctx, "GET", fmt.Sprintf("/v1/apps/%s/deployments?limit=%d", esc(app), limit), nil, &out)
}

func (c *Client) WhoAmI(ctx context.Context) (*api.WhoAmI, error) {
	var out api.WhoAmI
	return &out, c.do(ctx, "GET", "/v1/whoami", nil, &out)
}

func (c *Client) ListPlans(ctx context.Context, status string, limit int) ([]store.Plan, error) {
	var out []store.Plan
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if status != "" {
		q.Set("status", status)
	}
	return out, c.do(ctx, "GET", "/v1/plans?"+q.Encode(), nil, &out)
}

func (c *Client) Audit(ctx context.Context, limit int) ([]store.AuditEntry, error) {
	var out []store.AuditEntry
	return out, c.do(ctx, "GET", fmt.Sprintf("/v1/audit?limit=%d", limit), nil, &out)
}

func (c *Client) CreateToken(ctx context.Context, name, scope string) (*api.TokenResponse, error) {
	var out api.TokenResponse
	return &out, c.do(ctx, "POST", "/v1/tokens", api.TokenRequest{Name: name, Scope: scope}, &out)
}
