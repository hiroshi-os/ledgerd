package testutil

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hiroshi-os/ledgerd/internal/app"
	"github.com/hiroshi-os/ledgerd/internal/migrate"
)

const BootstrapKey = "sk_test_ledgerd"

var (
	once sync.Once
	pool *pgxpool.Pool
	err  error
)

func DatabaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://ledgerd:ledgerd@localhost:5432/ledgerd_test?sslmode=disable"
}

func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pool, err = pgxpool.New(ctx, DatabaseURL())
		if err != nil {
			return
		}
		err = pool.Ping(ctx)
		if err != nil {
			return
		}
		err = migrate.Up(ctx, pool)
	})
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	return pool
}

func NewApp(t *testing.T) *app.App {
	t.Helper()
	p := Pool(t)
	a := &app.App{
		Pool:          p,
		TTL:           24 * time.Hour,
		ProcessorSeed: 1,
	}
	ctx := context.Background()
	if err := a.Reset(ctx, BootstrapKey, "test"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	return a
}
