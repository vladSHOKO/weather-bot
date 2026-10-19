package weather

import (
	"time"
	"weather-sum/internal/location"
)

type WeatherResult struct {
	Day         ForecastDay
	Location    location.Location
	Period      ForecastPeriod
	Sources     []SourceResult
	Temperature TemperatureSummary
	Rain        RainSummary
	Umbrella    UmbrellaAdvice
	GeneratedAt time.Time
}
type ForecastPeriod struct{ From, To time.Time }
type SourceResult struct {
	SourceName                       string
	IsAvailable                      bool
	State                            string
	Partial                          bool
	MinTemperatureC, MaxTemperatureC float64
	HasTemperature                   bool
	ForecastPoints                   []ForecastPoint
	RainDetected                     bool
}
type ForecastPoint struct {
	Time         time.Time
	TemperatureC *float64
	// Accumulations/probabilities cover [PrecipitationFrom, PrecipitationTo).
	PrecipitationFrom, PrecipitationTo time.Time
	PrecipitationMM                    *float64
	PrecipitationProb                  *int
}
type TemperatureSummary struct {
	MinC, MaxC float64
	Available  bool
}
type RainSummary struct {
	HasRain                    bool
	Known                      bool
	Complete                   bool
	Disagreement               bool
	Intervals                  []RainInterval
	SourcesCount, RainySources int
	Risk                       RainRiskLevel
}
type RainInterval struct {
	Start, End time.Time
	Risk       RainRiskLevel
	Sources    []string
	Partial    bool
}
type UmbrellaAdvice struct {
	Level        UmbrellaAdviceLevel
	Text, Reason string
}
