// Package geo resolves country/region/city from an IP address (ТЗ §12).
// A MaxMind GeoLite2 database is optional: when it is absent, the Noop
// resolver leaves the geo fields empty.
package geo

import (
	"net/netip"

	"github.com/oschwald/geoip2-golang"
)

// Resolver returns geo location for an IP address. The strings are empty when
// the location is unknown.
type Resolver interface {
	Lookup(ip netip.Addr) (country, region, city string)
}

// Noop is a resolver that never resolves anything.
type Noop struct{}

// Lookup implements Resolver.
func (Noop) Lookup(netip.Addr) (string, string, string) {
	return "", "", ""
}

// MMDB resolves locations from a MaxMind .mmdb database file.
type MMDB struct {
	reader *geoip2.Reader
}

// OpenMMDB loads the database at path. It fails when the file is missing or
// corrupt.
func OpenMMDB(path string) (*MMDB, error) {
	r, err := geoip2.Open(path)
	if err != nil {
		return nil, err
	}
	return &MMDB{reader: r}, nil
}

// Lookup implements Resolver.
func (m *MMDB) Lookup(ip netip.Addr) (string, string, string) {
	record, err := m.reader.City(ip.AsSlice())
	if err != nil {
		return "", "", ""
	}
	if record.Country.IsoCode == "" {
		return "", "", ""
	}
	region := ""
	if len(record.Subdivisions) > 0 {
		region = record.Subdivisions[0].Names["en"]
	}
	return record.Country.IsoCode, region, record.City.Names["en"]
}

// Close releases the database file.
func (m *MMDB) Close() error {
	return m.reader.Close()
}
