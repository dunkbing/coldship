// Package client is the Go client for the Coldship HTTP API, shared by the CLI
// and the MCP server.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/api"
	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/offsite"
	"github.com/dunkbing/coldship/internal/store"
	"github.com/dunkbing/coldship/internal/update"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	// Apply may build an image from Git (bounded by the server's build
	// timeout, 20 minutes by default), so allow plenty of time.
	hc := &http.Client{Timeout: 45 * time.Minute}
	// unix:///path/api.sock talks to the server's Unix socket (how agent
	// containers reach it).
	if sock, ok := strings.CutPrefix(baseURL, "unix://"); ok {
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		baseURL = "http://coldship"
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: hc}
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
		return apierr.New(503, "server_unreachable", "cannot reach Coldship at %s: %v", c.BaseURL, err).
			WithHint("start it with `coldship server` or set COLDSHIP_URL")
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

func (c *Client) DeployLog(ctx context.Context, deployment string) (*engine.DeployLog, error) {
	var out engine.DeployLog
	return &out, c.do(ctx, "GET", "/v1/deployments/"+esc(deployment)+"/log", nil, &out)
}

func (c *Client) WebhookStatus(ctx context.Context, app string) (*api.WebhookInfo, error) {
	var out api.WebhookInfo
	return &out, c.do(ctx, "GET", "/v1/apps/"+esc(app)+"/webhook", nil, &out)
}

func (c *Client) EnableWebhook(ctx context.Context, app string, rotate bool) (*api.WebhookInfo, error) {
	var out api.WebhookInfo
	return &out, c.do(ctx, "POST", "/v1/apps/"+esc(app)+"/webhook", api.EnableWebhookRequest{Rotate: rotate}, &out)
}

func (c *Client) DisableWebhook(ctx context.Context, app string) error {
	return c.do(ctx, "DELETE", "/v1/apps/"+esc(app)+"/webhook", nil, nil)
}

func (c *Client) Databases(ctx context.Context, app string) ([]engine.DatabaseView, error) {
	var out []engine.DatabaseView
	return out, c.do(ctx, "GET", "/v1/apps/"+esc(app)+"/databases", nil, &out)
}

func (c *Client) Snapshots(ctx context.Context, app, database string) ([]store.Snapshot, error) {
	var out []store.Snapshot
	return out, c.do(ctx, "GET", "/v1/apps/"+esc(app)+"/snapshots?database="+url.QueryEscape(database), nil, &out)
}

func (c *Client) Snapshot(ctx context.Context, app, database, reason string) (*store.Snapshot, error) {
	var out store.Snapshot
	return &out, c.do(ctx, "POST", "/v1/apps/"+esc(app)+"/databases/"+esc(database)+"/snapshots", api.SnapshotRequest{Reason: reason}, &out)
}

func (c *Client) DetectBuild(ctx context.Context, req engine.DetectRequest) (*engine.DetectResult, error) {
	var out engine.DetectResult
	return &out, c.do(ctx, "POST", "/v1/build/detect", req, &out)
}

func (c *Client) CreatePreview(ctx context.Context, req engine.PreviewRequest) (*engine.PreviewResult, error) {
	var out engine.PreviewResult
	return &out, c.do(ctx, "POST", "/v1/previews", req, &out)
}

func (c *Client) ListPreviews(ctx context.Context) ([]store.App, error) {
	var out []store.App
	return out, c.do(ctx, "GET", "/v1/previews", nil, &out)
}

func (c *Client) DeletePreview(ctx context.Context, name, reason string) (*engine.ApplyResult, error) {
	var out engine.ApplyResult
	return &out, c.do(ctx, "DELETE", "/v1/previews/"+esc(name)+"?reason="+url.QueryEscape(reason), nil, &out)
}

func (c *Client) SecretsStatus(ctx context.Context) (*store.SecretsStatus, error) {
	var out store.SecretsStatus
	return &out, c.do(ctx, "GET", "/v1/system/secrets", nil, &out)
}

func (c *Client) RotateSecretKey(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "POST", "/v1/system/secrets/rotate", nil, &out)
}

// SystemView is System plus the container engine and proxy.
func (c *Client) SystemView(ctx context.Context) (*api.SystemView, error) {
	var out api.SystemView
	return &out, c.do(ctx, "GET", "/v1/system", nil, &out)
}

func (c *Client) System(ctx context.Context) (*update.Status, error) {
	var out update.Status
	return &out, c.do(ctx, "GET", "/v1/system", nil, &out)
}

func (c *Client) SystemCheck(ctx context.Context) (*update.Status, error) {
	var out update.Status
	return &out, c.do(ctx, "POST", "/v1/system/check", nil, &out)
}

func (c *Client) SystemUpgrade(ctx context.Context) (*update.Status, error) {
	var out update.Status
	return &out, c.do(ctx, "POST", "/v1/system/upgrade", nil, &out)
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

func (c *Client) ListTokens(ctx context.Context) ([]store.Token, error) {
	var out []store.Token
	return out, c.do(ctx, "GET", "/v1/tokens", nil, &out)
}

func (c *Client) RevokeToken(ctx context.Context, name, reason string) error {
	return c.do(ctx, "DELETE", "/v1/tokens/"+url.PathEscape(name)+"?reason="+url.QueryEscape(reason), nil, nil)
}

// CreateToken creates a token, optionally limited to apps (names or patterns
// like shop-*).
func (c *Client) CreateToken(ctx context.Context, name, scope string, apps ...string) (*api.TokenResponse, error) {
	var out api.TokenResponse
	return &out, c.do(ctx, "POST", "/v1/tokens", api.TokenRequest{Name: name, Scope: scope, Apps: apps}, &out)
}

// ServerStatus is the host's CPU, memory, disks and container engine
// (default organization only).
func (c *Client) ServerStatus(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, "GET", "/v1/system/status", nil, &out)
}

// CreateInvite makes a single-use link that adds someone to the token's
// organization with role.
func (c *Client) CreateInvite(ctx context.Context, role string) (*api.LinkView, error) {
	var out api.LinkView
	return &out, c.do(ctx, "POST", "/v1/org/invites", api.RoleRequest{Role: role}, &out)
}

// --- data services ---

func (c *Client) ListData(ctx context.Context) ([]engine.DataView, error) {
	var out []engine.DataView
	return out, c.do(ctx, "GET", "/v1/data", nil, &out)
}

func (c *Client) CreateData(ctx context.Context, req engine.DataRequest) (*engine.DataView, error) {
	var out engine.DataView
	return &out, c.do(ctx, "POST", "/v1/data", req, &out)
}

func (c *Client) GetData(ctx context.Context, name string) (*engine.DataView, error) {
	var out engine.DataView
	return &out, c.do(ctx, "GET", "/v1/data/"+url.PathEscape(name), nil, &out)
}

func (c *Client) DeleteData(ctx context.Context, name, reason string) error {
	return c.do(ctx, "DELETE", "/v1/data/"+url.PathEscape(name)+"?reason="+url.QueryEscape(reason), nil, nil)
}

func (c *Client) DataConnection(ctx context.Context, name string) (map[string]string, error) {
	var out map[string]string
	return out, c.do(ctx, "POST", "/v1/data/"+url.PathEscape(name)+"/connection", nil, &out)
}

func (c *Client) DataSnapshots(ctx context.Context, name string) ([]store.Snapshot, error) {
	var out []store.Snapshot
	return out, c.do(ctx, "GET", "/v1/data/"+url.PathEscape(name)+"/snapshots", nil, &out)
}

func (c *Client) SnapshotData(ctx context.Context, name, reason string) (*store.Snapshot, error) {
	var out store.Snapshot
	return &out, c.do(ctx, "POST", "/v1/data/"+url.PathEscape(name)+"/snapshots", api.DataSnapshotRequest{Reason: reason}, &out)
}

func (c *Client) RestoreData(ctx context.Context, name, snapshot, reason string) (string, error) {
	var out struct {
		Message string `json:"message"`
	}
	err := c.do(ctx, "POST", "/v1/data/"+url.PathEscape(name)+"/restore", api.DataRestoreRequest{Snapshot: snapshot, Reason: reason}, &out)
	return out.Message, err
}

func (c *Client) RestoreDataBackup(ctx context.Context, name, backup, reason string) (string, error) {
	var out struct {
		Message string `json:"message"`
	}
	err := c.do(ctx, "POST", "/v1/data/"+url.PathEscape(name)+"/restore", api.DataRestoreRequest{Backup: backup, Reason: reason}, &out)
	return out.Message, err
}

func (c *Client) BackupStatus(ctx context.Context) (*engine.BackupView, error) {
	var out engine.BackupView
	return &out, c.do(ctx, "GET", "/v1/backups", nil, &out)
}

func (c *Client) SaveBackup(ctx context.Context, cfg offsite.Config) (*engine.BackupView, error) {
	var out engine.BackupView
	return &out, c.do(ctx, "PUT", "/v1/backups", cfg, &out)
}

func (c *Client) DeleteBackup(ctx context.Context) error {
	return c.do(ctx, "DELETE", "/v1/backups", nil, nil)
}

func (c *Client) SyncBackups(ctx context.Context) (*engine.BackupView, error) {
	var out engine.BackupView
	return &out, c.do(ctx, "POST", "/v1/backups/sync", nil, &out)
}

// Backups lists off-site backups: of one database when given (app "" for
// a data service), else all.
func (c *Client) Backups(ctx context.Context, app, database string) ([]engine.Backup, error) {
	var out []engine.Backup
	q := url.Values{}
	if app != "" {
		q.Set("app", app)
	}
	if database != "" {
		q.Set("database", database)
	}
	return out, c.do(ctx, "GET", "/v1/backups/list?"+q.Encode(), nil, &out)
}

func (c *Client) ImportBackup(ctx context.Context, app, database, backup string) (*store.Snapshot, error) {
	var out store.Snapshot
	return &out, c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/databases/"+url.PathEscape(database)+"/import",
		map[string]string{"backup": backup}, &out)
}

// FollowDeployLog calls line for each line of a deployment's log as it is
// written, until the deployment finishes, and returns its final status.
func (c *Client) FollowDeployLog(ctx context.Context, deployment string, line func(string)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/v1/deployments/"+url.PathEscape(deployment)+"/log/stream", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", apierr.New(503, "server_unreachable", "cannot reach Coldship at %s: %v", c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var env struct {
			Error *apierr.Error `json:"error"`
		}
		data, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(data, &env) == nil && env.Error != nil {
			return "", env.Error
		}
		return "", apierr.New(resp.StatusCode, "http_error", "following the deploy log: %s", strings.TrimSpace(string(data)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	event := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "event: "):
			event = strings.TrimPrefix(l, "event: ")
		case strings.HasPrefix(l, "data: "):
			data := strings.TrimPrefix(l, "data: ")
			if event == "end" {
				return data, nil
			}
			line(data)
		case l == "":
			event = ""
		}
	}
	return "", sc.Err()
}
