package cli

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
)

// execPricing prices per-call usage from the live QG model catalog, the same
// pricing the TUI shows next to each answer. The catalog is fetched in the
// background so the model call is never delayed; a failed or slow fetch just
// leaves the cost fields off the usage events.
type execPricing struct {
	done   chan struct{}
	models map[string]qgaccount.ModelInfo
}

// startExecPricing returns nil for providers other than QG, which have no
// server-side pricing to read.
func startExecPricing(ctx context.Context, profile config.ProviderProfile) *execPricing {
	if strings.TrimSpace(profile.CatalogID) != providercatalog.QGID {
		return nil
	}
	descriptor, ok := providercatalog.Get(providercatalog.QGID)
	if !ok {
		return nil
	}
	pricing := &execPricing{done: make(chan struct{}), models: map[string]qgaccount.ModelInfo{}}
	go func() {
		defer close(pricing.done)
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		models, err := providermodeldiscovery.DiscoverCatalog(dctx, descriptor, discoveryCredentialProfile(profile), providermodeldiscovery.Options{})
		if err != nil {
			return
		}
		for _, model := range models {
			if model.APIModel != nil {
				pricing.models[strings.ToLower(strings.TrimSpace(model.ID))] = *model.APIModel
			}
		}
	}()
	return pricing
}

// cost returns the call's price in BRL and USD, each nil when the catalog has
// no usable rate for that currency.
func (pricing *execPricing) cost(modelID string, usage agent.Usage) (brl *float64, usd *float64) {
	if pricing == nil {
		return nil, nil
	}
	<-pricing.done
	info, ok := pricing.models[strings.ToLower(strings.TrimSpace(modelID))]
	if !ok {
		return nil, nil
	}
	for _, rates := range []qgaccount.ModelPricing{info.PricingBRL, info.Pricing} {
		amount, err := rates.Cost(usage)
		if err != nil {
			continue
		}
		value, _ := new(big.Rat).Set(amount).Float64()
		switch rates.Currency {
		case "BRL":
			if brl == nil {
				brl = &value
			}
		case "USD":
			if usd == nil {
				usd = &value
			}
		}
	}
	return brl, usd
}
