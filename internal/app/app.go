package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"time"
	"weather-sum/internal/bot"
	"weather-sum/internal/config"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/metrics"
	"weather-sum/internal/queue"
	"weather-sum/internal/report"
	"weather-sum/internal/safeerror"
	"weather-sum/internal/storage"
	"weather-sum/internal/weather"
)

const schemaVersion int64 = 20261018000000
const pollerLock int64 = 741739022

func Run(parent context.Context, c *config.Config) error { return run(parent, c, nil) }

func run(parent context.Context, c *config.Config, override http.RoundTripper) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return safeerror.Wrap("database configuration", err)
	}
	poolConfig.ConnConfig.ConnectTimeout = c.DBTimeout
	poolConfig.MaxConns = int32(c.Workers + 6)
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return safeerror.Wrap("database connect", err)
	}
	defer pool.Close()
	startup, stopStartup := context.WithTimeout(ctx, c.DBTimeout)
	err = pool.Ping(startup)
	if err == nil {
		var ready bool
		err = pool.QueryRow(startup, `SELECT COALESCE((SELECT is_applied FROM bot_schema_version WHERE version_id=$1),false)`, schemaVersion).Scan(&ready)
		if err == nil && !ready {
			err = fmt.Errorf("missing schema version")
		}
	}
	stopStartup()
	if err != nil {
		return safeerror.Wrap("database schema check: run make migrate and make db-grants", err)
	}
	lockCtx, stopLock := context.WithTimeout(ctx, c.DBTimeout)
	lock, err := pool.Acquire(lockCtx)
	stopLock()
	if err != nil {
		return safeerror.Wrap("poller lock connection", err)
	}
	defer lock.Release()
	var acquired bool
	lockCtx, stopLock = context.WithTimeout(ctx, c.DBTimeout)
	err = lock.QueryRow(lockCtx, "SELECT pg_try_advisory_lock($1)", pollerLock).Scan(&acquired)
	stopLock()
	if err != nil {
		return safeerror.Wrap("poller lock", err)
	}
	if !acquired {
		return fmt.Errorf("another bot instance is already using this database")
	}
	defer func() {
		releaseCtx, done := context.WithTimeout(context.Background(), c.DBTimeout)
		defer done()
		_, _ = lock.Exec(releaseCtx, "SELECT pg_advisory_unlock($1)", pollerLock)
	}()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxConnsPerHost = c.Workers + 2
	base.MaxIdleConnsPerHost = c.Workers + 2
	defer base.CloseIdleConnections()
	transport := http.RoundTripper(base)
	if override != nil {
		transport = override
	}
	budget := httpx.NewBudgetTransport(transport, c.RequestsPerSecond, c.Workers+2)
	weatherClient := &http.Client{Timeout: c.HTTPTimeout, Transport: budget}
	sendClient := &http.Client{Timeout: c.SendTimeout, Transport: budget}
	pollClient := &http.Client{Timeout: c.PollTimeout + 5*time.Second, Transport: transport}
	m := &metrics.Metrics{}
	providers := []weather.Provider{weather.Cached(weather.NewOpenMeteoProvider(weatherClient), c.CacheCapacity, c.CacheTTL, c.HTTPTimeout, m)}
	if c.OpenWeatherAPIKey != "" {
		providers = append(providers, weather.Cached(weather.NewOpenWeatherProvider(weatherClient, c.OpenWeatherAPIKey), c.CacheCapacity, c.CacheTTL, c.HTTPTimeout, m))
	}
	if c.WeatherAPIKey != "" {
		providers = append(providers, weather.Cached(weather.NewWeatherAPIProvider(weatherClient, c.WeatherAPIKey), c.CacheCapacity, c.CacheTTL, c.HTTPTimeout, m))
	}
	if c.MetNoUserAgent != "" {
		providers = append(providers, weather.Cached(weather.NewMetNoProvider(weatherClient, c.MetNoUserAgent, c.CacheCapacity), c.CacheCapacity, c.CacheTTL, c.HTTPTimeout, m))
	}
	repo := storage.NewPostgresUserLocationRepository(pool)
	repo.SetTimeout(c.DBTimeout)
	telegram := bot.NewTelegramBot(c.TelegramToken, repo, location.NewOpenMeteoGeocoder(weatherClient), weather.NewService(providers...), report.NewTelegramRenderer(), sendClient)
	telegram.SetPollClient(pollClient, c.PollTimeout)
	identityCtx, stopIdentity := context.WithTimeout(ctx, c.SendTimeout)
	botID, err := telegram.GetMe(identityCtx)
	stopIdentity()
	if err != nil {
		return err
	}
	store := &queue.Store{Pool: pool, Timeout: c.DBTimeout, Capacity: c.QueueCapacity}
	if err = store.Initialize(ctx, botID); err != nil {
		return safeerror.Wrap("inbox initialization: check bot identity and grants", err)
	}
	handler := bot.NewTelegramCommandHandler(telegram)
	handler.Cooldown = c.ChatCooldown
	handler.Metrics = m
	handler.OnForget = store.Forget
	listener, err := net.Listen("tcp", c.HealthAddr)
	if err != nil {
		return safeerror.Wrap("health listener", err)
	}
	group, workCtx := errgroup.WithContext(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		check, done := context.WithTimeout(r.Context(), time.Second)
		defer done()
		if pool.Ping(check) != nil || workCtx.Err() != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		stats, err := store.Stats(r.Context())
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"counters": m.Snapshot(), "queue": stats})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: c.DBTimeout + time.Second, IdleTimeout: 10 * time.Second}
	group.Go(func() error {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return safeerror.Wrap("health server", err)
	})
	group.Go(func() error {
		<-workCtx.Done()
		shutdown, done := context.WithTimeout(context.Background(), c.ShutdownTimeout)
		defer done()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
		return nil
	})
	group.Go(func() error { return poll(workCtx, telegram, store, c.MaxAttempts) })
	for i := 0; i < c.Workers; i++ {
		group.Go(func() error { return worker(workCtx, handler, store, c, m) })
	}
	group.Go(func() error {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		cycles := 0
		for {
			select {
			case <-workCtx.Done():
				return nil
			case <-ticker.C:
				check, done := context.WithTimeout(workCtx, c.DBTimeout)
				err := lock.Ping(check)
				done()
				if err != nil {
					return safeerror.Wrap("poller lock heartbeat", err)
				}
				cycles++
				if cycles%6 == 0 {
					if err = store.Cleanup(workCtx); err != nil {
						return safeerror.Wrap("inbox retention", err)
					}
					stats, err := store.Stats(workCtx)
					if err != nil {
						return safeerror.Wrap("inbox metrics", err)
					}
					slog.Info("runtime metrics", "queue", stats, "counters", m.Snapshot())
				}
			}
		}
	})
	slog.Info("bot started", "workers", c.Workers, "sources", len(providers), "health", c.HealthAddr)
	err = group.Wait()
	if parent.Err() != nil {
		return nil
	}
	return err
}
func poll(ctx context.Context, b *bot.TelegramBot, s *queue.Store, maxAttempts int) error {
	failures := 0
	for ctx.Err() == nil {
		offset, err := s.Offset(ctx)
		if err != nil {
			return safeerror.Wrap("inbox checkpoint", err)
		}
		updates, err := b.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			failures++
			slog.Warn("poll failed", "error", err, "attempt", failures)
			if !httpx.Retryable(err) || failures >= maxAttempts {
				return err
			}
			if httpx.Wait(ctx, retryDelay(err, failures)) != nil {
				return nil
			}
			continue
		}
		failures = 0
		if err = s.Ingest(ctx, updates.Messages); err != nil {
			if errors.Is(err, queue.ErrFull) {
				if httpx.Wait(ctx, time.Second) != nil {
					return nil
				}
				continue
			}
			return safeerror.Wrap("inbox ingestion", err)
		}
		if len(updates.Messages) == 0 {
			if httpx.Wait(ctx, 100*time.Millisecond) != nil {
				return nil
			}
		}
	}
	return nil
}
func worker(ctx context.Context, h *bot.TelegramCommandHandler, s *queue.Store, c *config.Config, m *metrics.Metrics) error {
	for ctx.Err() == nil {
		item, ok, err := s.Claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return safeerror.Wrap("inbox claim", err)
		}
		if !ok {
			if httpx.Wait(ctx, 100*time.Millisecond) != nil {
				return nil
			}
			continue
		}
		if item.Attempts > c.MaxAttempts {
			if err := s.Finish(ctx, item.Message.UpdateID, queue.AttemptsExhausted); err != nil {
				return safeerror.Wrap("inbox attempt limit", err)
			}
			continue
		}
		started := time.Now()
		commandCtx, done := context.WithTimeout(ctx, c.CommandTimeout)
		err = h.Handle(commandCtx, item.Message)
		done()
		m.Commands.Add(1)
		m.CommandNanos.Add(time.Since(started).Nanoseconds())
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			m.Failures.Add(1)
			slog.Warn("command failed", "update_id", item.Message.UpdateID, "attempt", item.Attempts, "error", safeerror.Wrap("command", err))
			if httpx.Retryable(err) && item.Attempts < c.MaxAttempts {
				if err = s.Retry(ctx, item.Message.UpdateID, retryDelay(err, item.Attempts)); err != nil {
					return safeerror.Wrap("inbox retry", err)
				}
				continue
			}
			var status *httpx.StatusError
			if !errors.As(err, &status) || status.Code != 403 {
				notice, stop := context.WithTimeout(ctx, c.SendTimeout)
				_ = h.NotifyFailure(notice, item.Message)
				stop()
			}
		}
		if finishErr := s.Finish(ctx, item.Message.UpdateID, failureCode(err)); finishErr != nil {
			return safeerror.Wrap("inbox completion", finishErr)
		}
	}
	return nil
}
func retryDelay(err error, attempt int) time.Duration {
	delay := time.Second*time.Duration(1<<min(attempt-1, 5)) + time.Duration(rand.IntN(500))*time.Millisecond
	var status *httpx.StatusError
	if errors.As(err, &status) && status.RetryAfter > delay {
		delay = status.RetryAfter
	}
	return delay
}
