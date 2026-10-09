package qgaccount

import (
	"context"
	"net/http"
	"strings"
)

// KeyInfo describes the API key the request authenticated with.
type KeyInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	CreatedAt string `json:"created_at"`
	// AllowedModels is nil when the gateway sent null: the key may run every
	// model. Never coerced to an empty slice, so "no restriction" and
	// "restrictions to nothing" stay distinguishable.
	AllowedModels []string `json:"allowed_models"`
	Plan          KeyPlan  `json:"plan"`
}

// KeyPlan is the billing plan attached to the key.
type KeyPlan struct {
	Code string `json:"code"`
	Type string `json:"type"`
}

// Key fetches the profile of the authenticating API key.
func (c *Client) Key(ctx context.Context) (KeyInfo, error) {
	var info KeyInfo
	if err := c.doJSON(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/key", nil, &info); err != nil {
		return KeyInfo{}, err
	}
	return info, nil
}
