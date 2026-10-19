// Package queue implements a bounded, durable inbox. Telegram is acknowledged
// only after ingestion commits, not after an in-memory dispatch.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
	"time"
	"weather-sum/internal/bot"
)

var ErrFull = errors.New("inbox is full")

type Item struct {
	Message  bot.IncomingMessage
	Attempts int
}
type Store struct {
	Pool     *pgxpool.Pool
	Timeout  time.Duration
	Capacity int
}

func (s *Store) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.Timeout)
}
func (s *Store) Initialize(ctx context.Context, botID int64) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO bot_state(id,bot_id) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET bot_id=bot_state.bot_id WHERE bot_state.bot_id=$1 RETURNING bot_id`, botID).Scan(&id)
	if err != nil {
		return fmt.Errorf("database belongs to another bot or is unavailable")
	}
	if _, err = tx.Exec(ctx, `UPDATE bot_updates SET status='pending' WHERE status='running'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Offset(ctx context.Context) (int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT next_offset FROM bot_state WHERE id=1`).Scan(&n)
	return n, err
}
func (s *Store) Ingest(ctx context.Context, messages []bot.IncomingMessage) error {
	if len(messages) == 0 {
		return nil
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var offset int64
	if err = tx.QueryRow(ctx, `SELECT next_offset FROM bot_state WHERE id=1 FOR UPDATE`).Scan(&offset); err != nil {
		return err
	}
	var pending int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM bot_updates WHERE status IN ('pending','running')`).Scan(&pending); err != nil {
		return err
	}
	// Conservatively reject a full batch; never acknowledge an unsaved suffix.
	messages = slices.Clone(messages)
	slices.SortFunc(messages, func(a, b bot.IncomingMessage) int {
		if a.UpdateID < b.UpdateID {
			return -1
		}
		if a.UpdateID > b.UpdateID {
			return 1
		}
		return 0
	})
	perChat := map[int64]int{}
	rows, err := tx.Query(ctx, `SELECT chat_id,count(*) FROM bot_updates WHERE status IN ('pending','running') GROUP BY chat_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var n int
		if err = rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		perChat[id] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	newCount := 0
	for i := range messages {
		m := &messages[i]
		if m.UpdateID < offset || m.Ignore {
			continue
		}
		if perChat[m.ChatID] >= 20 {
			m.Ignore = true
			continue
		}
		perChat[m.ChatID]++
		newCount++
	}
	if pending+newCount > s.Capacity {
		return ErrFull
	}
	for _, m := range messages {
		if m.UpdateID < offset {
			continue
		}
		if m.UpdateID < 0 || m.UpdateID == int64(^uint64(0)>>1) {
			return fmt.Errorf("invalid update id")
		}
		payload, err := json.Marshal(m)
		if err != nil {
			return err
		}
		state := "pending"
		var finished *time.Time
		if m.Ignore {
			m.ChatID = 0
			state = "done"
			payload = []byte("{}")
			now := time.Now()
			finished = &now
		}
		if _, err = tx.Exec(ctx, `INSERT INTO bot_updates(update_id,chat_id,payload,status,finished_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(update_id) DO NOTHING`, m.UpdateID, m.ChatID, payload, state, finished); err != nil {
			return err
		}
		offset = max(offset, m.UpdateID+1)
	}
	if _, err = tx.Exec(ctx, `UPDATE bot_state SET next_offset=GREATEST(next_offset,$1) WHERE id=1`, offset); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Claim(ctx context.Context) (Item, bool, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var payload []byte
	var item Item
	err := s.Pool.QueryRow(ctx, `UPDATE bot_updates SET status='running',attempts=attempts+1 WHERE update_id=(
 SELECT u.update_id FROM bot_updates u WHERE u.status='pending' AND u.available_at<=now()
 AND NOT EXISTS(SELECT 1 FROM bot_updates earlier WHERE earlier.chat_id=u.chat_id AND earlier.update_id<u.update_id AND earlier.status IN ('pending','running'))
 ORDER BY u.update_id FOR UPDATE OF u SKIP LOCKED LIMIT 1) RETURNING payload,attempts`).Scan(&payload, &item.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if err = json.Unmarshal(payload, &item.Message); err != nil {
		return item, false, err
	}
	return item, true, nil
}

// FailureCode is a bounded classification; external error text is never stored.
type FailureCode string

const (
	Success           FailureCode = ""
	CommandFailed     FailureCode = "command_failed"
	AttemptsExhausted FailureCode = "attempts_exhausted"
	Canceled          FailureCode = "canceled"
	Timeout           FailureCode = "timeout"
	NetworkError      FailureCode = "network_error"
	Forbidden         FailureCode = "http_403"
	RateLimited       FailureCode = "http_429"
	ClientError       FailureCode = "http_4xx"
	ServerError       FailureCode = "http_5xx"
	InvalidResponse   FailureCode = "invalid_response"
	NoForecastData    FailureCode = "no_forecast_data"
)

func (s *Store) Finish(ctx context.Context, id int64, code FailureCode) error {
	switch code {
	case Success, CommandFailed, AttemptsExhausted, Canceled, Timeout, NetworkError,
		Forbidden, RateLimited, ClientError, ServerError, InvalidResponse, NoForecastData:
	default:
		return fmt.Errorf("invalid inbox failure code")
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	state := "done"
	if code != Success {
		state = "failed"
	}
	_, err := s.Pool.Exec(ctx, `UPDATE bot_updates SET status=$2,payload='{}',chat_id=0,finished_at=now(),failure_code=$3 WHERE update_id=$1`, id, state, code)
	return err
}
func (s *Store) Retry(ctx context.Context, id int64, delay time.Duration) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.Pool.Exec(ctx, `UPDATE bot_updates SET status='pending',available_at=now()+($2 * interval '1 millisecond') WHERE update_id=$1`, id, delay.Milliseconds())
	return err
}
func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	out := map[string]int64{"pending": 0, "running": 0, "failed": 0}
	rows, err := s.Pool.Query(ctx, `SELECT status,count(*) FROM bot_updates GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int64
		if err = rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}
func (s *Store) Cleanup(ctx context.Context) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.Pool.Exec(ctx, `DELETE FROM bot_updates WHERE update_id IN (SELECT update_id FROM bot_updates WHERE finished_at<now()-interval '7 days' ORDER BY update_id LIMIT 1000)`)
	return err
}

// Forget removes queued user content once the deletion command reaches the
// front of its chat. Later commands explicitly issued by the user remain valid.
func (s *Store) Forget(ctx context.Context, chatID, throughID int64) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.Pool.Exec(ctx, `UPDATE bot_updates SET payload='{}' WHERE chat_id=$1 AND update_id<$2`, chatID, throughID)
	return err
}
