package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hiroshi-os/ledgerd/internal/app"
	"github.com/hiroshi-os/ledgerd/internal/config"
	"github.com/hiroshi-os/ledgerd/internal/httpapi"
	"github.com/hiroshi-os/ledgerd/internal/migrate"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		log.Fatal(err)
	}

	a := &app.App{
		Pool:          pool,
		TTL:           cfg.IdempotencyTTL,
		ProcessorSeed: cfg.ProcessorSeed,
	}
	if err := a.BootstrapAPIKey(ctx, cfg.BootstrapAPIKey, cfg.BootstrapKeyName); err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(a),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("ledgerd listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
