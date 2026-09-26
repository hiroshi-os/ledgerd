package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// App is the payments service. HTTP is a thin wrapper around these methods.
type App struct {
	Pool          *pgxpool.Pool
	TTL           time.Duration
	ProcessorSeed int64

	mu           sync.Mutex
	beforeCommit func()
}

// Request is the idempotency scope for one POST.
type Request struct {
	Token          string
	IdempotencyKey string
	Method         string
	Path           string
	Body           []byte
}

// Result is a committed HTTP response, or a replay of one.
type Result struct {
	Status       int
	Body         []byte
	Replayed     bool
	DropResponse bool
}

// SetBeforeCommit installs a hook that runs inside the open transaction after
// the effect and the idempotency insert, before COMMIT.
func (a *App) SetBeforeCommit(fn func()) {
	a.mu.Lock()
	a.beforeCommit = fn
	a.mu.Unlock()
}

func (a *App) runBeforeCommit() {
	a.mu.Lock()
	fn := a.beforeCommit
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (a *App) BootstrapAPIKey(ctx context.Context, token, name string) error {
	sum := sha256.Sum256([]byte(token))
	_, err := a.Pool.Exec(ctx, `
		INSERT INTO api_keys (id, token_sha256, name, created_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (token_sha256) DO NOTHING`, uuid.New(), sum[:], name)
	return err
}

func writeEvent(ctx context.Context, tx pgx.Tx, eventType, aggregateID string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (id, type, aggregate_id, payload, created_at)
		VALUES ($1, $2, $3::uuid, $4, now())`, uuid.New(), eventType, aggregateID, b)
	return err
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func newID() string {
	return uuid.NewString()
}
