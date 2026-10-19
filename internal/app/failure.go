package app

import (
	"context"
	"errors"
	"net"
	"weather-sum/internal/httpx"
	"weather-sum/internal/queue"
	"weather-sum/internal/weather"
)

func failureCode(err error) queue.FailureCode {
	var status *httpx.StatusError
	var network net.Error
	switch {
	case err == nil:
		return queue.Success
	case errors.Is(err, context.Canceled):
		return queue.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return queue.Timeout
	case errors.As(err, &status):
		switch {
		case status.Code == 403:
			return queue.Forbidden
		case status.Code == 429:
			return queue.RateLimited
		case status.Code >= 500 && status.Code <= 599:
			return queue.ServerError
		case status.Code >= 400 && status.Code <= 499:
			return queue.ClientError
		}
	case errors.As(err, &network):
		if network.Timeout() {
			return queue.Timeout
		}
		return queue.NetworkError
	case errors.Is(err, httpx.ErrInvalidResponse):
		return queue.InvalidResponse
	case errors.Is(err, weather.ErrNoData):
		return queue.NoForecastData
	}
	return queue.CommandFailed
}
