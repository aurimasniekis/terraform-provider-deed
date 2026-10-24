package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
)

// Link is a typed external link on an entity.
type Link struct {
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// VersionInfo is the declared version policy of an entity.
type VersionInfo struct {
	Declared string `json:"declared,omitempty"`
	Policy   string `json:"policy,omitempty"`
	Channel  string `json:"channel,omitempty"`
	Product  string `json:"product,omitempty"`
}

// IsZero reports whether no field is set.
func (v *VersionInfo) IsZero() bool {
	return v == nil || (v.Declared == "" && v.Policy == "" && v.Channel == "" && v.Product == "")
}

// Metadata is the writable metadata of a declaration. Empty fields are omitted, so a declaration only asserts what
// it sets.
type Metadata struct {
	DisplayName string            `json:"displayName,omitempty"`
	Description string            `json:"description,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	Lifecycle   string            `json:"lifecycle,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Links       []Link            `json:"links,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Version     *VersionInfo      `json:"version,omitempty"`
}

// EntityMetadata is the effective (merged) metadata of an entity.
type EntityMetadata struct {
	ID              string            `json:"id"`
	Project         string            `json:"project"`
	ProjectID       string            `json:"projectId"`
	Name            string            `json:"name"`
	DisplayName     string            `json:"displayName,omitempty"`
	Description     string            `json:"description,omitempty"`
	Environment     string            `json:"environment,omitempty"`
	Owner           string            `json:"owner,omitempty"`
	Lifecycle       string            `json:"lifecycle,omitempty"`
	Icon            string            `json:"icon,omitempty"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	Links           []Link            `json:"links"`
	Tags            []string          `json:"tags"`
	Version         map[string]string `json:"version"`
	Aliases         []string          `json:"aliases,omitempty"`
	ResourceVersion int64             `json:"resourceVersion"`
}

// EntityStatus is the read-only, computed part of the effective view.
type EntityStatus struct {
	Health struct {
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	} `json:"health"`
	Freshness struct {
		State string `json:"state"`
	} `json:"freshness"`
	Valid         bool   `json:"valid"`
	SchemaVersion string `json:"schemaVersion"`
	Declared      bool   `json:"declared"`
	Facets        int    `json:"facets"`
	Conflicts     int    `json:"conflicts"`
	TombstonedAt  string `json:"tombstonedAt,omitempty"`
}

// Entity is the effective view of an entity (all sources merged).
type Entity struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   EntityMetadata  `json:"metadata"`
	Spec       json.RawMessage `json:"spec"`
	Status     json.RawMessage `json:"status"`
}

// ParsedStatus decodes the status document.
func (e *Entity) ParsedStatus() EntityStatus {
	var s EntityStatus
	_ = json.Unmarshal(e.Status, &s)
	return s
}

// Declaration is one source's facet of an entity: exactly what that source asserted, never other sources' data.
type Declaration struct {
	EntityID        string          `json:"entity_id"`
	Project         string          `json:"project"`
	ProjectID       string          `json:"project_id"`
	Source          string          `json:"source"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Metadata        Metadata        `json:"metadata"`
	Spec            json.RawMessage `json:"spec"`
	ResourceVersion int64           `json:"resource_version"`
	Entity          *Entity         `json:"entity"`
	ETag            string          `json:"-"`
}

// DeclarationInput is the writable part of a declaration.
type DeclarationInput struct {
	Metadata *Metadata       `json:"metadata,omitempty"`
	Spec     json.RawMessage `json:"spec,omitempty"`
}

// EntityCreate declares a new entity through a source.
type EntityCreate struct {
	Project  string          `json:"project"`
	Source   string          `json:"source"`
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Metadata *Metadata       `json:"metadata,omitempty"`
	Spec     json.RawMessage `json:"spec,omitempty"`
}

func declETag(d *Declaration, e string) { d.ETag = e }

// CreateEntity declares an entity (or adopts an existing one with the same project, kind and name that this source
// does not declare yet). It fails with 409 already_exists when the source already declares it.
func (c *Client) CreateEntity(ctx context.Context, in EntityCreate) (*Declaration, error) {
	out, resp, err := call[Declaration](ctx, c, &request{method: http.MethodPost, path: "/entities", body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, declETag), nil
}

// GetDeclaration reads one source's declaration of an entity (404 when the source does not declare it).
func (c *Client) GetDeclaration(ctx context.Context, entityID, source string) (*Declaration, error) {
	out, resp, err := call[Declaration](ctx, c, &request{method: http.MethodGet, path: p("entities", entityID, "declarations", source)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, declETag), nil
}

// PutDeclaration replaces (or adds) one source's declaration (If-Match etag when non-empty).
func (c *Client) PutDeclaration(ctx context.Context, entityID, source, etag string, in DeclarationInput) (*Declaration, error) {
	out, resp, err := call[Declaration](ctx, c, &request{method: http.MethodPut, path: p("entities", entityID, "declarations", source), body: in,
		ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, declETag), nil
}

// DeleteDeclaration removes only this source's facet; other sources' contributions survive.
func (c *Client) DeleteDeclaration(ctx context.Context, entityID, source string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("entities", entityID, "declarations", source)})
	return err
}

// GetEntity reads the effective view of an entity.
func (c *Client) GetEntity(ctx context.Context, id string) (*Entity, error) {
	out, _, err := call[Entity](ctx, c, &request{method: http.MethodGet, path: p("entities", id)})
	return out, err
}

// LookupEntity finds an entity by project, kind and name (aliases included).
func (c *Client) LookupEntity(ctx context.Context, project, kind, name string) (*Entity, error) {
	q := url.Values{"project": {project}, "kind": {kind}, "name": {name}}
	out, _, err := call[Entity](ctx, c, &request{method: http.MethodGet, path: "/entities/lookup", query: q})
	return out, err
}

// EntityFilter filters ListEntities.
type EntityFilter struct {
	Projects []string
	Kinds    []string
	Labels   map[string]string
	Query    string
	// Limit caps the number of entities returned (0 = all).
	Limit int
}

type entityPage struct {
	Items      []Entity `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Total      int      `json:"total"`
}

// ListEntities searches entities, following cursor pagination.
func (c *Client) ListEntities(ctx context.Context, f EntityFilter) ([]Entity, error) {
	q := url.Values{}
	for _, v := range f.Projects {
		q.Add("project", v)
	}
	for _, v := range f.Kinds {
		q.Add("kind", v)
	}
	keys := make([]string, 0, len(f.Labels))
	for k := range f.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q.Add("label", k+"="+f.Labels[k])
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	q.Set("sort", "name")
	out := []Entity{}
	cursor := ""
	for range 1000 {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("limit", strconv.Itoa(500))
		if cursor != "" {
			qq.Set("cursor", cursor)
		}
		page, _, err := call[entityPage](ctx, c, &request{method: http.MethodGet, path: "/entities", query: qq})
		if err != nil {
			return nil, err
		}
		for _, e := range page.Items {
			out = append(out, e)
			if f.Limit > 0 && len(out) >= f.Limit {
				return out, nil
			}
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	return out, nil
}
