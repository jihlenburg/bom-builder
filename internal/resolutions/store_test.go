// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package resolutions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testRequest() Request {
	return Request{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421-Q1",
		Replacement: Replacement{
			Manufacturer: "Texas Instruments",
			PartNumber:   "TMP421AQDCNRQ1",
			Provider:     "mouser",
			ProviderSKU:  "595-TMP421AQDCNRQ1",
		},
		ApprovedBy: "J. Ihlenburg",
		Note:       "packaging variant cleared for Rev A",
		SourceDocuments: []EvidenceDocument{{
			URL:    "https://www.ti.com/lit/ds/symlink/tmp421.pdf",
			SHA256: strings.Repeat("ab", 32),
		}},
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "resolutions.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestApproveRoundTripsRecordAndAuditEvent(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	record, superseded, err := store.Approve(context.Background(), testRequest(), now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if superseded != nil {
		t.Fatalf("first approval must not supersede anything, got %+v", superseded)
	}
	if record.Status != StatusActive {
		t.Fatalf("expected active record, got %q", record.Status)
	}
	if len(record.ResolutionID) != 16 {
		t.Fatalf("expected 16-character resolution id, got %q", record.ResolutionID)
	}

	records, err := store.List(context.Background(), "", "", 100, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one active record, got %d", len(records))
	}
	loaded := records[0]
	if loaded.ResolutionID != record.ResolutionID ||
		loaded.Replacement != record.Replacement ||
		loaded.ApprovedBy != record.ApprovedBy ||
		loaded.Note != record.Note ||
		!loaded.ApprovedAt.Equal(now) {
		t.Fatalf("loaded record does not match approval: %+v", loaded)
	}

	events, err := store.History(context.Background(), "", "", 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 1 ||
		events[0].Action != ActionApproved ||
		events[0].Actor != "J. Ihlenburg" ||
		events[0].ResolutionID != record.ResolutionID {
		t.Fatalf("expected one approved event, got %+v", events)
	}
}

func TestApproveSupersedesPreviousActiveResolution(t *testing.T) {
	store := openTestStore(t)
	first := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	original, _, err := store.Approve(context.Background(), testRequest(), first)
	if err != nil {
		t.Fatalf("first approve: %v", err)
	}
	replacementRequest := testRequest()
	replacementRequest.Replacement.PartNumber = "TMP421BQDCNRQ1"
	updated, superseded, err := store.Approve(context.Background(), replacementRequest, second)
	if err != nil {
		t.Fatalf("second approve: %v", err)
	}
	if superseded == nil || superseded.ResolutionID != original.ResolutionID {
		t.Fatalf("expected the first resolution to be superseded, got %+v", superseded)
	}
	if superseded.Status != StatusSuperseded {
		t.Fatalf("expected superseded status, got %q", superseded.Status)
	}

	active, err := store.List(context.Background(), "texas instruments", "tmp421-q1", 100, false)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].ResolutionID != updated.ResolutionID {
		t.Fatalf("expected only the new resolution active, got %+v", active)
	}

	everything, err := store.List(context.Background(), "", "", 100, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(everything) != 2 {
		t.Fatalf("expected two stored records, got %d", len(everything))
	}

	events, err := store.History(context.Background(), "", "", 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected approve+supersede+approve events, got %+v", events)
	}
	if events[0].Action != ActionApproved ||
		events[1].Action != ActionSuperseded ||
		events[2].Action != ActionApproved {
		t.Fatalf("unexpected event order (newest first): %+v", events)
	}
	if !strings.Contains(events[1].Details, updated.ResolutionID) {
		t.Fatalf("supersede event must reference the new resolution: %+v", events[1])
	}
}

func TestRevokeRequiresExactPreviewToken(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	record, _, err := store.Approve(context.Background(), testRequest(), now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	preview, err := store.Revoke(
		context.Background(),
		record.ResolutionID,
		"J. Ihlenburg",
		"replaced by internal stock",
		"",
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Applied || preview.ApplyToken == "" || !preview.Matched {
		t.Fatalf("expected unapplied preview with token, got %+v", preview)
	}

	if _, err := store.Revoke(
		context.Background(),
		record.ResolutionID,
		"J. Ihlenburg",
		"",
		"sha256:wrong",
		now.Add(time.Hour),
	); err == nil {
		t.Fatal("expected a stale-preview failure for a wrong token")
	} else {
		var storeErr *Error
		if !errors.As(err, &storeErr) || storeErr.Kind != "stale_preview" {
			t.Fatalf("expected stale_preview, got %v", err)
		}
	}

	applied, err := store.Revoke(
		context.Background(),
		record.ResolutionID,
		"J. Ihlenburg",
		"replaced by internal stock",
		preview.ApplyToken,
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !applied.Applied || applied.Record.Status != StatusRevoked {
		t.Fatalf("expected applied revocation, got %+v", applied)
	}

	if _, err := store.Revoke(
		context.Background(),
		record.ResolutionID,
		"J. Ihlenburg",
		"",
		"",
		now.Add(2*time.Hour),
	); err == nil {
		t.Fatal("expected preview of a revoked resolution to fail")
	}

	events, err := store.History(context.Background(), "", "", 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if events[0].Action != ActionRevoked ||
		events[0].Details != "replaced by internal stock" {
		t.Fatalf("expected a revoked audit event, got %+v", events[0])
	}
}

func TestRevokeUnknownResolutionFails(t *testing.T) {
	store := openTestStore(t)
	_, err := store.Revoke(
		context.Background(),
		"does-not-exist",
		"J. Ihlenburg",
		"",
		"",
		time.Now(),
	)
	var storeErr *Error
	if !errors.As(err, &storeErr) || storeErr.Kind != "not_found" {
		t.Fatalf("expected not_found, got %v", err)
	}
}

func TestRevokeTokenGoesStaleWhenRecordChanges(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	record, _, err := store.Approve(context.Background(), testRequest(), now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	preview, err := store.Revoke(
		context.Background(),
		record.ResolutionID,
		"J. Ihlenburg",
		"",
		"",
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// A new approval supersedes the previewed record, so the old token must
	// stop working: the record it described no longer exists in that state.
	if _, _, err := store.Approve(context.Background(), testRequest(), now.Add(2*time.Hour)); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if _, err := store.Revoke(
		context.Background(),
		record.ResolutionID,
		"J. Ihlenburg",
		"",
		preview.ApplyToken,
		now.Add(3*time.Hour),
	); err == nil {
		t.Fatal("expected the stale token to be rejected after supersede")
	}
}

func TestListIsDeterministicAndFilterable(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	parts := []string{"Z-PART-1", "A-PART-1", "M-PART-1"}
	for index, part := range parts {
		request := testRequest()
		request.PartNumber = part
		if _, _, err := store.Approve(
			context.Background(),
			request,
			base.Add(time.Duration(index)*time.Minute),
		); err != nil {
			t.Fatalf("approve %s: %v", part, err)
		}
	}
	records, err := store.List(context.Background(), "", "", 100, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ordered := []string{}
	for _, record := range records {
		ordered = append(ordered, record.PartNumber)
	}
	expected := []string{"A-PART-1", "M-PART-1", "Z-PART-1"}
	if fmt.Sprint(ordered) != fmt.Sprint(expected) {
		t.Fatalf("expected deterministic order %v, got %v", expected, ordered)
	}

	filtered, err := store.List(context.Background(), "TEXAS INSTRUMENTS", "a-part-1", 100, false)
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(filtered) != 1 || filtered[0].PartNumber != "A-PART-1" {
		t.Fatalf("case-insensitive filters must match, got %+v", filtered)
	}
}

func TestStoreStatusCountsByStatus(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	record, _, err := store.Approve(context.Background(), testRequest(), now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, _, err := store.Approve(context.Background(), testRequest(), now.Add(time.Hour)); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	_ = record

	other := testRequest()
	other.PartNumber = "OTHER-PART"
	otherRecord, _, err := store.Approve(context.Background(), other, now)
	if err != nil {
		t.Fatalf("approve other: %v", err)
	}
	preview, err := store.Revoke(
		context.Background(),
		otherRecord.ResolutionID,
		"J. Ihlenburg",
		"",
		"",
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := store.Revoke(
		context.Background(),
		otherRecord.ResolutionID,
		"J. Ihlenburg",
		"",
		preview.ApplyToken,
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	status, err := store.StoreStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.SchemaVersion != SchemaVersion ||
		status.ActiveCount != 1 ||
		status.SupersededCount != 1 ||
		status.RevokedCount != 1 ||
		status.EventCount != 5 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestConcurrentApprovalsKeepOneActiveResolutionPerDemand(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	const writers = 8
	var group sync.WaitGroup
	errs := make([]error, writers)
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			request := testRequest()
			request.Replacement.ProviderSKU = fmt.Sprintf("595-TMP421-%02d", worker)
			_, _, err := store.Approve(
				context.Background(),
				request,
				base.Add(time.Duration(worker)*time.Millisecond),
			)
			errs[worker] = err
		}(index)
	}
	group.Wait()
	for worker, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", worker, err)
		}
	}
	active, err := store.List(context.Background(), "", "", 1000, false)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected exactly one active resolution, got %d", len(active))
	}
	all, err := store.List(context.Background(), "", "", 1000, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != writers {
		t.Fatalf("expected %d stored records, got %d", writers, len(all))
	}
	events, err := store.History(context.Background(), "", "", 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(events) != writers*2-1 {
		t.Fatalf("expected %d audit events, got %d", writers*2-1, len(events))
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolutions.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	store.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("expected a newer-schema database to be rejected")
	}
}

func TestApproveRejectsInvalidRequests(t *testing.T) {
	store := openTestStore(t)
	now := time.Now()
	broken := testRequest()
	broken.ApprovedBy = ""
	if _, _, err := store.Approve(context.Background(), broken, now); err == nil {
		t.Fatal("expected missing approver to be rejected")
	}
	broken = testRequest()
	broken.Replacement.Provider = "amazon"
	if _, _, err := store.Approve(context.Background(), broken, now); err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}
}

func TestSQLiteDSNIsPortable(t *testing.T) {
	posix := sqliteDSN("/home/user/resolutions.sqlite3", nil)
	if posix != "file:///home/user/resolutions.sqlite3" {
		t.Fatalf("unexpected POSIX DSN: %s", posix)
	}
	// A Windows absolute path (already slash-separated here, since
	// filepath.ToSlash converts separators only on Windows itself) must
	// become an authority-free path: a drive letter in the URL authority
	// position ("file://C:/...") is rejected by SQLite.
	windows := sqliteDSN("C:/Users/joern/resolutions.sqlite3", nil)
	if windows != "file:///C:/Users/joern/resolutions.sqlite3" {
		t.Fatalf("Windows path must render as file:///C:/..., got %s", windows)
	}
}
