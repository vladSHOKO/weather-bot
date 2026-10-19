package bot

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type securityTransport func(*http.Request) (*http.Response, error)

func (f securityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramErrorsDoNotExposeToken(t *testing.T) {
	const token = "SYNTHETIC_TELEGRAM_TOKEN"
	for _, cause := range []error{
		&net.DNSError{Err: "lookup failed " + token, Name: "api.telegram.org"},
		x509.UnknownAuthorityError{},
		context.DeadlineExceeded,
		fmt.Errorf("connection failed: %w", errors.New(token)),
	} {
		client := &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
			return nil, cause
		})}
		b := NewTelegramBot(token, nil, nil, nil, nil, client)
		_, pollErr := b.GetUpdates(context.Background(), 0)
		_, sendErr := b.SendMessage(context.Background(), 1, "test")
		for _, err := range []error{pollErr, sendErr} {
			if err == nil || strings.Contains(fmt.Sprintf("%v %+v", err, err), token) {
				t.Fatalf("unsafe or missing error: %v", err)
			}
			if !errors.Is(err, cause) {
				t.Fatal("network cause lost")
			}
		}
	}
}

func TestTelegramResponseDescriptionCannotLeakSecret(t *testing.T) {
	const token = "SYNTHETIC_TELEGRAM_TOKEN"
	client := &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"` + token + `"}`))}, nil
	})}
	b := NewTelegramBot(token, nil, nil, nil, nil, client)
	_, err := b.SendMessage(context.Background(), 1, "test")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("unsafe or missing error: %v", err)
	}
}
