package storage

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"weather-sum/internal/location"
)

func TestRepositoryCRUD(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable TEST_DATABASE_URL")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test database connect failed")
	}
	defer p.Close()
	r := NewPostgresUserLocationRepository(p)
	l := location.Location{Name: "Test", Latitude: 0, Longitude: 0, Timezone: "UTC"}
	const id = 9001
	if err = r.Save(ctx, id, l); err != nil {
		t.Fatal(err)
	}
	l.Name = "Changed"
	if err = r.Save(ctx, id, l); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetByChatID(ctx, id)
	if err != nil || got.Name != "Changed" {
		t.Fatalf("%+v %v", got, err)
	}
	if err = r.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = r.GetByChatID(ctx, id)
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	l.Latitude = 100
	if r.Save(ctx, id, l) == nil {
		t.Fatal("invalid coordinates saved")
	}
}
