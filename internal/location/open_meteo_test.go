package location

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCandidateValidationAndEscaping(t *testing.T) {
	for _, body := range []string{`{"results":[{"name":"A","latitude":91,"longitude":0,"timezone":"UTC"}]}`, `{"results":[{"name":"A","longitude":0,"timezone":"UTC"}]}`, `{"results":[{"name":"A","latitude":0,"longitude":0,"timezone":"bad/zone"}]}`, `null`} {
		client := &http.Client{Timeout: time.Second, Transport: testTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		if _, err := NewOpenMeteoGeocoder(client).Candidates(context.Background(), "Test"); err == nil {
			t.Fatal("invalid candidate accepted")
		}
	}
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("name") != "A&B" || r.URL.Query().Get("count") != "5" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"results":[{"name":"A","latitude":0,"longitude":0,"timezone":"UTC","country":"Country","admin1":"Region"}]}`))}, nil
	})}
	g := NewOpenMeteoGeocoder(client)
	for i := 0; i < 2; i++ {
		list, err := g.Candidates(context.Background(), "A&B")
		if err != nil || len(list) != 1 || list[0].Label() != "A, Region, Country" {
			t.Fatalf("%v %v", list, err)
		}
	}
	if calls != 1 {
		t.Fatal("geocoding cache unused")
	}
}
