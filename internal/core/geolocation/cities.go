package geolocation

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

type City struct {
	ID string `json:"id"`
	Position
}
type cityRecord struct {
	City
	Search string `json:"search"`
}

//go:embed cities.json.gz
var cityData []byte
var loadCities = sync.OnceValue(func() []cityRecord {
	reader, err := gzip.NewReader(bytes.NewReader(cityData))
	if err != nil {
		panic(err)
	}
	defer reader.Close()
	var result []cityRecord
	if err = json.NewDecoder(reader).Decode(&result); err != nil {
		panic(err)
	}
	return result
})

func SearchCities(query, country string) []City {
	query = strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(query)
	out := make([]City, 0, 50)
	for _, record := range loadCities() {
		if country != "" && record.CountryCode != country {
			continue
		}
		match := true
		for _, term := range terms {
			if !strings.Contains(record.Search, term) {
				match = false
				break
			}
		}
		if match {
			out = append(out, record.City)
			if len(out) == 50 {
				break
			}
		}
	}
	return out
}
func FindCity(id string) (City, bool) {
	for _, record := range loadCities() {
		if record.ID == id {
			return record.City, true
		}
	}
	return City{}, false
}
