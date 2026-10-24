package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Ref identifies a relationship endpoint: by entity ID, or by natural key (kind + name, optionally in another project).
type Ref struct {
	ID      string `json:"id,omitempty"`
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Name    string `json:"name,omitempty"`
}

// EntityRef is a resolved relationship endpoint.
type EntityRef struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Tombstoned  bool   `json:"tombstoned,omitempty"`
	Redacted    bool   `json:"redacted,omitempty"`
}

// Assertion is one source's assertion of a relationship.
type Assertion struct {
	ID            string          `json:"id"`
	SourceID      string          `json:"source_id"`
	Source        string          `json:"source"`
	Provider      string          `json:"provider"`
	Layer         string          `json:"layer"`
	EvidenceClass string          `json:"evidence_class"`
	Attributes    json.RawMessage `json:"attributes"`
	Live          bool            `json:"live"`
}

// Relation is a relationship with every source's assertion.
type Relation struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	ProjectID     string          `json:"project_id"`
	From          EntityRef       `json:"from"`
	To            EntityRef       `json:"to"`
	Attributes    json.RawMessage `json:"attributes"`
	EvidenceClass string          `json:"evidence_class"`
	Inferred      bool            `json:"inferred"`
	CrossProject  bool            `json:"cross_project"`
	BridgeID      string          `json:"bridge_id,omitempty"`
	Live          bool            `json:"live"`
	Assertions    []Assertion     `json:"assertions,omitempty"`
}

// LiveAssertion returns the live assertion of a source (nil when the source does not assert the relationship).
func (r *Relation) LiveAssertion(source string) *Assertion {
	for i := range r.Assertions {
		if r.Assertions[i].Source == source && r.Assertions[i].Live {
			return &r.Assertions[i]
		}
	}
	return nil
}

// RelationCreate declares a relationship through a source.
type RelationCreate struct {
	Project    string          `json:"project,omitempty"`
	Source     string          `json:"source"`
	Type       string          `json:"type"`
	From       Ref             `json:"from"`
	To         Ref             `json:"to"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
}

// CreateRelation records a source's assertion of a relationship (409 when the source already asserts it, 404 when an
// endpoint does not exist, 422 when endpoint kinds or project bridges forbid it).
func (c *Client) CreateRelation(ctx context.Context, in RelationCreate) (*Relation, error) {
	out, _, err := call[Relation](ctx, c, &request{method: http.MethodPost, path: "/relations", body: in, idempotent: true})
	return out, err
}

// GetRelation reads a relationship with its assertions.
func (c *Client) GetRelation(ctx context.Context, id string) (*Relation, error) {
	out, _, err := call[Relation](ctx, c, &request{method: http.MethodGet, path: p("relations", id)})
	return out, err
}

// PutAssertion replaces (or adds) a source's assertion of a relationship.
func (c *Client) PutAssertion(ctx context.Context, id, source string, attributes json.RawMessage) (*Relation, error) {
	body := map[string]json.RawMessage{}
	if len(attributes) > 0 {
		body["attributes"] = attributes
	}
	out, _, err := call[Relation](ctx, c, &request{method: http.MethodPut, path: p("relations", id, "assertions", source), body: body})
	return out, err
}

// DeleteAssertion retracts a source's assertion; the relationship stays while other sources assert it.
func (c *Client) DeleteAssertion(ctx context.Context, id, source string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("relations", id, "assertions", source)})
	return err
}

// RelationFilter filters ListRelations.
type RelationFilter struct {
	Entity    string
	Direction string
	Types     []string
}

type relationPage struct {
	Items      []Relation `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// ListRelations lists relationships, following cursor pagination.
func (c *Client) ListRelations(ctx context.Context, f RelationFilter) ([]Relation, error) {
	out := []Relation{}
	cursor := ""
	for range 1000 {
		q := url.Values{"limit": {"500"}}
		if f.Entity != "" {
			q.Set("entity", f.Entity)
		}
		if f.Direction != "" {
			q.Set("direction", f.Direction)
		}
		for _, t := range f.Types {
			q.Add("type", t)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		page, _, err := call[relationPage](ctx, c, &request{method: http.MethodGet, path: "/relations", query: q})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.NextCursor == "" || len(page.Items) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	return out, nil
}
