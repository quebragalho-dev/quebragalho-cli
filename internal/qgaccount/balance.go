package qgaccount

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"
)

// Balance is the account wallet snapshot. Amounts stay in the gateway's
// decimal-string form end to end — never parsed into a float — so no binary
// rounding ever touches a displayed saldo.
type Balance struct {
	Currency  string `json:"currency"`
	Available string `json:"available"`
	Default   string `json:"default"`
	Bonus     string `json:"bonus"`
}

// Balance fetches the live wallet snapshot. Callers must not cache it: the
// endpoint is cheap and a stale saldo is worse than a slow one.
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var balance Balance
	if err := c.doJSON(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/balance", nil, &balance); err != nil {
		return Balance{}, err
	}
	return balance, nil
}

// FormatBRL renders a gateway decimal string as Brazilian reais rounded
// half-up to centavos ("145.05489680807" → "R$ 145,05"). Parsing uses
// big.Rat: a float64 would silently drift on values the gateway emits.
func FormatBRL(decimal string) (string, error) {
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(decimal))
	if !ok {
		return "", fmt.Errorf("qg account: not a decimal amount: %q", decimal)
	}
	cents := new(big.Rat).Mul(amount, big.NewRat(100, 1))
	// Round half away from zero at the centavo boundary.
	num, den := cents.Num(), cents.Denom()
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twiceRem := new(big.Int).Abs(new(big.Int).Mul(rem, big.NewInt(2)))
	if twiceRem.CmpAbs(den) >= 0 {
		if cents.Sign() < 0 {
			quo.Sub(quo, big.NewInt(1))
		} else {
			quo.Add(quo, big.NewInt(1))
		}
	}
	sign := ""
	if quo.Sign() < 0 {
		sign = "-"
		quo.Abs(quo)
	}
	whole, fraction := new(big.Int).QuoRem(quo, big.NewInt(100), new(big.Int))
	wholeText := GroupThousands(whole.String())
	return fmt.Sprintf("R$ %s%s,%02d", sign, wholeText, fraction.Int64()), nil
}

// GroupThousands inserts "." every three digits from the right, pt-BR style.
// digits must be unsigned (the caller keeps the sign aside); the CLI shares it
// for token/request counts so both surfaces group identically.
func GroupThousands(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var out []byte
	for i, digit := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, digit)
	}
	return string(out)
}
