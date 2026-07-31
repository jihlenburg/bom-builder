package lookupcache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// openTestStore opens a fresh store in a per-test directory.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestPutRefusesEntriesTheReadPathWouldReject(t *testing.T) {
	t.Parallel()
	// decodeRow deliberately treats invalid entries as corrupt with no
	// network fallback, so anything Put persists that decodeRow rejects
	// poisons every later lookup of that part until expiry or prune. The
	// write path must therefore enforce the exact same contract as the
	// read path — most importantly the status whitelist, so a future
	// adapter status added without updating validCachedStatus fails at
	// write time instead of at every subsequent read.
	store := openTestStore(t)
	ctx := context.Background()
	demand := procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	}
	key, err := cacheKey("mouser", "adapter-v1", "context-hash", demand)
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	expiresAt := fetchedAt.Add(24 * time.Hour)

	unknownStatus := procurement.SourcedPart{Demand: demand, Status: "backordered"}
	err = store.Put(ctx, "mouser", key, "context-hash", "adapter-v1", demand, unknownStatus, fetchedAt, expiresAt, 1)
	if err == nil {
		t.Fatal("Put accepted a status the read path rejects as corrupt")
	}
	if _, found, _ := store.Get(ctx, "mouser", key, fetchedAt); found {
		t.Fatal("refused entry was written anyway")
	}

	invertedExpiry := procurement.SourcedPart{Demand: demand, Status: "not_found"}
	err = store.Put(ctx, "mouser", key, "context-hash", "adapter-v1", demand, invertedExpiry, fetchedAt, fetchedAt, 1)
	if err == nil {
		t.Fatal("Put accepted an expiry that does not follow the fetch time")
	}

	valid := procurement.SourcedPart{Demand: demand, Status: "not_found"}
	if err := store.Put(ctx, "mouser", key, "context-hash", "adapter-v1", demand, valid, fetchedAt, expiresAt, 1); err != nil {
		t.Fatalf("valid Put failed: %v", err)
	}
	entry, found, err := store.Get(ctx, "mouser", key, fetchedAt)
	if err != nil || !found {
		t.Fatalf("Get after valid Put: found=%v err=%v", found, err)
	}
	if entry.Result.Status != "not_found" {
		t.Fatalf("round-tripped status = %q", entry.Result.Status)
	}
}

func TestOpenAppliesSessionPragmas(t *testing.T) {
	t.Parallel()
	// Pins the session configuration the store depends on. The pragmas are
	// encoded in the DSN so that every pooled connection — including a
	// replacement after driver.ErrBadConn — carries them; a pool-level
	// Exec would configure only the first connection, and a recycled one
	// would silently run with busy_timeout = 0.
	store := openTestStore(t)
	var timeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", timeout)
	}
	var foreignKeys int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	var synchronous int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}
}

func TestCacheStatusReportsTheDatabasesActualSchemaVersion(t *testing.T) {
	t.Parallel()
	// CacheStatus must read PRAGMA user_version rather than echo the
	// compile-time constant: reporting the constant would mask drift if
	// the open-time migration invariant ever loosens.
	store := openTestStore(t)
	if _, err := store.db.Exec("PRAGMA user_version = 42"); err != nil {
		t.Fatal(err)
	}
	status, err := store.CacheStatus(context.Background(), time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 42 {
		t.Fatalf("schema version = %d, want the database's actual 42", status.SchemaVersion)
	}
}
