package main

import (
	"flag"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type healthTransport func(*http.Request) (*http.Response, error)

func (f healthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHealthcheckUsesConfiguredAddress(t *testing.T) {
	for _, env := range []bool{false, true} {
		t.Run(map[bool]string{false: "dotenv", true: "environment"}[env], func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile(".env", []byte("HEALTH_ADDR=127.0.0.1:8087\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HEALTH_ADDR", "127.0.0.1:8088")
			want := "127.0.0.1:8088"
			if !env {
				if err := os.Unsetenv("HEALTH_ADDR"); err != nil {
					t.Fatal(err)
				}
				want = "127.0.0.1:8087"
			}
			oldFlags, oldArgs, oldTransport := flag.CommandLine, os.Args, http.DefaultTransport
			t.Cleanup(func() { flag.CommandLine, os.Args, http.DefaultTransport = oldFlags, oldArgs, oldTransport })
			flag.CommandLine = flag.NewFlagSet("health-test", flag.ContinueOnError)
			os.Args = []string{"weather-sum", "-healthcheck"}
			called := false
			http.DefaultTransport = healthTransport(func(req *http.Request) (*http.Response, error) {
				called = true
				if req.URL.Host != want || req.URL.Path != "/healthz" {
					t.Errorf("unexpected health URL: %s", req.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			if code := start(); code != 0 || !called {
				t.Fatalf("healthcheck failed: code=%d called=%t", code, called)
			}
		})
	}
}
