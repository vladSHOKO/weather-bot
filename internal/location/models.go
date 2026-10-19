package location

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type Location struct {
	Name      string  `db:"location_name"`
	Latitude  float64 `db:"latitude"`
	Longitude float64 `db:"longitude"`
	Timezone  string  `db:"timezone"`
	Country   string  `db:"-"`
	Region    string  `db:"-"`
}

func (l Location) Validate() error {
	if strings.TrimSpace(l.Name) == "" || len(l.Name) > 256 || len(l.Region) > 128 || len(l.Country) > 128 || math.IsNaN(l.Latitude) || math.IsNaN(l.Longitude) || math.IsInf(l.Latitude, 0) || math.IsInf(l.Longitude, 0) || l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 || l.Timezone == "" {
		return fmt.Errorf("invalid location")
	}
	if _, err := time.LoadLocation(l.Timezone); err != nil {
		return fmt.Errorf("invalid location timezone")
	}
	return nil
}
func (l Location) Label() string {
	parts := []string{l.Name}
	if l.Region != "" {
		parts = append(parts, l.Region)
	}
	if l.Country != "" {
		parts = append(parts, l.Country)
	}
	return strings.Join(parts, ", ")
}
