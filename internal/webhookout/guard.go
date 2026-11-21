package webhookout

import (
	"net/netip"

	"github.com/SigmaUno/subspace/internal/netguard"
)

var ErrBlocked = netguard.ErrBlocked

type Guard = netguard.Guard

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func ParsePrefixes(list string) ([]netip.Prefix, error) { return netguard.ParsePrefixes(list) }

func NewGuard(allow []netip.Prefix) *Guard { return netguard.New(allow) }
