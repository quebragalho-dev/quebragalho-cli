package qgaccount

import (
	"context"
	"net/http"
	"strings"
)

// ModelPricing is a per-1M-token price table in one currency. Amounts stay
// decimal strings exactly as the gateway emits them.
type ModelPricing struct {
	Currency    string `json:"currency"`
	Input       string `json:"input"`
	Output      string `json:"output"`
	CachedInput string `json:"cached_input"`
}

// ModelCapabilities is the live feature matrix for a model.
type ModelCapabilities struct {
	Tools     bool `json:"tools"`
	Vision    bool `json:"vision"`
	Reasoning bool `json:"reasoning"`
}

// ModelOffer describes a temporary promotion (e.g. relâmpago). Nil offer =
// no active promotion; a present offer with a zero discount means the
// gateway did not publish the number, not that the discount is zero.
type ModelOffer struct {
	Type            string `json:"type"`
	DiscountPercent int    `json:"discount_percent"`
}

// ModelInfo is one entry of GET /v1/models. Unknown gateway fields are
// ignored; nullable scalars stay pointers so "not announced" never becomes
// a misleading zero.
type ModelInfo struct {
	ID              string            `json:"id"`
	OwnedBy         string            `json:"owned_by"`
	DisplayName     *string           `json:"display_name"`
	ContextLength   *int64            `json:"context_length"`
	MaxOutputTokens *int64            `json:"max_output_tokens"`
	Capabilities    ModelCapabilities `json:"capabilities"`
	Description     *string           `json:"description"`
	AcceptsBonus    bool              `json:"accepts_bonus"`
	Pricing         ModelPricing      `json:"pricing"`
	PricingBRL      ModelPricing      `json:"pricing_brl"`
	Offer           *ModelOffer       `json:"offer"`
}

// Models fetches the live model catalog from GET /v1/models.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	var payload struct {
		Data []ModelInfo `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/models", nil, &payload); err != nil {
		return nil, err
	}
	return payload.Data, nil
}
