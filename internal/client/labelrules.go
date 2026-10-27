package client

import (
	"context"
	"net/http"
)

// LabelRuleMatch selects the entities of a project a label rule applies to (every given criterion must match; globs).
type LabelRuleMatch struct {
	Kinds   []string          `json:"kinds,omitempty"`
	Sources []string          `json:"sources,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// LabelRule adds labels to a project's entities below every source (Deed 0.7.0).
type LabelRule struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Match           LabelRuleMatch    `json:"match"`
	Labels          map[string]string `json:"labels"`
	ResourceVersion int64             `json:"resource_version"`
	ETag            string            `json:"-"`
}

// LabelRuleInput creates or replaces a label rule. Description and Match are always sent, so removing them clears them.
type LabelRuleInput struct {
	Name        string            `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Match       *LabelRuleMatch   `json:"match,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

func labelRuleETag(r *LabelRule, e string) { r.ETag = e }

// CreateLabelRule creates a rule in a project (project admin); Deed applies it before answering.
func (c *Client) CreateLabelRule(ctx context.Context, project string, in LabelRuleInput) (*LabelRule, error) {
	out, resp, err := call[LabelRule](ctx, c, &request{method: http.MethodPost, path: p("projects", project, "label-rules"), body: in, idempotent: true})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, labelRuleETag), nil
}

// GetLabelRule reads a rule by ID or name.
func (c *Client) GetLabelRule(ctx context.Context, project, idOrName string) (*LabelRule, error) {
	out, resp, err := call[LabelRule](ctx, c, &request{method: http.MethodGet, path: p("projects", project, "label-rules", idOrName)})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, labelRuleETag), nil
}

// UpdateLabelRule changes a rule (If-Match etag when non-empty).
func (c *Client) UpdateLabelRule(ctx context.Context, project, id, etag string, in LabelRuleInput) (*LabelRule, error) {
	out, resp, err := call[LabelRule](ctx, c, &request{method: http.MethodPatch, path: p("projects", project, "label-rules", id), body: in, ifMatch: etag})
	if err != nil {
		return nil, err
	}
	return withETag(out, resp, out.ResourceVersion, labelRuleETag), nil
}

// DeleteLabelRule removes a rule; the labels it added go away.
func (c *Client) DeleteLabelRule(ctx context.Context, project, id string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("projects", project, "label-rules", id)})
	return err
}
