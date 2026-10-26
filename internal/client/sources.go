package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Source is where facts come from: a pull connector (github, terraform_state, pulse_admin, …), a push source or a
// system source. Secrets are never returned; SecretFieldsSet lists which ones have a stored value.
type Source struct {
	ID                      string          `json:"id"`
	ProjectID               string          `json:"project_id"`
	Name                    string          `json:"name"`
	DisplayName             string          `json:"display_name"`
	Provider                string          `json:"provider"`
	ProviderTitle           string          `json:"provider_title"`
	Layer                   string          `json:"layer"`
	System                  bool            `json:"system"`
	Enabled                 bool            `json:"enabled"`
	Config                  json.RawMessage `json:"config"`
	HasSecrets              bool            `json:"has_secrets"`
	SecretFieldsSet         []string        `json:"secret_fields_set"`
	Policy                  json.RawMessage `json:"policy"`
	ScheduleSeconds         int64           `json:"schedule_seconds"`
	ExpectedIntervalSeconds int64           `json:"expected_interval_seconds"`
	Pull                    bool            `json:"pull"`
	Push                    bool            `json:"push"`
	Running                 bool            `json:"running"`
	Freshness               string          `json:"freshness"`
	LastGeneration          int64           `json:"last_generation"`
	LastRunAt               *time.Time      `json:"last_run_at,omitempty"`
	LastSuccessAt           *time.Time      `json:"last_success_at,omitempty"`
	LastHeartbeatAt         *time.Time      `json:"last_heartbeat_at,omitempty"`
	LastError               string          `json:"last_error,omitempty"`
	NextRunAt               *time.Time      `json:"next_run_at,omitempty"`
	ResourceVersion         int64           `json:"resource_version"`
	ETag                    string          `json:"-"`
}

// SourceInput creates a source (Project, Name and Provider required) or changes it (PATCH: empty or nil fields are
// kept; Secrets nil keeps the stored credentials, an empty map clears them).
type SourceInput struct {
	Project                 string            `json:"project,omitempty"`
	Name                    string            `json:"name,omitempty"`
	DisplayName             string            `json:"display_name,omitempty"`
	Provider                string            `json:"provider,omitempty"`
	Layer                   string            `json:"layer,omitempty"`
	Enabled                 *bool             `json:"enabled,omitempty"`
	Config                  json.RawMessage   `json:"config,omitempty"`
	Secrets                 map[string]string `json:"secrets,omitempty"`
	Policy                  json.RawMessage   `json:"policy,omitempty"`
	ScheduleSeconds         *int64            `json:"schedule_seconds,omitempty"`
	ExpectedIntervalSeconds int64             `json:"expected_interval_seconds,omitempty"`
}

// MarshalJSON keeps an empty, non-nil Secrets map (which clears the credentials) instead of omitting it.
func (in SourceInput) MarshalJSON() ([]byte, error) {
	type plain SourceInput
	b, err := json.Marshal(plain(in))
	if err != nil || in.Secrets == nil || len(in.Secrets) > 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["secrets"] = json.RawMessage(`{}`)
	return json.Marshal(m)
}

func sourceETag(s *Source, e string) { s.ETag = e }

// CreateSource creates a source (project admin).
func (c *Client) CreateSource(ctx context.Context, in SourceInput) (*Source, error) {
	out, resp, err := call[Source](ctx, c, &request{method: http.MethodPost, path: "/sources", body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, sourceETag), nil
}

// GetSource reads a source by ID.
func (c *Client) GetSource(ctx context.Context, id string) (*Source, error) {
	out, resp, err := call[Source](ctx, c, &request{method: http.MethodGet, path: p("sources", id)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, sourceETag), nil
}

// FindSource returns the source named name in project (a 404 APIError when there is none).
func (c *Client) FindSource(ctx context.Context, project, name string) (*Source, error) {
	out, _, err := call[[]Source](ctx, c, &request{method: http.MethodGet, path: "/sources", query: url.Values{"project": {project}}})
	if err != nil {
		return nil, err
	}
	for _, s := range *out {
		if s.Name == name {
			return c.GetSource(ctx, s.ID)
		}
	}
	return nil, notFound("/sources", "source "+project+"/"+name)
}

// UpdateSource changes a source (If-Match etag when non-empty).
func (c *Client) UpdateSource(ctx context.Context, id, etag string, in SourceInput) (*Source, error) {
	out, resp, err := call[Source](ctx, c, &request{method: http.MethodPatch, path: p("sources", id), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, sourceETag), nil
}

// DeleteSource deletes a source and every fact it owns.
func (c *Client) DeleteSource(ctx context.Context, id string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("sources", id)})
	return err
}
