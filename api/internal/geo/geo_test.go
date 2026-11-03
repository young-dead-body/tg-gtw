package geo

import (
	"net/netip"
	"testing"
)

func TestNoop(t *testing.T) {
	var r Resolver = Noop{}
	country, region, city := r.Lookup(netip.MustParseAddr("8.8.8.8"))
	if country != "" || region != "" || city != "" {
		t.Errorf("expected empty geo, got %q/%q/%q", country, region, city)
	}
}
