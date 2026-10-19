package storage

import (
	"context"
	"errors"
	"weather-sum/internal/location"
)

var ErrNotFound = errors.New("location not found in storage")

type UserLocationRepository interface {
	Save(context.Context, int64, location.Location) error
	GetByChatID(context.Context, int64) (location.Location, error)
	Delete(context.Context, int64) error
}
