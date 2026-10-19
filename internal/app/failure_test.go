package app

import (
	"context"
	"errors"
	"net"
	"testing"
	"weather-sum/internal/httpx"
	"weather-sum/internal/queue"
	"weather-sum/internal/safeerror"
	"weather-sum/internal/weather"
)

func TestFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want queue.FailureCode
	}{
		{nil, queue.Success},
		{context.Canceled, queue.Canceled},
		{context.DeadlineExceeded, queue.Timeout},
		{&net.DNSError{Err: "SYNTHETIC_SECRET", IsTimeout: true}, queue.Timeout},
		{&net.DNSError{Err: "SYNTHETIC_SECRET"}, queue.NetworkError},
		{&httpx.StatusError{Code: 403}, queue.Forbidden},
		{&httpx.StatusError{Code: 429}, queue.RateLimited},
		{&httpx.StatusError{Code: 401}, queue.ClientError},
		{&httpx.StatusError{Code: 503}, queue.ServerError},
		{httpx.ErrInvalidResponse, queue.InvalidResponse},
		{weather.ErrNoData, queue.NoForecastData},
		{errors.New("SYNTHETIC_SECRET"), queue.CommandFailed},
	} {
		if got := failureCode(safeerror.Wrap("boundary", tc.err)); got != tc.want {
			t.Errorf("code=%s want=%s", got, tc.want)
		}
	}
}
