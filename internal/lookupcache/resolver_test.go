// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package lookupcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type fakeResolver struct {
	calls  int
	result procurement.SourcedPart
	err    error
}

func (resolver *fakeResolver) Lookup(
	_ context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	resolver.calls++
	result := resolver.result
	result.Demand = demand
	return result, resolver.err
}

func TestPreferCachesNormalizedResultAndRestoresCurrentDemand(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	session, err := NewSession(Config{
		Policy: PolicyPrefer,
		Path:   filepath.Join(t.TempDir(), "cache.sqlite3"),
		TTL:    time.Hour,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	network := &fakeResolver{
		result: procurement.SourcedPart{
			Status:         "not_found",
			CandidateCount: 3,
			IssueCode:      "PART_NOT_FOUND",
			IssueMessage:   "no exact candidate",
		},
	}
	requestCount := func() int { return network.calls }
	cached, err := session.Resolver("mouser", network, requestCount)
	if err != nil {
		t.Fatal(err)
	}
	first := procurement.Demand{
		PartNumber:       "ABC-123",
		Manufacturer:     "Example",
		RequiredQuantity: 10,
		Description:      "first design description",
		References: []procurement.SourceReference{
			{Design: "board-a", Reference: "U1"},
		},
	}
	if _, err := cached.Lookup(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Description = "second design description"
	second.References = []procurement.SourceReference{
		{Design: "board-b", Reference: "U9"},
	}
	result, err := cached.Lookup(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if network.calls != 1 {
		t.Fatalf("network calls = %d, want 1", network.calls)
	}
	if result.Demand.Description != second.Description ||
		len(result.Demand.References) != 1 ||
		result.Demand.References[0].Design != "board-b" {
		t.Fatalf("cached result leaked prior provenance: %#v", result.Demand)
	}
	stats := session.Snapshot()
	if stats.Hits != 1 || stats.Misses != 1 ||
		stats.Refreshes != 1 || stats.Writes != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestOfflineUsesStaleEntryWithoutNetworkResolver(t *testing.T) {
	current := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	online, err := NewSession(Config{
		Policy: PolicyPrefer,
		Path:   path,
		TTL:    time.Minute,
		Now:    func() time.Time { return current },
	})
	if err != nil {
		t.Fatal(err)
	}
	network := &fakeResolver{
		result: procurement.SourcedPart{
			Status:    "shortage",
			IssueCode: "INSUFFICIENT_STOCK",
		},
	}
	cached, err := online.Resolver("mouser", network, func() int { return network.calls })
	if err != nil {
		t.Fatal(err)
	}
	demand := procurement.Demand{
		PartNumber:       "ABC-123",
		Manufacturer:     "Example",
		RequiredQuantity: 100,
	}
	if _, err := cached.Lookup(context.Background(), demand); err != nil {
		t.Fatal(err)
	}
	if err := online.Close(); err != nil {
		t.Fatal(err)
	}

	current = current.Add(2 * time.Minute)
	offline, err := NewSession(Config{
		Policy: PolicyOffline,
		Path:   path,
		TTL:    time.Hour,
		Now:    func() time.Time { return current },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer offline.Close()
	resolver, err := offline.Resolver("mouser", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" || offline.Snapshot().StaleHits != 1 {
		t.Fatalf("unexpected stale result: %#v, stats: %#v", result, offline.Snapshot())
	}
}

func TestOnlyMissIsExplicitAndDoesNotRequireNetwork(t *testing.T) {
	session, err := NewSession(Config{
		Policy: PolicyOnly,
		Path:   filepath.Join(t.TempDir(), "cache.sqlite3"),
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	resolver, err := session.Resolver("mouser", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ABC-123",
		Manufacturer:     "Example",
		RequiredQuantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unavailable" || result.IssueCode != "CACHE_MISS" ||
		session.Snapshot().Misses != 1 {
		t.Fatalf("unexpected miss result: %#v", result)
	}
}

func TestCorruptionIsReportedWithoutNetworkFallback(t *testing.T) {
	session, err := NewSession(Config{
		Policy: PolicyPrefer,
		Path:   filepath.Join(t.TempDir(), "cache.sqlite3"),
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	network := &fakeResolver{
		result: procurement.SourcedPart{Status: "not_found"},
	}
	resolver, err := session.Resolver("mouser", network, func() int { return network.calls })
	if err != nil {
		t.Fatal(err)
	}
	demand := procurement.Demand{
		PartNumber:       "ABC-123",
		Manufacturer:     "Example",
		RequiredQuantity: 1,
	}
	if _, err := resolver.Lookup(context.Background(), demand); err != nil {
		t.Fatal(err)
	}
	if _, err := session.store.db.Exec(
		"UPDATE lookup_entries SET result_json = ?",
		[]byte(`{"status":"priced"}`),
	); err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Lookup(context.Background(), demand)
	var cacheError *Error
	if !errors.As(err, &cacheError) || cacheError.Kind != "corrupt" {
		t.Fatalf("error = %#v, want corrupt cache error", err)
	}
	if network.calls != 1 {
		t.Fatalf("network calls = %d, corrupt entry must not silently refresh", network.calls)
	}
}

func TestPruneRequiresMatchingPreviewToken(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	session, err := NewSession(Config{
		Policy: PolicyPrefer,
		Path:   filepath.Join(t.TempDir(), "cache.sqlite3"),
		TTL:    time.Minute,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	network := &fakeResolver{result: procurement.SourcedPart{Status: "not_found"}}
	resolver, err := session.Resolver("mouser", network, func() int { return network.calls })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ABC-123",
		Manufacturer:     "Example",
		RequiredQuantity: 1,
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	preview, err := session.store.Prune(context.Background(), false, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.MatchedCount != 1 || preview.ApplyToken == "" || preview.Applied {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if _, err := session.store.Prune(context.Background(), false, "wrong", now); err == nil {
		t.Fatal("wrong apply token unexpectedly succeeded")
	}
	status, err := session.store.CacheStatus(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if status.EntryCount != 1 {
		t.Fatalf("preview or rejected apply deleted data: %#v", status)
	}
	applied, err := session.store.Prune(
		context.Background(),
		false,
		preview.ApplyToken,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.DeletedCount != 1 {
		t.Fatalf("unexpected apply result: %#v", applied)
	}
}

func TestStoreRejectsSymlinkDatabase(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.sqlite3")
	if err := os.WriteFile(target, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "cache.sqlite3")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := Open(link)
	var cacheError *Error
	if !errors.As(err, &cacheError) || cacheError.Kind != "permissions" {
		t.Fatalf("error = %#v, want permissions error", err)
	}
}

func TestStoreRejectsSymlinkSQLiteSidecar(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "cache.sqlite3")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+"-wal"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := Open(path)
	var cacheError *Error
	if !errors.As(err, &cacheError) || cacheError.Kind != "permissions" {
		t.Fatalf("error = %#v, want permissions error", err)
	}
}

func TestStoreUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows does not carry POSIX permission bits: Go emulates the
		// mode (0666/0444) and Chmod affects only the read-only flag, so
		// this assertion is meaningless there. Access control on Windows
		// comes from the profile directory's ACLs.
		t.Skip("POSIX permission bits are not representable on Windows")
	}
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("permissions = %o, want 600", permissions)
	}
}
