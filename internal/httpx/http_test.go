package httpx

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDecodeBounds(t *testing.T) {
	for _, body := range []string{`{"x":"` + strings.Repeat("a", 100) + `"}`, `{"x":`, `null`, `{} {}`} {
		var dst map[string]any
		if err := Decode(strings.NewReader(body), 32, &dst); err == nil {
			t.Fatal("accepted invalid/oversize response")
		}
	}
}
func TestRetryPolicy(t *testing.T) {
	for _, code := range []int{400, 401, 403, 409, 429, 500, 503} {
		if Retryable(&StatusError{Code: code}) != (code == 429 || code >= 500) {
			t.Fatal(code)
		}
	}
	if Retryable(errors.New("permanent")) {
		t.Fatal("unexpected retry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Wait(ctx, time.Hour) == nil {
		t.Fatal("wait ignored cancellation")
	}
}

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBudgetCancellation(t *testing.T) {
	tr := NewBudgetTransport(fakeTransport(func(*http.Request) (*http.Response, error) { return &http.Response{}, nil }), 1, 1)
	req, _ := http.NewRequest("GET", "https://example.test", nil)
	_, _ = tr.RoundTrip(req)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := tr.RoundTrip(req.WithContext(ctx))
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("budget wait not canceled")
	}
}
