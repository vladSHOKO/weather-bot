package weather

import (
	"context"
	"weather-sum/internal/location"
)

type Provider interface {
	Name() string
	Forecast(ctx context.Context, loc location.Location, period ForecastPeriod) ([]ForecastPoint, error)
}
