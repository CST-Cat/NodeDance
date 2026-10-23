package geolocation

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

func number(v float64) *float64 { return &v }
func TestAutomaticCityCoordinatesAndMissingData(t *testing.T) {
	for _, tc := range []struct {
		extra string
		known bool
	}{
		{`,"city":"Los Angeles","region":"California","latitude":34.0522,"longitude":-118.2437`, true},
		{`,"city":"Null Island","latitude":0,"longitude":0`, true},
		{`,"city":"Missing coordinates"`, false},
		{`,"city":"Out of range","latitude":91,"longitude":0`, false},
		{`,"city":"","latitude":34,"longitude":-118`, false},
	} {
		r := New()
		r.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Query().Get("fields"), "latitude") {
				t.Error("city fields not requested")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ip":"8.8.8.8","success":true,"country_code":"US","country":"United States"` + tc.extra + `}`))}, nil
		})
		country := awaitCountry(t, r, "8.8.8.8")
		location := LocationFromCountry(country)
		if (location.Status == "known") != tc.known || country.Code != "US" {
			t.Fatalf("unexpected %+v %+v", country, location)
		}
		raw, _ := json.Marshal(country)
		if strings.Contains(string(raw), "latitude") {
			t.Fatal("coordinates leaked through country-only view")
		}
	}
}
func TestManualPositionValidationAndCityDirectory(t *testing.T) {
	for _, record := range loadCities() {
		if !ValidPosition(&record.Position) {
			t.Fatalf("directory contains an invalid manual choice: %+v", record.City)
		}
	}
	good := Position{CountryCode: "US", City: "Test", Latitude: number(0), Longitude: number(0)}
	if !ValidPosition(&good) {
		t.Fatal("zero coordinates rejected")
	}
	for _, mutate := range []func(*Position){func(p *Position) { p.Latitude = nil }, func(p *Position) { p.Longitude = number(181) }, func(p *Position) { p.Latitude = number(math.NaN()) }, func(p *Position) { p.CountryCode = "us" }, func(p *Position) { p.City = "bad\ncity" }} {
		invalid := good
		mutate(&invalid)
		if ValidPosition(&invalid) {
			t.Fatal("invalid position accepted", invalid)
		}
	}
	for _, query := range []string{"Los Angeles", "洛杉矶", "San Jose", "New York"} {
		cities := SearchCities(query, "US")
		if len(cities) == 0 || len(cities) > 50 {
			t.Fatal(query, len(cities))
		}
		for _, city := range cities {
			if city.CountryCode != "US" || !ValidPosition(&city.Position) {
				t.Fatal(city)
			}
			found, ok := FindCity(city.ID)
			if !ok || found.City != city.City {
				t.Fatal("directory lookup differs")
			}
		}
	}
	if len(SearchCities("xxxxxxxx-no-such-city", "US")) != 0 {
		t.Fatal("guessed missing city")
	}
}
