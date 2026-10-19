package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"weather-sum/internal/app"
	"weather-sum/internal/config"
)

func main() { os.Exit(start()) }
func start() int {
	check := flag.Bool("healthcheck", false, "check local readiness endpoint")
	flag.Parse()
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "invalid .env file")
		return 1
	}
	if *check {
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://" + healthAddress() + "/healthz")
		if err != nil {
			return 1
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return 1
		}
		return 0
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = app.Run(ctx, cfg); err != nil {
		slog.Error("application stopped", "error", err)
		return 1
	}
	return 0
}

func healthAddress() string {
	if addr := os.Getenv("HEALTH_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:8080"
}
