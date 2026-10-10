package qgaccount

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func (pricing ModelPricing) Cost(tokens qgruntime.Usage) (*big.Rat, error) {
	if pricing.Currency != "BRL" && pricing.Currency != "USD" {
		return nil, fmt.Errorf("unknown pricing currency")
	}
	normalized, err := qgruntime.NormalizeUsage(qgruntime.TokenUsage{
		InputTokens: tokens.EffectiveInputTokens(), OutputTokens: tokens.EffectiveOutputTokens(), CachedInputTokens: tokens.CachedInputTokens,
	})
	if err != nil {
		return nil, err
	}
	parts := []struct {
		tokens int
		rate   string
	}{
		{normalized.InputTokens - normalized.CachedInputTokens, pricing.Input},
		{normalized.CachedInputTokens, pricing.CachedInput},
		{normalized.OutputTokens, pricing.Output},
	}
	total := new(big.Rat)
	for _, part := range parts {
		if part.tokens == 0 {
			continue
		}
		rate, ok := new(big.Rat).SetString(strings.TrimSpace(part.rate))
		if !ok || rate.Sign() < 0 {
			return nil, fmt.Errorf("missing or invalid pricing rate")
		}
		total.Add(total, new(big.Rat).Mul(rate, new(big.Rat).SetFrac64(int64(part.tokens), 1000000)))
	}
	return total, nil
}
