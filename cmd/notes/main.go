package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/razzie-cloud/notes/internal/app"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/db"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
)

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return errors.New("invalid configuration: " + err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	uri, err := cfg.DatabaseURI(startup)
	if err != nil {
		return errors.New("database discovery failed")
	}
	database, err := sql.Open("pgx", uri)
	if err != nil {
		return errors.New("database configuration failed")
	}
	defer database.Close()
	database.SetMaxOpenConns(3)
	poolCfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		return errors.New("database configuration failed")
	}
	poolCfg.MaxConns = int32(cfg.Workers + 16)
	poolCfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(startup, poolCfg)
	if err != nil {
		return errors.New("database initialization failed")
	}
	defer pool.Close()
	if err = pool.Ping(startup); err != nil {
		return errors.New("database connection failed")
	}
	if err = db.Migrate(startup, database, pool); err != nil {
		return errors.New("database migration failed")
	}
	registry := &storage.Registry{Repo: &store.Repository{Pool: pool}, Config: cfg}
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		return errors.New("queue initialization failed")
	}
	server, err := app.New(cfg, engine)
	if err != nil {
		return errors.New("template initialization failed")
	}
	if err = engine.Client.Start(ctx); err != nil {
		return errors.New("queue start failed")
	}
	go server.Health(ctx)
	httpServer := &http.Server{Addr: ":" + cfg.Port, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if httpServer.Shutdown(shutdown) != nil {
			httpServer.Close()
		}
		if engine.Client.Stop(shutdown) != nil {
			hard, cancelHard := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelHard()
			engine.Client.StopAndCancel(hard)
		}
	}()
	slog.Info("Server listening", "event", "server.listening", "service", "notes", "port", cfg.Port)
	err = httpServer.ListenAndServe()
	stop()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("HTTP server failed")
}
func main() {
	var level slog.Level
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
	case "", "INFO":
		level = slog.LevelInfo
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		slog.Error("Invalid LOG_LEVEL", "event", "config.invalid", "field", "LOG_LEVEL")
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	if err := run(); err != nil {
		slog.Error("Service failed", "event", "server.failed", "error_kind", err.Error())
		os.Exit(1)
	}
}
