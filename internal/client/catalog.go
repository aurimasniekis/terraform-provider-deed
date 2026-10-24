package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// SystemInfo describes the server (GET /system/info, public).
type SystemInfo struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Commit     string   `json:"commit,omitempty"`
	APIVersion string   `json:"api_version"`
	Features   []string `json:"features"`
}

// SystemInfo returns the server version and API version (a single attempt: it is a best-effort probe).
func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	out, _, err := call[SystemInfo](ctx, c, &request{method: http.MethodGet, path: "/system/info", noRetry: true})
	return out, err
}

// Project is an organizational and security scope.
type Project struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description"`
	AllowHTTPLinks  bool   `json:"allow_http_links"`
	Synthetic       bool   `json:"synthetic"`
	ResourceVersion int64  `json:"resource_version"`
	Role            string `json:"role"`
	ETag            string `json:"-"`
}

// ProjectInput creates a project (Name required) or replaces its mutable fields (PATCH).
type ProjectInput struct {
	Name           string `json:"name,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	Description    string `json:"description,omitempty"`
	AllowHTTPLinks bool   `json:"allow_http_links,omitempty"`
}

func withETag[T any](out *T, resp *response, version int64, set func(*T, string)) *T {
	set(out, etagFor(resp, version))
	return out
}

func projectETag(p *Project, e string) { p.ETag = e }

// CreateProject creates a project (installation owner or admin token).
func (c *Client) CreateProject(ctx context.Context, in ProjectInput) (*Project, error) {
	out, resp, err := call[Project](ctx, c, &request{method: http.MethodPost, path: "/projects", body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, projectETag), nil
}

// GetProject reads a project by ID or name.
func (c *Client) GetProject(ctx context.Context, idOrName string) (*Project, error) {
	out, resp, err := call[Project](ctx, c, &request{method: http.MethodGet, path: p("projects", idOrName)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, projectETag), nil
}

// ListProjects lists the projects the token can read.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	out, _, err := call[[]Project](ctx, c, &request{method: http.MethodGet, path: "/projects"})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// UpdateProject replaces a project's display name, description and link policy (If-Match etag when non-empty).
func (c *Client) UpdateProject(ctx context.Context, idOrName, etag string, in ProjectInput) (*Project, error) {
	in.Name = ""
	out, resp, err := call[Project](ctx, c, &request{method: http.MethodPatch, path: p("projects", idOrName), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, projectETag), nil
}

// DeleteProject deletes a project. Without force Deed refuses while live entities remain.
func (c *Client) DeleteProject(ctx context.Context, idOrName string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("projects", idOrName), query: q})
	return err
}

// ProjectMember is a user's explicit role in a project.
type ProjectMember struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Role        string `json:"role"`
	RoleSource  string `json:"role_source"`
}

// ListMembers lists the explicit members of a project.
func (c *Client) ListMembers(ctx context.Context, project string) ([]ProjectMember, error) {
	out, _, err := call[[]ProjectMember](ctx, c, &request{method: http.MethodGet, path: p("projects", project, "members")})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// GetMember returns one member of a project (404 problem when the user holds no explicit role).
func (c *Client) GetMember(ctx context.Context, project, userID string) (*ProjectMember, error) {
	ms, err := c.ListMembers(ctx, project)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		if ms[i].UserID == userID {
			return &ms[i], nil
		}
	}
	return nil, notFound(p("projects", project, "members"), "project member "+userID)
}

// SetMember grants a project role (viewer, editor or admin).
func (c *Client) SetMember(ctx context.Context, project, userID, role string) error {
	_, err := c.do(ctx, &request{method: http.MethodPut, path: p("projects", project, "members", userID), body: map[string]string{"role": role}})
	return err
}

// RemoveMember removes a project role.
func (c *Client) RemoveMember(ctx context.Context, project, userID string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("projects", project, "members", userID)})
	return err
}

// KindVersion is one published schema version of a kind.
type KindVersion struct {
	Version   string          `json:"version"`
	Schema    json.RawMessage `json:"schema"`
	Breaking  bool            `json:"breaking"`
	Migration string          `json:"migration,omitempty"`
}

// KindSupport describes what Deed can do for a kind (computed by the server).
type KindSupport struct {
	Modeling  bool     `json:"modeling"`
	Terraform []string `json:"terraform"`
	Runtime   []string `json:"runtime"`
	Native    []string `json:"native"`
}

// Kind is a kind definition.
type Kind struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Group           string        `json:"group"`
	Builtin         bool          `json:"builtin"`
	Title           string        `json:"title"`
	Plural          string        `json:"plural"`
	Description     string        `json:"description"`
	Icon            string        `json:"icon"`
	Color           string        `json:"color"`
	IdentityKeys    [][]string    `json:"identity_keys"`
	StorageVersion  string        `json:"storage_version"`
	Versions        []KindVersion `json:"versions"`
	Deprecated      bool          `json:"deprecated"`
	ResourceVersion int64         `json:"resource_version"`
	Support         KindSupport   `json:"support"`
	ETag            string        `json:"-"`
}

// StorageSchema returns the schema of the storage version (nil when unknown).
func (k *Kind) StorageSchema() json.RawMessage {
	for _, v := range k.Versions {
		if v.Version == k.StorageVersion {
			return v.Schema
		}
	}
	return nil
}

func kindETag(k *Kind, e string) { k.ETag = e }

// KindInput creates a custom kind or replaces its display fields and identity keys (PATCH).
type KindInput struct {
	Name         string     `json:"name,omitempty"`
	Group        string     `json:"group,omitempty"`
	Title        string     `json:"title"`
	Plural       string     `json:"plural,omitempty"`
	Description  string     `json:"description,omitempty"`
	Icon         string     `json:"icon,omitempty"`
	Color        string     `json:"color,omitempty"`
	IdentityKeys [][]string `json:"identity_keys,omitempty"`
	Deprecated   bool       `json:"deprecated,omitempty"`
}

type kindCreate struct {
	KindInput
	Schema json.RawMessage `json:"schema,omitempty"`
}

// CreateKind creates a custom kind with its first schema version (v1). An empty schema means an open object.
func (c *Client) CreateKind(ctx context.Context, in KindInput, schema json.RawMessage) (*Kind, error) {
	out, resp, err := call[Kind](ctx, c, &request{method: http.MethodPost, path: "/kinds", body: kindCreate{KindInput: in, Schema: schema}, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, kindETag), nil
}

// GetKind reads a kind by name.
func (c *Client) GetKind(ctx context.Context, name string) (*Kind, error) {
	out, resp, err := call[Kind](ctx, c, &request{method: http.MethodGet, path: p("kinds", name)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, kindETag), nil
}

// ListKinds lists built-in and custom kinds.
func (c *Client) ListKinds(ctx context.Context) ([]Kind, error) {
	out, _, err := call[[]Kind](ctx, c, &request{method: http.MethodGet, path: "/kinds"})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// UpdateKind replaces a custom kind's display fields and identity keys.
func (c *Client) UpdateKind(ctx context.Context, name, etag string, in KindInput) (*Kind, error) {
	in.Name = ""
	out, resp, err := call[Kind](ctx, c, &request{method: http.MethodPatch, path: p("kinds", name), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, kindETag), nil
}

// PublishInput publishes a new schema version.
type PublishInput struct {
	Schema    json.RawMessage `json:"schema"`
	Breaking  bool            `json:"breaking,omitempty"`
	Migration string          `json:"migration,omitempty"`
}

// PublishResult reports a new schema version.
type PublishResult struct {
	Kind                  *Kind    `json:"kind"`
	Version               string   `json:"version"`
	CompatibilityProblems []string `json:"compatibility_problems"`
	Revalidated           int      `json:"revalidated"`
	Invalid               int      `json:"invalid"`
}

// PublishKindVersion publishes a schema version and makes it the storage version. A non-breaking version that is not
// backward compatible fails with 409 conflict whose fields list the compatibility problems.
func (c *Client) PublishKindVersion(ctx context.Context, name, etag string, in PublishInput) (*PublishResult, error) {
	out, resp, err := call[PublishResult](ctx, c, &request{method: http.MethodPost, path: p("kinds", name, "versions"), body: in, ifMatch: etag,
		idempotent: true})
	if err != nil {
		return nil, err
	}
	if out.Kind != nil {
		out.Kind.ETag = etagFor(resp, out.Kind.ResourceVersion)
	}
	return out, nil
}

// DeleteKind deletes an unused custom kind.
func (c *Client) DeleteKind(ctx context.Context, name string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("kinds", name)})
	return err
}

// RelType is a relationship type definition.
type RelType struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	InverseName      string          `json:"inverse_name"`
	Title            string          `json:"title"`
	Description      string          `json:"description"`
	Directed         bool            `json:"directed"`
	FromKinds        []string        `json:"from_kinds"`
	ToKinds          []string        `json:"to_kinds"`
	DefaultEvidence  string          `json:"default_evidence"`
	Cardinality      string          `json:"cardinality"`
	AttributesSchema json.RawMessage `json:"attributes_schema,omitempty"`
	Impact           string          `json:"impact"`
	Color            string          `json:"color"`
	Builtin          bool            `json:"builtin"`
	ResourceVersion  int64           `json:"resource_version"`
	ETag             string          `json:"-"`
}

func relTypeETag(r *RelType, e string) { r.ETag = e }

// RelTypeInput creates or replaces a custom relationship type.
type RelTypeInput struct {
	Name             string          `json:"name,omitempty"`
	InverseName      string          `json:"inverse_name,omitempty"`
	Title            string          `json:"title"`
	Description      string          `json:"description,omitempty"`
	Directed         *bool           `json:"directed,omitempty"`
	FromKinds        []string        `json:"from_kinds,omitempty"`
	ToKinds          []string        `json:"to_kinds,omitempty"`
	DefaultEvidence  string          `json:"default_evidence,omitempty"`
	Cardinality      string          `json:"cardinality,omitempty"`
	AttributesSchema json.RawMessage `json:"attributes_schema,omitempty"`
	Impact           string          `json:"impact,omitempty"`
	Color            string          `json:"color,omitempty"`
}

// CreateRelType creates a custom relationship type.
func (c *Client) CreateRelType(ctx context.Context, in RelTypeInput) (*RelType, error) {
	out, resp, err := call[RelType](ctx, c, &request{method: http.MethodPost, path: "/relationship-types", body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, relTypeETag), nil
}

// GetRelType reads a relationship type by name.
func (c *Client) GetRelType(ctx context.Context, name string) (*RelType, error) {
	out, resp, err := call[RelType](ctx, c, &request{method: http.MethodGet, path: p("relationship-types", name)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, relTypeETag), nil
}

// UpdateRelType replaces a custom relationship type.
func (c *Client) UpdateRelType(ctx context.Context, name, etag string, in RelTypeInput) (*RelType, error) {
	in.Name = ""
	out, resp, err := call[RelType](ctx, c, &request{method: http.MethodPut, path: p("relationship-types", name), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, relTypeETag), nil
}

// DeleteRelType deletes an unused custom relationship type.
func (c *Client) DeleteRelType(ctx context.Context, name string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("relationship-types", name)})
	return err
}

// Me is the caller of the API.
type Me struct {
	UserID           string `json:"user_id,omitempty"`
	InstallationRole string `json:"installation_role,omitempty"`
	TokenID          string `json:"token_id,omitempty"`
}

// GetMe returns the authenticated principal.
func (c *Client) GetMe(ctx context.Context) (*Me, error) {
	out, _, err := call[Me](ctx, c, &request{method: http.MethodGet, path: "/me"})
	return out, err
}

// APIToken describes one of the caller's API tokens (never its secret).
type APIToken struct {
	ID          string   `json:"id"`
	ProjectRole string   `json:"project_role,omitempty"`
	Projects    []string `json:"projects"`
}

// ListTokens lists the caller's API tokens.
func (c *Client) ListTokens(ctx context.Context) ([]APIToken, error) {
	out, _, err := call[[]APIToken](ctx, c, &request{method: http.MethodGet, path: "/me/tokens"})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// SeesAllProjects reports whether the token can read every project (no project restriction and no project-role cap),
// so that an empty search result really means "none in the installation". Errors count as false.
func (c *Client) SeesAllProjects(ctx context.Context) bool {
	me, err := c.GetMe(ctx)
	if err != nil || me.TokenID == "" || (me.InstallationRole != "owner" && me.InstallationRole != "admin" && me.InstallationRole != "auditor") {
		return false
	}
	ts, err := c.ListTokens(ctx)
	if err != nil {
		return false
	}
	for _, t := range ts {
		if t.ID == me.TokenID {
			return len(t.Projects) == 0 && t.ProjectRole == ""
		}
	}
	return false
}
