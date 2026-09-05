package testmoney

import (
	"errors"
	"math/big"
	"strings"
	"time"
)

var (
	ErrDisabled  = errors.New("test-money issuance is disabled")
	ErrInput     = errors.New("invalid test-money request or policy")
	ErrRole      = errors.New("active treasury role required")
	ErrChecker   = errors.New("proposer and approver must differ")
	ErrRecipient = errors.New("recipient is not eligible")
	ErrConflict  = errors.New("idempotency payload conflict")
	ErrLimit     = errors.New("test-money issuance ceiling exceeded")
	ErrReference = errors.New("independent fresh reference evidence required")
)

// Values use eight USDT decimal places, independently of asset decimals.
const TesterLimitUSDTAtomic = "1000000000000"

type AssetPolicy struct {
	Network  string
	Decimals int
}

// Global limits and reference tolerances have no operational defaults.
type Policy struct {
	GlobalLimitUSDTAtomic string
	MaxAgeSeconds         int
	MaxDivergenceBPS      int
	Assets                map[string]AssetPolicy
}

type Observation struct {
	Venue           string
	PriceUSDTAtomic string
	ObservedAt      time.Time
}

func atomic(s string, zero bool) (*big.Int, error) {
	if s == "" || len(s) > 78 || (len(s) > 1 && s[0] == '0') {
		return nil, ErrInput
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, ErrInput
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || (!zero && n.Sign() == 0) {
		return nil, ErrInput
	}
	return n, nil
}

func (p Policy) Validate() error {
	if _, err := atomic(p.GlobalLimitUSDTAtomic, false); err != nil {
		return err
	}
	if p.MaxAgeSeconds < 1 || p.MaxAgeSeconds > 300 || p.MaxDivergenceBPS < 1 || p.MaxDivergenceBPS > 10000 || len(p.Assets) == 0 {
		return ErrInput
	}
	for id, a := range p.Assets {
		if id == "" || a.Decimals < 0 || a.Decimals > 36 {
			return ErrInput
		}
		switch a.Network {
		case "internal_spot", "ethereum_sepolia", "bitcoin_testnet4":
		default:
			return ErrInput
		}
	}
	return nil
}

// Value conservatively rounds up fractional midpoint/value atoms. Evidence must
// come from a trusted server adapter, never from a grant request.
func (p Policy) Value(assetID, amount string, observations []Observation, now time.Time) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	a, ok := p.Assets[assetID]
	if !ok {
		return "", ErrInput
	}
	qty, err := atomic(amount, false)
	if err != nil {
		return "", err
	}
	if now.IsZero() || len(observations) != 2 {
		return "", ErrReference
	}
	values := make([]*big.Int, 2)
	venues := map[string]bool{}
	for i, o := range observations {
		venue := strings.ToLower(strings.TrimSpace(o.Venue))
		if venue == "" || venues[venue] || o.ObservedAt.IsZero() || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > time.Duration(p.MaxAgeSeconds)*time.Second {
			return "", ErrReference
		}
		venues[venue] = true
		values[i], err = atomic(o.PriceUSDTAtomic, false)
		if err != nil {
			return "", ErrReference
		}
	}
	low, high := values[0], values[1]
	if low.Cmp(high) > 0 {
		low, high = high, low
	}
	spread := new(big.Int).Mul(new(big.Int).Sub(high, low), big.NewInt(10000))
	if spread.Cmp(new(big.Int).Mul(low, big.NewInt(int64(p.MaxDivergenceBPS)))) > 0 {
		return "", ErrReference
	}
	price := ceilDiv(new(big.Int).Add(low, high), big.NewInt(2))
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(a.Decimals)), nil)
	return ceilDiv(new(big.Int).Mul(qty, price), scale).String(), nil
}

func ceilDiv(n, d *big.Int) *big.Int {
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, d, r)
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func (p Policy) CheckQuota(value, userUsed, globalUsed string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	v, e := atomic(value, false)
	if e != nil {
		return e
	}
	u, e := atomic(userUsed, true)
	if e != nil {
		return e
	}
	g, e := atomic(globalUsed, true)
	if e != nil {
		return e
	}
	limit, _ := atomic(TesterLimitUSDTAtomic, false)
	global, _ := atomic(p.GlobalLimitUSDTAtomic, false)
	if new(big.Int).Add(u, v).Cmp(limit) > 0 || new(big.Int).Add(g, v).Cmp(global) > 0 {
		return ErrLimit
	}
	return nil
}
