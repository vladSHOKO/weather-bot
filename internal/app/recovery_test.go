package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"weather-sum/internal/bot"
	"weather-sum/internal/config"
	"weather-sum/internal/metrics"
	"weather-sum/internal/queue"
)

func TestRecoveryAfterSuccessfulSendAndFailedCompletion(t *testing.T) {
	p := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.Exec(ctx, "TRUNCATE bot_updates,bot_state"); err != nil {
		t.Fatal(err)
	}
	s := &queue.Store{Pool: p, Timeout: time.Second, Capacity: 100}
	if err := s.Initialize(ctx, 42); err != nil {
		t.Fatal(err)
	}
	message := bot.IncomingMessage{UpdateID: 1, ChatID: 7, UserID: 7, ChatType: "private", Text: "/help", CommandEntity: true}
	if err := s.Ingest(ctx, []bot.IncomingMessage{message}); err != nil {
		t.Fatal(err)
	}
	_, err := p.Exec(ctx, `CREATE FUNCTION test_reject_completion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.status='done' THEN RAISE EXCEPTION 'synthetic completion failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER test_reject_completion BEFORE UPDATE ON bot_updates FOR EACH ROW EXECUTE FUNCTION test_reject_completion()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), "DROP FUNCTION IF EXISTS test_reject_completion() CASCADE")
	})
	var sends atomic.Int64
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		sends.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	h := bot.NewTelegramCommandHandler(bot.NewTelegramBot("synthetic", nil, nil, nil, nil, client))
	c := &config.Config{MaxAttempts: 3, CommandTimeout: time.Second, SendTimeout: time.Second}
	if err := worker(ctx, h, s, c, &metrics.Metrics{}); err == nil {
		t.Fatal("completion write failure did not stop worker")
	}
	var state string
	if err := p.QueryRow(ctx, "SELECT status FROM bot_updates WHERE update_id=1").Scan(&state); err != nil || state != "running" || sends.Load() != 1 {
		t.Fatalf("unacknowledged completion lost: state=%s sends=%d err=%v", state, sends.Load(), err)
	}
	if _, err := p.Exec(ctx, "DROP FUNCTION test_reject_completion() CASCADE"); err != nil {
		t.Fatal(err)
	}
	if err := s.Initialize(ctx, 42); err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- worker(workerCtx, h, s, c, &metrics.Metrics{}) }()
	for {
		stats, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats["done"] == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery stalled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 2 {
		t.Fatal("uncertain external send must be replayable", sends.Load())
	}
	if err := s.Initialize(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(ctx); err != nil || ok {
		t.Fatal("finished event replayed", err)
	}
}
