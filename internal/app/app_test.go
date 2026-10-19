package app

import (
	"context"
	"encoding/json"
	"errors"
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
	"weather-sum/internal/httpx"
	"weather-sum/internal/metrics"
	"weather-sum/internal/queue"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRetryDelay(t *testing.T) {
	err := &httpx.StatusError{Code: 429, RetryAfter: 12 * time.Second}
	if retryDelay(err, 1) < 12*time.Second {
		t.Fatal("retry_after ignored")
	}
	if retryDelay(errors.New("down"), 1) > 2*time.Second {
		t.Fatal("backoff unbounded")
	}
}
func TestWorkersIsolatePermanentFailure(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test database connect failed")
	}
	defer p.Close()
	_, err = p.Exec(ctx, "TRUNCATE bot_updates,bot_state")
	if err != nil {
		t.Fatal(err)
	}
	s := &queue.Store{Pool: p, Timeout: time.Second, Capacity: 100}
	_ = s.Initialize(ctx, 42)
	var mu sync.Mutex
	calls := map[int64]int{}
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var payload bot.Payload
		_ = json.NewDecoder(req.Body).Decode(&payload)
		mu.Lock()
		calls[payload.ChatID]++
		mu.Unlock()
		body := `{"ok":true}`
		status := 200
		if payload.ChatID == 1 {
			body = `{"ok":false,"error_code":403}`
			status = 403
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	h := bot.NewTelegramCommandHandler(bot.NewTelegramBot("synthetic", nil, nil, nil, nil, client))
	var batch []bot.IncomingMessage
	for i := int64(1); i <= 3; i++ {
		batch = append(batch, bot.IncomingMessage{UpdateID: i, ChatID: i, UserID: i, ChatType: "private", Text: "/help", CommandEntity: true})
	}
	if err = s.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 2)
	c := &config.Config{CommandTimeout: time.Second, SendTimeout: time.Second, MaxAttempts: 2}
	m := &metrics.Metrics{}
	go func() { done <- worker(workerCtx, h, s, c, m) }()
	go func() { done <- worker(workerCtx, h, s, c, m) }()
	for {
		stats, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats["done"] == 2 && stats["failed"] == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("workers blocked")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var code string
	if err := p.QueryRow(ctx, "SELECT failure_code FROM bot_updates WHERE update_id=1").Scan(&code); err != nil || code != string(queue.Forbidden) {
		t.Fatalf("permanent failure not classified: code=%s err=%v", code, err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := int64(1); i <= 3; i++ {
		if calls[i] != 1 {
			t.Fatal("unexpected retries or missing response", calls)
		}
	}
}

// This exercises startup, schema check, identity, ingestion, workers, geocoding,
// confirmation, forecast, deletion and cancellation against real PostgreSQL.
// Only the external HTTPS transport is replaced; no live token is used.
func TestApplicationFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test database connect failed")
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, "TRUNCATE bot_updates,bot_state"); err != nil {
		t.Fatal(err)
	}
	choiceReady := make(chan string, 1)
	deleteReady := make(chan string, 1)
	var mu sync.Mutex
	var replies []string
	sequence := 0
	response := func(body string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	update := func(id int, text string) map[string]any {
		return map[string]any{"update_id": id, "message": map[string]any{"from": map[string]any{"id": 7}, "chat": map[string]any{"id": 7, "type": "private"}, "text": text, "entities": []map[string]any{{"type": "bot_command", "offset": 0, "length": 5}}}}
	}
	callback := func(id int, data string) map[string]any {
		return map[string]any{"update_id": id, "callback_query": map[string]any{"id": fmt.Sprint(id), "from": map[string]any{"id": 7}, "message": map[string]any{"chat": map[string]any{"id": 7, "type": "private"}}, "data": data}}
	}

	fake := transportFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/getMe"):
			return response(`{"ok":true,"result":{"id":42,"username":"TestBot"}}`)
		case strings.HasSuffix(req.URL.Path, "/getUpdates"):
			sequence++
			if sequence == 1 {
				b, _ := json.Marshal(map[string]any{"ok": true, "result": []any{update(1, "/start"), update(2, "/setlocation Test")}})
				return response(string(b))
			}
			if sequence == 2 {
				select {
				case data := <-choiceReady:
					b, _ := json.Marshal(map[string]any{"ok": true, "result": []any{callback(3, data), update(4, "/weather"), update(5, "/deletelocation")}})
					return response(string(b))
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			}
			if sequence == 3 {
				select {
				case data := <-deleteReady:
					b, _ := json.Marshal(map[string]any{"ok": true, "result": []any{callback(6, data), update(7, "/location")}})
					return response(string(b))
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		case strings.HasSuffix(req.URL.Path, "/answerCallbackQuery"):
			return response(`{"ok":true}`)
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			var payload bot.Payload
			_ = json.NewDecoder(req.Body).Decode(&payload)
			mu.Lock()
			replies = append(replies, payload.Text)
			mu.Unlock()
			if payload.ReplyMarkup != nil {
				for _, row := range payload.ReplyMarkup.InlineKeyboard {
					for _, button := range row {
						if strings.HasPrefix(button.CallbackData, "choose:") {
							select {
							case choiceReady <- button.CallbackData:
							default:
							}
						}
						if strings.HasPrefix(button.CallbackData, "delete:") {
							select {
							case deleteReady <- button.CallbackData:
							default:
							}
						}
					}
				}
			}
			return response(`{"ok":true}`)
		case req.URL.Host == "geocoding-api.open-meteo.com":
			return response(`{"results":[{"name":"Test","latitude":1,"longitude":1,"timezone":"UTC"}]}`)
		case req.URL.Host == "api.open-meteo.com":
			now := time.Now().UTC()
			midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			var stamps []int64
			var temps, mm []float64
			var probabilities []int
			for i := 0; i < 48; i++ {
				stamps = append(stamps, midnight.Add(time.Duration(i)*time.Hour).Unix())
				temps = append(temps, 5)
				mm = append(mm, 0)
				probabilities = append(probabilities, 1)
			}
			b, _ := json.Marshal(map[string]any{"hourly": map[string]any{"time": stamps, "temperature_2m": temps, "precipitation": mm, "precipitation_probability": probabilities}})
			return response(string(b))
		default:
			return nil, errors.New("unexpected test endpoint")
		}
	})
	c := &config.Config{TelegramToken: "synthetic", DatabaseURL: dsn, HTTPTimeout: time.Second, DBTimeout: time.Second, PollTimeout: time.Second, SendTimeout: time.Second, CommandTimeout: 3 * time.Second, ShutdownTimeout: time.Second, CacheTTL: time.Minute, ChatCooldown: time.Nanosecond, Workers: 3, QueueCapacity: 100, CacheCapacity: 10, MaxAttempts: 2, RequestsPerSecond: 1000, HealthAddr: "127.0.0.1:0"}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- run(runCtx, c, fake) }()
	for {
		var completed int
		err = pool.QueryRow(ctx, "SELECT count(*) FROM bot_updates WHERE status='done'").Scan(&completed)
		if err == nil && completed == 7 {
			break
		}
		select {
		case err := <-done:
			t.Fatal("application stopped before completing flow", err)
		case <-ctx.Done():
			t.Fatal("application flow timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(replies, "\n")
	for _, want := range []string{"Какой город", "Локация сохранена", "🌡", "Сохранённая локация удалена", "Сначала выберите"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing flow response %s", want)
		}
	}
	var count int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM user_locations WHERE chat_id=7").Scan(&count)
	if count != 0 {
		t.Fatal("location not deleted")
	}
}

func TestPersistentAttemptLimit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test database connect failed")
	}
	defer p.Close()
	_, _ = p.Exec(ctx, "TRUNCATE bot_updates,bot_state")
	s := &queue.Store{Pool: p, Timeout: time.Second, Capacity: 100}
	_ = s.Initialize(ctx, 42)
	message := bot.IncomingMessage{UpdateID: 1, ChatID: 7, UserID: 7, ChatType: "private", Text: "/help", CommandEntity: true}
	if err = s.Ingest(ctx, []bot.IncomingMessage{message}); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Exec(ctx, "UPDATE bot_updates SET attempts=2,status='running'")
	if err = s.Initialize(ctx, 42); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not execute exhausted command")
	})}
	h := bot.NewTelegramCommandHandler(bot.NewTelegramBot("synthetic", nil, nil, nil, nil, client))
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- worker(workerCtx, h, s, &config.Config{MaxAttempts: 2, CommandTimeout: time.Second, SendTimeout: time.Second}, &metrics.Metrics{})
	}()
	for {
		stats, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats["failed"] == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("attempt limit did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("exhausted command executed after restart")
	}
	var code string
	if err := p.QueryRow(ctx, "SELECT failure_code FROM bot_updates WHERE update_id=1").Scan(&code); err != nil || code != string(queue.AttemptsExhausted) {
		t.Fatalf("attempt limit not classified: code=%s err=%v", code, err)
	}
}

func TestWorkersLoad(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test database connect failed")
	}
	defer p.Close()
	_, _ = p.Exec(ctx, "TRUNCATE bot_updates,bot_state")
	s := &queue.Store{Pool: p, Timeout: time.Second, Capacity: 100}
	_ = s.Initialize(ctx, 42)
	var mu sync.Mutex
	active := map[int64]int{}
	inflight, peak := 0, 0
	overlap := false
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var v bot.Payload
		_ = json.NewDecoder(r.Body).Decode(&v)
		mu.Lock()
		active[v.ChatID]++
		inflight++
		peak = max(peak, inflight)
		if active[v.ChatID] > 1 {
			overlap = true
		}
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		active[v.ChatID]--
		inflight--
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	h := bot.NewTelegramCommandHandler(bot.NewTelegramBot("synthetic", nil, nil, nil, nil, client))
	var batch []bot.IncomingMessage
	for i := int64(1); i <= 100; i++ {
		chat := (i-1)%10 + 1
		batch = append(batch, bot.IncomingMessage{UpdateID: i, ChatID: chat, UserID: chat, ChatType: "private", Text: "/help", CommandEntity: true})
	}
	if err = s.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 4)
	m := &metrics.Metrics{}
	start := time.Now()
	for i := 0; i < 4; i++ {
		go func() {
			done <- worker(workerCtx, h, s, &config.Config{MaxAttempts: 2, CommandTimeout: time.Second, SendTimeout: time.Second}, m)
		}()
	}
	for {
		stats, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats["done"] == 100 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("load test stalled")
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
	if overlap || peak > 4 || peak < 2 || m.Commands.Load() != 100 {
		t.Fatal("parallelism/order limit violated", overlap, peak, m.Commands.Load())
	}
	t.Logf("100 commands / 10 chats completed in %s; peak active requests %d; no concurrent commands in one chat", time.Since(start), peak)
}
