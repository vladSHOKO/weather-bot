package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"weather-sum/internal/bot"
	"weather-sum/internal/config"
	"weather-sum/internal/metrics"
	"weather-sum/internal/queue"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable TEST_DATABASE_URL")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal("test database connect failed")
	}
	t.Cleanup(p.Close)
	return p
}

func TestWorkersHTTPRetries(t *testing.T) {
	p := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := p.Exec(ctx, "TRUNCATE bot_updates,bot_state"); err != nil {
		t.Fatal(err)
	}
	s := &queue.Store{Pool: p, Timeout: time.Second, Capacity: 100}
	if err := s.Initialize(ctx, 42); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := map[int64][]time.Time{}
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var payload bot.Payload
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		status := 200
		if strings.Contains(payload.Text, "/setlocation") { // help replies, excluding failure notices
			mu.Lock()
			calls[payload.ChatID] = append(calls[payload.ChatID], time.Now())
			n := len(calls[payload.ChatID])
			mu.Unlock()
			switch payload.ChatID {
			case 1:
				if n == 1 {
					status = 429
				}
			case 2:
				status = 503
			case 3:
				status = 403
			}
		}
		body := `{"ok":true}`
		if status != 200 {
			body = fmt.Sprintf(`{"ok":false,"error_code":%d,"parameters":{"retry_after":2}}`, status)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	h := bot.NewTelegramCommandHandler(bot.NewTelegramBot("synthetic", nil, nil, nil, nil, client))
	var batch []bot.IncomingMessage
	for id := int64(1); id <= 4; id++ {
		batch = append(batch, bot.IncomingMessage{UpdateID: id, ChatID: id, UserID: id, ChatType: "private", Text: "/help", CommandEntity: true})
	}
	if err := s.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			done <- worker(workerCtx, h, s, &config.Config{MaxAttempts: 2, CommandTimeout: time.Second, SendTimeout: time.Second}, &metrics.Metrics{})
		}()
	}
	for {
		stats, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats["done"] == 2 && stats["failed"] == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("HTTP retries did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls[1]) != 2 || len(calls[2]) != 2 || len(calls[3]) != 1 || len(calls[4]) != 1 {
		t.Fatalf("unexpected retry counts: %v", calls)
	}
	if calls[1][1].Sub(calls[1][0]) < 2*time.Second {
		t.Fatal("429 retry_after was ignored")
	}
	var code string
	var attempts int
	if err := p.QueryRow(ctx, "SELECT failure_code,attempts FROM bot_updates WHERE update_id=2").Scan(&code, &attempts); err != nil || code != string(queue.ServerError) || attempts != 2 {
		t.Fatalf("5xx budget/classification: code=%s attempts=%d err=%v", code, attempts, err)
	}
}
