package client

import (
	"context"
	"net/http"
	"time"
)

// ServiceAccount is a non-human Deed user for automation: no password, SSO or sessions, only API tokens.
type ServiceAccount struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	DisplayName      string    `json:"display_name"`
	Description      string    `json:"description,omitempty"`
	Status           string    `json:"status"`
	InstallationRole string    `json:"installation_role,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ServiceAccountInput creates a service account (Name required).
type ServiceAccountInput struct {
	Name             string `json:"name,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
	Description      string `json:"description,omitempty"`
	InstallationRole string `json:"installation_role,omitempty"`
}

// ServiceAccountPatch changes a service account; nil fields are kept.
type ServiceAccountPatch struct {
	DisplayName      *string `json:"display_name,omitempty"`
	Description      *string `json:"description,omitempty"`
	Status           *string `json:"status,omitempty"`
	InstallationRole *string `json:"installation_role,omitempty"`
}

// CreateServiceAccount creates a service account (installation owner or admin).
func (c *Client) CreateServiceAccount(ctx context.Context, in ServiceAccountInput) (*ServiceAccount, error) {
	out, _, err := call[ServiceAccount](ctx, c, &request{method: http.MethodPost, path: "/service-accounts", body: in, idempotent: true})
	return out, err
}

// GetServiceAccount reads a service account by ID or name.
func (c *Client) GetServiceAccount(ctx context.Context, idOrName string) (*ServiceAccount, error) {
	out, _, err := call[ServiceAccount](ctx, c, &request{method: http.MethodGet, path: p("service-accounts", idOrName)})
	return out, err
}

// UpdateServiceAccount changes a service account.
func (c *Client) UpdateServiceAccount(ctx context.Context, idOrName string, in ServiceAccountPatch) (*ServiceAccount, error) {
	out, _, err := call[ServiceAccount](ctx, c, &request{method: http.MethodPatch, path: p("service-accounts", idOrName), body: in})
	return out, err
}

// DeleteServiceAccount deletes a service account with its tokens and memberships.
func (c *Client) DeleteServiceAccount(ctx context.Context, idOrName string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("service-accounts", idOrName)})
	return err
}

// APIToken describes an API token (never its secret).
type APIToken struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	UserID           string     `json:"user_id,omitempty"`
	InstallationRole string     `json:"installation_role,omitempty"`
	ProjectRole      string     `json:"project_role,omitempty"`
	Projects         []string   `json:"projects"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

// CreatedToken is a new token with its secret (returned once).
type CreatedToken struct {
	APIToken
	Token string `json:"token"`
}

// ServiceTokenInput issues a service account token. InstallationRole nil inherits the account's role.
type ServiceTokenInput struct {
	Name             string   `json:"name"`
	InstallationRole *string  `json:"installation_role,omitempty"`
	ProjectRole      string   `json:"project_role,omitempty"`
	Projects         []string `json:"projects,omitempty"`
	ExpiresInDays    int64    `json:"expires_in_days,omitempty"`
}

// CreateServiceAccountToken issues a token for a service account; the secret is returned once.
func (c *Client) CreateServiceAccountToken(ctx context.Context, account string, in ServiceTokenInput) (*CreatedToken, error) {
	out, _, err := call[CreatedToken](ctx, c, &request{method: http.MethodPost, path: p("service-accounts", account, "tokens"), body: in, idempotent: true})
	return out, err
}

// ListServiceAccountTokens lists a service account's tokens, including revoked and expired ones.
func (c *Client) ListServiceAccountTokens(ctx context.Context, account string) ([]APIToken, error) {
	out, _, err := call[[]APIToken](ctx, c, &request{method: http.MethodGet, path: p("service-accounts", account, "tokens")})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// GetServiceAccountToken returns one of a service account's tokens (404 when it does not exist).
func (c *Client) GetServiceAccountToken(ctx context.Context, account, id string) (*APIToken, error) {
	toks, err := c.ListServiceAccountTokens(ctx, account)
	if err != nil {
		return nil, err
	}
	for _, t := range toks {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, notFound(p("service-accounts", account, "tokens", id), "token "+id)
}

// RevokeServiceAccountToken revokes a service account token.
func (c *Client) RevokeServiceAccountToken(ctx context.Context, account, id string) error {
	_, err := c.do(ctx, &request{method: http.MethodDelete, path: p("service-accounts", account, "tokens", id)})
	return err
}
