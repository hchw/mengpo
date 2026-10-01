package postgres

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"

	"github.com/hchw/mengpo/internal/ports"
)

// AdvisoryLocker uses PostgreSQL session-scoped advisory locks so a scheduled
// trigger executes at most once across worker replicas. The lock is held on a
// dedicated connection for the duration of fn and released afterwards.
type AdvisoryLocker struct {
	db *sql.DB
}

func NewAdvisoryLocker(db *sql.DB) *AdvisoryLocker { return &AdvisoryLocker{db: db} }

func (l *AdvisoryLocker) TryWithLock(ctx context.Context, key string, fn func(context.Context) error) (bool, error) {
	if l == nil || l.db == nil {
		return false, errors.New("advisory locker requires a database")
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	id := AdvisoryKey(key)
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, id).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, id)
	}()
	if err := fn(ctx); err != nil {
		return true, err
	}
	return true, nil
}

// AdvisoryKey derives a stable 64-bit key from a schedule key.
func AdvisoryKey(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}

var _ ports.Locker = (*AdvisoryLocker)(nil)
