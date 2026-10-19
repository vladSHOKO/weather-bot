package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// StatusError contains only trusted numeric fields, never a provider description.
type StatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP/API status %d", e.Code) }

func (e *StatusError) StatusCode() int { return e.Code }

var ErrInvalidResponse = errors.New("invalid external response")

func Decode(r io.Reader, maxBytes int64, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return ErrInvalidResponse
	}
	if int64(len(b)) > maxBytes {
		return ErrInvalidResponse
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return ErrInvalidResponse
	}
	return nil
}
func Retryable(err error) bool {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Code == 429 || status.Code >= 500
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network)
}
func Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// BudgetTransport spaces requests and bounds concurrent network work.
type BudgetTransport struct {
	Base    http.RoundTripper
	mu      sync.Mutex
	next    time.Time
	spacing time.Duration
	slots   chan struct{}
}

func NewBudgetTransport(base http.RoundTripper, perSecond, concurrent int) *BudgetTransport {
	return &BudgetTransport{Base: base, spacing: time.Second / time.Duration(perSecond), slots: make(chan struct{}, concurrent)}
}
func (t *BudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case t.slots <- struct{}{}:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	release := func() { <-t.slots }
	t.mu.Lock()
	now := time.Now()
	start := t.next
	if start.Before(now) {
		start = now
	}
	t.next = start.Add(t.spacing)
	t.mu.Unlock()
	if err := Wait(r.Context(), time.Until(start)); err != nil {
		release()
		return nil, err
	}
	response, err := t.Base.RoundTrip(r)
	if err != nil || response == nil || response.Body == nil {
		release()
		return response, err
	}
	response.Body = &releaseBody{ReadCloser: response.Body, release: release}
	return response, nil
}

type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *releaseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}
func (b *releaseBody) Close() error { defer b.once.Do(b.release); return b.ReadCloser.Close() }
