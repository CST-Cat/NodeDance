package geolocation

import (
	"math"
	"strings"
	"unicode"
)

// Position is shared by manual preferences, the city directory and resolved
// locations. Pointers distinguish a valid zero coordinate from a missing value.
type Position struct {
	CountryCode string   `json:"countryCode"`
	City        string   `json:"city"`
	Region      string   `json:"region,omitempty"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
}
type Location struct {
	Status    string    `json:"status"`
	Source    string    `json:"source,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	SampledAt string    `json:"sampledAt,omitempty"`
	Position  *Position `json:"position,omitempty"`
}

func ValidPosition(p *Position) bool {
	if p == nil || len(p.CountryCode) != 2 || p.CountryCode[0] < 'A' || p.CountryCode[0] > 'Z' || p.CountryCode[1] < 'A' || p.CountryCode[1] > 'Z' || p.Latitude == nil || p.Longitude == nil {
		return false
	}
	if math.IsNaN(*p.Latitude) || math.IsInf(*p.Latitude, 0) || *p.Latitude < -90 || *p.Latitude > 90 || math.IsNaN(*p.Longitude) || math.IsInf(*p.Longitude, 0) || *p.Longitude < -180 || *p.Longitude > 180 {
		return false
	}
	if p.City == "" || strings.TrimSpace(p.City) != p.City || len(p.City) > 200 || strings.TrimSpace(p.Region) != p.Region || len(p.Region) > 200 {
		return false
	}
	for _, v := range []string{p.City, p.Region} {
		for _, r := range v {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}
func LocationFromCountry(c Country) Location {
	if c.Position != nil {
		return Location{Status: "known", Source: c.Source, SampledAt: c.SampledAt, Position: c.Position}
	}
	reason := c.Reason
	if reason == "" {
		reason = "city_unavailable"
	}
	return Location{Status: "unknown", Reason: reason}
}
