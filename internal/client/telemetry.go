package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Integration is a configured telemetry backend (prometheus, loki, grafana, natsmon, opensearch). Secrets are never
// returned; HasSecrets says whether any are stored.
type Integration struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Adapter         string          `json:"adapter"`
	Config          json.RawMessage `json:"config"`
	HasSecrets      bool            `json:"has_secrets"`
	Enabled         bool            `json:"enabled"`
	LastTestAt      *time.Time      `json:"last_test_at,omitempty"`
	LastTestOK      *bool           `json:"last_test_ok,omitempty"`
	LastError       string          `json:"last_error,omitempty"`
	ResourceVersion int64           `json:"resource_version"`
	ETag            string          `json:"-"`
}

// IntegrationInput creates an integration (Name and Adapter required) or changes it (PATCH: empty fields are kept;
// Secrets nil keeps the stored credentials, an empty map clears them).
type IntegrationInput struct {
	Name    string            `json:"name,omitempty"`
	Adapter string            `json:"adapter,omitempty"`
	Config  json.RawMessage   `json:"config,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

// MarshalJSON keeps an empty, non-nil Secrets map (which clears the credentials) instead of omitting it.
func (in IntegrationInput) MarshalJSON() ([]byte, error) {
	type plain IntegrationInput
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

func integrationETag(i *Integration, e string) { i.ETag = e }

// CreateIntegration creates a telemetry integration (installation admin).
func (c *Client) CreateIntegration(ctx context.Context, in IntegrationInput) (*Integration, error) {
	out, resp, err := call[Integration](ctx, c, &request{method: http.MethodPost, path: "/integrations", body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, integrationETag), nil
}

// GetIntegration reads an integration by ID or name.
func (c *Client) GetIntegration(ctx context.Context, idOrName string) (*Integration, error) {
	out, resp, err := call[Integration](ctx, c, &request{method: http.MethodGet, path: p("integrations", idOrName)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, integrationETag), nil
}

// ListIntegrations lists every integration.
func (c *Client) ListIntegrations(ctx context.Context) ([]Integration, error) {
	out, _, err := call[[]Integration](ctx, c, &request{method: http.MethodGet, path: "/integrations"})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// UpdateIntegration changes an integration (If-Match etag when non-empty).
func (c *Client) UpdateIntegration(ctx context.Context, id, etag string, in IntegrationInput) (*Integration, error) {
	out, resp, err := call[Integration](ctx, c, &request{method: http.MethodPatch, path: p("integrations", id), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, integrationETag), nil
}

// DeleteIntegration deletes an integration.
func (c *Client) DeleteIntegration(ctx context.Context, id string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("integrations", id)})
	return err
}

// BindingParam is a typed query parameter filled from an entity field.
type BindingParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	From     string `json:"from,omitempty"`
	Default  string `json:"default,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// BindingThresholds turn a scalar into health (role health) or colour a chart.
type BindingThresholds struct {
	Warning   *float64 `json:"warning,omitempty"`
	Critical  *float64 `json:"critical,omitempty"`
	Direction string   `json:"direction,omitempty"`
}

// Binding is a named, admin-authored telemetry query that users run against entities.
type Binding struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Title            string            `json:"title"`
	IntegrationID    string            `json:"integration_id"`
	Kinds            []string          `json:"kinds"`
	Query            string            `json:"query,omitempty"`
	Params           []BindingParam    `json:"params"`
	Shape            string            `json:"shape"`
	Unit             string            `json:"unit"`
	Thresholds       BindingThresholds `json:"thresholds"`
	FreshnessSeconds int64             `json:"freshness_seconds"`
	Role             string            `json:"role"`
	RelationshipType string            `json:"relationship_type,omitempty"`
	Guard            string            `json:"guard,omitempty"`
	WhenEmpty        *float64          `json:"when_empty,omitempty"`
	OnMap            bool              `json:"on_map"`
	ResourceVersion  int64             `json:"resource_version"`
	ETag             string            `json:"-"`
}

// BindingInput creates or changes a binding. On PATCH empty fields are kept: send empty slices, "" through the
// pointers and an empty Thresholds to clear.
type BindingInput struct {
	Name             string             `json:"name,omitempty"`
	Title            string             `json:"title,omitempty"`
	Integration      string             `json:"integration,omitempty"`
	Kinds            []string           `json:"kinds"`
	Query            string             `json:"query,omitempty"`
	Params           []BindingParam     `json:"params"`
	Shape            string             `json:"shape,omitempty"`
	Unit             *string            `json:"unit,omitempty"`
	Thresholds       *BindingThresholds `json:"thresholds,omitempty"`
	FreshnessSeconds int64              `json:"freshness_seconds,omitempty"`
	Role             string             `json:"role,omitempty"`
	RelationshipType *string            `json:"relationship_type,omitempty"`
	// Deed 0.4.0+: "" removes the guard, an empty WhenEmpty removes the value. Left nil (not sent) when unused, so
	// configurations without them keep working against older Deed versions.
	Guard     *string    `json:"guard,omitempty"`
	WhenEmpty *WhenEmpty `json:"when_empty,omitempty"`
	OnMap     *bool      `json:"on_map,omitempty"`
}

// WhenEmpty sets (Value) or removes (no Value) a binding's value for an empty result.
type WhenEmpty struct {
	Value *float64 `json:"value,omitempty"`
}

func bindingETag(b *Binding, e string) { b.ETag = e }

// CreateBinding creates a telemetry binding (installation admin).
func (c *Client) CreateBinding(ctx context.Context, in BindingInput) (*Binding, error) {
	out, resp, err := call[Binding](ctx, c, &request{method: http.MethodPost, path: "/bindings", body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, bindingETag), nil
}

// GetBinding reads a binding by ID or name (the API lists bindings; there is no single-binding read).
func (c *Client) GetBinding(ctx context.Context, idOrName string) (*Binding, error) {
	out, _, err := call[[]Binding](ctx, c, &request{method: http.MethodGet, path: "/bindings"})
	if err != nil {
		return nil, err
	}
	for _, b := range *out {
		if b.ID == idOrName || b.Name == idOrName {
			b.ETag = ETagOf(b.ResourceVersion)
			return &b, nil
		}
	}
	return nil, notFound(p("bindings", idOrName), "binding "+idOrName)
}

// UpdateBinding changes a binding (If-Match etag when non-empty).
func (c *Client) UpdateBinding(ctx context.Context, id, etag string, in BindingInput) (*Binding, error) {
	out, resp, err := call[Binding](ctx, c, &request{method: http.MethodPatch, path: p("bindings", id), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, bindingETag), nil
}

// DeleteBinding deletes a binding.
func (c *Client) DeleteBinding(ctx context.Context, id string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("bindings", id)})
	return err
}
