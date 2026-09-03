package custody

import (
	"errors"
	"strings"
)

var ErrNetworkNotApproved = errors.New("custody network is not approved")

// RoutePolicy is enforced before an address is issued or a future signing
// request is constructed. It deliberately fails closed: self-custody can only
// be enabled for the explicitly enumerated test networks.
type RoutePolicy struct {
	Mode                      string
	SelfCustodyTestnetEnabled bool
	TestnetOnly               bool
}

func (p RoutePolicy) Validate(network string) error {
	if p.TestnetOnly && !isApprovedSelfCustodyTestnet(network) {
		return ErrNetworkNotApproved
	}
	if strings.ToLower(strings.TrimSpace(p.Mode)) != "self_custody_testnet" {
		return nil
	}
	if !p.SelfCustodyTestnetEnabled || !isApprovedSelfCustodyTestnet(network) {
		return ErrNetworkNotApproved
	}
	return nil
}

func isApprovedSelfCustodyTestnet(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "bitcoin_testnet4", "ethereum_sepolia":
		return true
	default:
		return false
	}
}
