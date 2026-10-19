package storage

import (
	"context"
	"errors"
	"time"
	"weather-sum/internal/location"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresUserLocationRepository struct {
	db      *pgxpool.Pool
	timeout time.Duration
}

func NewPostgresUserLocationRepository(db *pgxpool.Pool) *PostgresUserLocationRepository {
	return &PostgresUserLocationRepository{db: db, timeout: 5 * time.Second}
}

func (repo *PostgresUserLocationRepository) Save(ctx context.Context, chatID int64, loc location.Location) error {
	ctx, cancel := context.WithTimeout(ctx, repo.timeout)
	defer cancel()
	if err := loc.Validate(); err != nil {
		return err
	}
	query := "INSERT INTO user_locations (chat_id, location_name, latitude, longitude, timezone) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(chat_id) DO UPDATE SET location_name = EXCLUDED.location_name, latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude, timezone = EXCLUDED.timezone, updated_at = NOW()"
	_, err := repo.db.Exec(ctx, query, chatID, loc.Name, loc.Latitude, loc.Longitude, loc.Timezone)
	if err != nil {
		return err
	}
	return nil
}

func (repo *PostgresUserLocationRepository) GetByChatID(ctx context.Context, chatID int64) (location.Location, error) {
	ctx, cancel := context.WithTimeout(ctx, repo.timeout)
	defer cancel()
	query := "SELECT location_name, latitude, longitude, timezone FROM user_locations WHERE chat_id = $1 LIMIT 1"
	rows, err := repo.db.Query(ctx, query, chatID)
	if err != nil {
		return location.Location{}, err
	}
	loc, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[location.Location])
	if errors.Is(err, pgx.ErrNoRows) {
		return location.Location{}, ErrNotFound
	}
	if err != nil {
		return location.Location{}, err
	}
	return loc, nil
}

func (repo *PostgresUserLocationRepository) Delete(ctx context.Context, chatID int64) error {
	ctx, cancel := context.WithTimeout(ctx, repo.timeout)
	defer cancel()
	_, err := repo.db.Exec(ctx, "DELETE FROM user_locations WHERE chat_id=$1", chatID)
	return err
}
func (repo *PostgresUserLocationRepository) SetTimeout(d time.Duration) { repo.timeout = d }
