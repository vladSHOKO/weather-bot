package weather

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"weather-sum/internal/location"
)

type securityTransport func(*http.Request) (*http.Response, error)

func (f securityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenWeatherErrorsDoNotExposeKey(t *testing.T) {
	const key = "SYNTHETIC_OPENWEATHER_KEY"
	for _, cause := range []error{
		&net.DNSError{Err: "lookup failed " + key, Name: "api.openweathermap.org"},
		x509.UnknownAuthorityError{},
		context.DeadlineExceeded,
		fmt.Errorf("connection failed: %w", errors.New(key)),
	} {
		client := &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
			return nil, cause
		})}
		provider := NewOpenWeatherProvider(client, key)
		_, err := provider.Forecast(context.Background(), location.Location{}, ForecastPeriod{From: time.Now(), To: time.Now().Add(time.Hour)})
		if err == nil || strings.Contains(fmt.Sprintf("%v %+v", err, err), key) {
			t.Fatalf("unsafe or missing error: %v", err)
		}
		if !errors.Is(err, cause) {
			t.Fatal("network cause lost")
		}
	}
}
