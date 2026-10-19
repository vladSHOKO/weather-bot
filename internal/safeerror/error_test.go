package safeerror

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestWrapDoesNotExposeCause(t *testing.T) {
	secret := "SYNTHETIC_DATABASE_PASSWORD"
	cause := fmt.Errorf("nested: %w", errors.New("postgres://user:"+secret+"@localhost/db"))
	err := Wrap("database connect", cause)
	output := fmt.Sprintf("%v | %+v | %s", err, err, err)
	if strings.Contains(output, secret) || !strings.Contains(output, "database connect") {
		t.Fatalf("unsafe or unhelpful output: %s", output)
	}
	if !errors.Is(err, cause) {
		t.Fatal("original cause must remain available for classification")
	}
}

func TestWrapPreservesClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"dns", &net.DNSError{Err: "SYNTHETIC_SECRET", Name: "host"}, "network error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := &url.Error{Op: "Get", URL: "https://host/SYNTHETIC_SECRET", Err: tc.err}
			err := Wrap("request", cause)
			if err.Error() != "request: "+tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("unexpected classification: %v", err)
			}
			var wrapped *url.Error
			if !errors.As(err, &wrapped) {
				t.Fatal("errors.As must preserve typed error access")
			}
		})
	}
	if Wrap("request", nil) != nil {
		t.Fatal("successful operations must retain nil error")
	}
}
