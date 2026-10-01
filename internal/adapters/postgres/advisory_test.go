package postgres

import (
	"context"
	"database/sql"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestAdvisoryLockerSerializesAcrossInstances proves that two scheduler
// replicas contending for the same (tenant, schedule) key cannot execute the
// trigger at the same time: exactly one acquires it.
func TestAdvisoryLockerSerializesAcrossInstances(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	first := NewAdvisoryLocker(db)
	second := NewAdvisoryLocker(db)
	key := "scheduler:tenant-a:consolidate"

	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan bool, 1)
	go func() {
		acquired, _ := first.TryWithLock(ctx, key, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
		firstDone <- acquired
	}()
	<-started

	// While the first holds the lock, the second must be refused.
	var ranSecond int32
	acquired, err := second.TryWithLock(ctx, key, func(context.Context) error {
		atomic.StoreInt32(&ranSecond, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("second TryWithLock error = %v", err)
	}
	if acquired {
		t.Fatal("second instance acquired a lock already held by the first")
	}
	if atomic.LoadInt32(&ranSecond) != 0 {
		t.Fatal("second instance ran its trigger despite not holding the lock")
	}

	close(release)
	if acquiredFirst := <-firstDone; !acquiredFirst {
		t.Fatal("first instance did not report acquiring the lock")
	}

	// Once released, a later attempt succeeds.
	acquired, err = second.TryWithLock(ctx, key, func(context.Context) error { return nil })
	if err != nil || !acquired {
		t.Fatalf("post-release TryWithLock acquired=%v err=%v", acquired, err)
	}
}
