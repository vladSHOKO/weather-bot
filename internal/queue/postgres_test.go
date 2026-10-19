package queue

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sync"
	"testing"
	"time"
	"weather-sum/internal/bot"
)

func database(t *testing.T) *Store {
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
	if _, err = p.Exec(context.Background(), "TRUNCATE bot_updates,bot_state"); err != nil {
		t.Fatal(err)
	}
	s := &Store{Pool: p, Timeout: time.Second * 5, Capacity: 100}
	if err = s.Initialize(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	return s
}
func event(id, chat int64) bot.IncomingMessage {
	return bot.IncomingMessage{UpdateID: id, ChatID: chat, UserID: chat, ChatType: "private", Text: "/help", CommandEntity: true}
}
func TestFinishRejectsArbitraryFailureText(t *testing.T) {
	s := &Store{}
	err := s.Finish(context.Background(), 1, FailureCode("SYNTHETIC_SECRET"))
	if err == nil || err.Error() != "invalid inbox failure code" {
		t.Fatal("untrusted failure text accepted or exposed")
	}
}
func TestCheckpointOrderingRecovery(t *testing.T) {
	ctx := context.Background()
	s := database(t)
	batch := []bot.IncomingMessage{event(12, 2), event(10, 1), event(11, 1)}
	if err := s.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	offset, _ := s.Offset(ctx)
	if offset != 13 {
		t.Fatal(offset)
	}
	_ = s.Ingest(ctx, nil)
	_ = s.Ingest(ctx, batch)
	offset, _ = s.Offset(ctx)
	if offset != 13 {
		t.Fatal("checkpoint regressed")
	}
	first, ok, err := s.Claim(ctx)
	if err != nil || !ok || first.Message.UpdateID != 10 {
		t.Fatalf("%+v %v", first, err)
	}
	other, ok, err := s.Claim(ctx)
	if err != nil || !ok || other.Message.UpdateID != 12 {
		t.Fatalf("chat order violated: %+v %v", other, err)
	}
	_ = s.Finish(ctx, 12, Success)
	if _, ok, _ = s.Claim(ctx); ok {
		t.Fatal("same chat overtook running event")
	}
	if err = s.Initialize(ctx, 42); err != nil {
		t.Fatal(err)
	}
	retry, ok, _ := s.Claim(ctx)
	if !ok || retry.Message.UpdateID != 10 || retry.Attempts != 2 {
		t.Fatal("restart did not recover running item")
	}
	_ = s.Finish(ctx, 10, CommandFailed)
	next, ok, _ := s.Claim(ctx)
	if !ok || next.Message.UpdateID != 11 {
		t.Fatal("failed event blocked successor")
	}
	_ = s.Finish(ctx, 11, Success)
	var erased bool
	if err = s.Pool.QueryRow(ctx, "SELECT bool_and(payload='{}'::jsonb AND chat_id=0) FROM bot_updates").Scan(&erased); err != nil || !erased {
		t.Fatal("processed content retained", err)
	}
	if err = s.Initialize(ctx, 99); err == nil {
		t.Fatal("another bot identity accepted")
	}
}
func TestCapacityRollbackAndChatFlood(t *testing.T) {
	ctx := context.Background()
	s := database(t)
	s.Capacity = 1
	_ = s.Ingest(ctx, []bot.IncomingMessage{event(1, 1)})
	if !errors.Is(s.Ingest(ctx, []bot.IncomingMessage{event(2, 2)}), ErrFull) {
		t.Fatal("capacity ignored")
	}
	offset, _ := s.Offset(ctx)
	if offset != 2 {
		t.Fatal("unsaved event acknowledged")
	}
	_, _ = s.Pool.Exec(ctx, "TRUNCATE bot_updates,bot_state")
	_ = s.Initialize(ctx, 42)
	s.Capacity = 100
	var batch []bot.IncomingMessage
	for i := int64(1); i <= 100; i++ {
		batch = append(batch, event(i, 1))
	}
	if err := s.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	stats, _ := s.Stats(ctx)
	if stats["pending"] != 20 || stats["done"] != 80 {
		t.Fatal(stats)
	}
	offset, _ = s.Offset(ctx)
	if offset != 101 {
		t.Fatal(offset)
	}
}
func TestConcurrentClaims(t *testing.T) {
	ctx := context.Background()
	s := database(t)
	var batch []bot.IncomingMessage
	for i := int64(1); i <= 10; i++ {
		batch = append(batch, event(i, i))
	}
	_ = s.Ingest(ctx, batch)
	ids := make(chan int64, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, ok, err := s.Claim(ctx)
			if err != nil || !ok {
				ids <- 0
				return
			}
			ids <- v.Message.UpdateID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if id == 0 || seen[id] {
			t.Fatal("duplicate or missing claim", id)
		}
		seen[id] = true
	}
}

func TestIngestionFailureRollsBackBatchAndCheckpoint(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	_, err := s.Pool.Exec(ctx, `CREATE FUNCTION test_reject_ingestion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.update_id=2 THEN RAISE EXCEPTION 'synthetic write failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER test_reject_ingestion BEFORE INSERT ON bot_updates FOR EACH ROW EXECUTE FUNCTION test_reject_ingestion()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.Pool.Exec(context.Background(), "DROP FUNCTION test_reject_ingestion() CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if err := s.Ingest(ctx, []bot.IncomingMessage{event(1, 1), event(2, 2)}); err == nil {
		t.Fatal("injected write failure ignored")
	}
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM bot_updates").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial batch survived rollback: count=%d err=%v", count, err)
	}
	offset, err := s.Offset(ctx)
	if err != nil || offset != 0 {
		t.Fatalf("unsaved batch acknowledged: offset=%d err=%v", offset, err)
	}
}
