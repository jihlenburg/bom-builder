// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package resolutions

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store owns one process-safe SQLite resolutions connection. SQLite provides
// the locking and atomic-write guarantees the store requires on every
// supported platform, including Windows, where POSIX rename-over and
// advisory-lock tricks do not hold.
type Store struct {
	db   *sql.DB
	path string
}

type storedRow struct {
	resolutionID string
	demandKey    string
	manufacturer string
	partNumber   string
	status       string
	approvedAtNS int64
	updatedAtNS  int64
	recordJSON   []byte
	recordSHA256 string
}

// Open creates or opens a resolutions database after validating the path.
func Open(path string) (*Store, error) {
	absolute, err := validatedPath(path)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, &Error{Kind: "open", Message: "could not create resolutions directory"}
	}
	if err := validateExistingFile(absolute); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(absolute); errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(
			absolute,
			os.O_CREATE|os.O_EXCL|os.O_RDWR,
			0o600,
		)
		if createErr != nil {
			return nil, &Error{Kind: "open", Message: "could not securely create resolutions database"}
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, &Error{Kind: "open", Message: "could not initialize resolutions database file"}
		}
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		return nil, &Error{Kind: "permissions", Message: "could not secure resolutions database"}
	}
	for _, sidecar := range []string{absolute + "-wal", absolute + "-shm"} {
		if err := validateExistingFile(sidecar); err != nil {
			return nil, &Error{
				Kind:    "permissions",
				Message: "resolutions SQLite sidecar must be a regular non-symlink file",
			}
		}
	}
	// Session pragmas ride in the DSN so every pooled connection applies
	// them, exactly as in lookupcache: a pool-level Exec would leave a
	// recycled connection with busy_timeout = 0 and turn cross-process
	// contention into immediate SQLITE_BUSY failures.
	pragmas := url.Values{}
	for _, pragma := range []string{
		"busy_timeout(5000)",
		"foreign_keys(ON)",
		"synchronous(NORMAL)",
	} {
		pragmas.Add("_pragma", pragma)
	}
	db, err := sql.Open("sqlite", sqliteDSN(absolute, pragmas))
	if err != nil {
		return nil, &Error{Kind: "open", Message: "could not initialize SQLite"}
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: absolute}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		_ = db.Close()
		return nil, &Error{Kind: "permissions", Message: "could not secure resolutions database"}
	}
	store.secureSidecars()
	return store, nil
}

// Path returns the absolute resolutions database path.
func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) initialize(ctx context.Context) error {
	if err := store.db.PingContext(ctx); err != nil {
		return &Error{Kind: "open", Message: "could not open SQLite database"}
	}
	var busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil ||
		busyTimeout != 5000 {
		return &Error{Kind: "open", Message: "SQLite session pragmas were not applied"}
	}
	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return &Error{Kind: "open", Message: "could not enable SQLite WAL mode"}
	}
	if !strings.EqualFold(journalMode, "wal") {
		return &Error{Kind: "open", Message: "SQLite WAL mode is unavailable"}
	}
	return store.migrate(ctx)
}

func (store *Store) migrate(ctx context.Context) error {
	var version int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return &Error{Kind: "migration", Message: "could not read resolutions schema version"}
	}
	if version > SchemaVersion {
		return &Error{
			Kind: "migration",
			Message: fmt.Sprintf(
				"database schema %d is newer than supported schema %d",
				version,
				SchemaVersion,
			),
		}
	}
	if version == SchemaVersion {
		return nil
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return &Error{Kind: "migration", Message: "could not start resolutions migration"}
	}
	defer transaction.Rollback()
	if version == 0 {
		statements := []string{
			`CREATE TABLE resolution_records (
				resolution_id TEXT NOT NULL PRIMARY KEY,
				demand_key TEXT NOT NULL,
				manufacturer TEXT NOT NULL,
				part_number TEXT NOT NULL,
				status TEXT NOT NULL
					CHECK (status IN ('active', 'superseded', 'revoked')),
				approved_at_ns INTEGER NOT NULL,
				updated_at_ns INTEGER NOT NULL,
				record_json BLOB NOT NULL,
				record_sha256 TEXT NOT NULL
			) WITHOUT ROWID`,
			`CREATE UNIQUE INDEX resolution_records_active_demand
				ON resolution_records (demand_key)
				WHERE status = 'active'`,
			`CREATE INDEX resolution_records_demand
				ON resolution_records (demand_key, approved_at_ns)`,
			`CREATE TABLE resolution_events (
				event_id INTEGER PRIMARY KEY AUTOINCREMENT,
				resolution_id TEXT NOT NULL,
				action TEXT NOT NULL
					CHECK (action IN ('approved', 'superseded', 'revoked')),
				actor TEXT NOT NULL,
				demand_key TEXT NOT NULL,
				manufacturer TEXT NOT NULL,
				part_number TEXT NOT NULL,
				details TEXT NOT NULL DEFAULT '',
				occurred_at_ns INTEGER NOT NULL
			)`,
			`CREATE INDEX resolution_events_demand
				ON resolution_events (demand_key, event_id)`,
			`PRAGMA user_version = 1`,
		}
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return &Error{Kind: "migration", Message: "could not create resolutions schema"}
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		return &Error{Kind: "migration", Message: "could not commit resolutions migration"}
	}
	return nil
}

// Approve atomically records one validated human approval. Any previously
// active resolution for the same demand is superseded in the same
// transaction, and both changes land in the audit history.
func (store *Store) Approve(
	ctx context.Context,
	request Request,
	now time.Time,
) (Record, *Record, error) {
	if err := ValidateRequest(&request); err != nil {
		return Record{}, nil, &Error{Kind: "input", Message: err.Error()}
	}
	now = now.UTC()
	key := demandKey(request.Manufacturer, request.PartNumber)
	record := Record{
		ResolutionID:    newResolutionID(key, request, now),
		Manufacturer:    request.Manufacturer,
		PartNumber:      request.PartNumber,
		Replacement:     request.Replacement,
		ApprovedBy:      request.ApprovedBy,
		Note:            request.Note,
		SourceDocuments: append([]EvidenceDocument{}, request.SourceDocuments...),
		Status:          StatusActive,
		ApprovedAt:      now,
		UpdatedAt:       now,
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, nil, &Error{Kind: "write", Message: "could not start approval transaction"}
	}
	defer transaction.Rollback()
	previous, found, err := activeRecordForKey(ctx, transaction, key)
	if err != nil {
		return Record{}, nil, err
	}
	var superseded *Record
	if found {
		if previous.ResolutionID == record.ResolutionID {
			return Record{}, nil, &Error{
				Kind:    "write",
				Message: "approval collides with the active resolution identity",
			}
		}
		previous.Status = StatusSuperseded
		previous.UpdatedAt = now
		if err := updateRecord(ctx, transaction, previous); err != nil {
			return Record{}, nil, err
		}
		if err := appendEvent(ctx, transaction, Event{
			ResolutionID: previous.ResolutionID,
			Action:       ActionSuperseded,
			Actor:        record.ApprovedBy,
			Manufacturer: previous.Manufacturer,
			PartNumber:   previous.PartNumber,
			Details:      "superseded by resolution " + record.ResolutionID,
			OccurredAt:   now,
		}); err != nil {
			return Record{}, nil, err
		}
		superseded = &previous
	}
	if err := insertRecord(ctx, transaction, record); err != nil {
		return Record{}, nil, err
	}
	if err := appendEvent(ctx, transaction, Event{
		ResolutionID: record.ResolutionID,
		Action:       ActionApproved,
		Actor:        record.ApprovedBy,
		Manufacturer: record.Manufacturer,
		PartNumber:   record.PartNumber,
		Details:      record.Note,
		OccurredAt:   now,
	}); err != nil {
		return Record{}, nil, err
	}
	if err := transaction.Commit(); err != nil {
		return Record{}, nil, &Error{Kind: "write", Message: "could not commit approval"}
	}
	store.secureSidecars()
	return record, superseded, nil
}

// List returns bounded records in deterministic demand order, newest
// approval first within one demand.
func (store *Store) List(
	ctx context.Context,
	manufacturer, partNumber string,
	limit int,
	includeInactive bool,
) ([]Record, error) {
	if limit < 1 || limit > 1000 {
		return nil, &Error{Kind: "input", Message: "list limit must be between 1 and 1000"}
	}
	conditions := []string{"1 = 1"}
	arguments := []any{}
	if manufacturer = strings.TrimSpace(manufacturer); manufacturer != "" {
		conditions = append(conditions, "manufacturer = ? COLLATE NOCASE")
		arguments = append(arguments, manufacturer)
	}
	if partNumber = strings.TrimSpace(partNumber); partNumber != "" {
		conditions = append(conditions, "part_number = ? COLLATE NOCASE")
		arguments = append(arguments, partNumber)
	}
	if !includeInactive {
		conditions = append(conditions, "status = 'active'")
	}
	arguments = append(arguments, limit)
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT resolution_id, demand_key, manufacturer, part_number, status,
			approved_at_ns, updated_at_ns, record_json, record_sha256
		FROM resolution_records
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY demand_key, approved_at_ns DESC, resolution_id
		LIMIT ?`,
		arguments...,
	)
	if err != nil {
		return nil, &Error{Kind: "read", Message: "could not list resolutions"}
	}
	defer rows.Close()
	records := []Record{}
	for rows.Next() {
		row, scanErr := scanStoredRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		record, decodeErr := decodeRow(row)
		if decodeErr != nil {
			return nil, decodeErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, &Error{Kind: "read", Message: "could not finish listing resolutions"}
	}
	return records, nil
}

// History returns bounded audit events, newest first.
func (store *Store) History(
	ctx context.Context,
	manufacturer, partNumber string,
	limit int,
) ([]Event, error) {
	if limit < 1 || limit > 1000 {
		return nil, &Error{Kind: "input", Message: "history limit must be between 1 and 1000"}
	}
	conditions := []string{"1 = 1"}
	arguments := []any{}
	if manufacturer = strings.TrimSpace(manufacturer); manufacturer != "" {
		conditions = append(conditions, "manufacturer = ? COLLATE NOCASE")
		arguments = append(arguments, manufacturer)
	}
	if partNumber = strings.TrimSpace(partNumber); partNumber != "" {
		conditions = append(conditions, "part_number = ? COLLATE NOCASE")
		arguments = append(arguments, partNumber)
	}
	arguments = append(arguments, limit)
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT event_id, resolution_id, action, actor,
			manufacturer, part_number, details, occurred_at_ns
		FROM resolution_events
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY event_id DESC
		LIMIT ?`,
		arguments...,
	)
	if err != nil {
		return nil, &Error{Kind: "read", Message: "could not read resolution history"}
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var occurredAtNS int64
		if err := rows.Scan(
			&event.EventID,
			&event.ResolutionID,
			&event.Action,
			&event.Actor,
			&event.Manufacturer,
			&event.PartNumber,
			&event.Details,
			&occurredAtNS,
		); err != nil {
			return nil, &Error{Kind: "read", Message: "could not decode resolution event"}
		}
		event.OccurredAt = time.Unix(0, occurredAtNS).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, &Error{Kind: "read", Message: "could not finish reading resolution history"}
	}
	return events, nil
}

// Revoke previews or applies the revocation of one active resolution. The
// apply token binds the exact record content shown in the preview, so any
// concurrent change invalidates the token instead of being silently revoked.
func (store *Store) Revoke(
	ctx context.Context,
	resolutionID, revokedBy, reason, applyToken string,
	now time.Time,
) (RevokeResult, error) {
	resolutionID = strings.TrimSpace(resolutionID)
	revokedBy = strings.TrimSpace(revokedBy)
	reason = strings.TrimSpace(reason)
	if resolutionID == "" {
		return RevokeResult{}, &Error{Kind: "input", Message: "resolution id is required"}
	}
	if revokedBy == "" {
		return RevokeResult{}, &Error{
			Kind:    "input",
			Message: "revoked-by is required: a revocation records a human decision",
		}
	}
	if len(reason) > maxNoteLength {
		return RevokeResult{}, &Error{
			Kind:    "input",
			Message: fmt.Sprintf("reason must contain at most %d characters", maxNoteLength),
		}
	}
	now = now.UTC()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return RevokeResult{}, &Error{Kind: "write", Message: "could not start revoke transaction"}
	}
	defer transaction.Rollback()
	row, found, err := rowByID(ctx, transaction, resolutionID)
	if err != nil {
		return RevokeResult{}, err
	}
	if !found {
		return RevokeResult{}, &Error{
			Kind:    "not_found",
			Message: "resolution " + resolutionID + " does not exist",
		}
	}
	record, err := decodeRow(row)
	if err != nil {
		return RevokeResult{}, err
	}
	if record.Status != StatusActive {
		return RevokeResult{}, &Error{
			Kind:    "input",
			Message: "resolution " + resolutionID + " is not active (status " + record.Status + ")",
		}
	}
	token := revokeToken(row.resolutionID, row.recordSHA256)
	result := RevokeResult{
		ResolutionID: record.ResolutionID,
		Matched:      true,
		Record:       &record,
		ApplyToken:   token,
	}
	if strings.TrimSpace(applyToken) == "" {
		return result, nil
	}
	if !constantTimeTextEqual(token, strings.TrimSpace(applyToken)) {
		return RevokeResult{}, &Error{
			Kind:    "stale_preview",
			Message: "apply token does not match the current resolution; preview again",
		}
	}
	record.Status = StatusRevoked
	record.UpdatedAt = now
	if err := updateRecord(ctx, transaction, record); err != nil {
		return RevokeResult{}, err
	}
	if err := appendEvent(ctx, transaction, Event{
		ResolutionID: record.ResolutionID,
		Action:       ActionRevoked,
		Actor:        revokedBy,
		Manufacturer: record.Manufacturer,
		PartNumber:   record.PartNumber,
		Details:      reason,
		OccurredAt:   now,
	}); err != nil {
		return RevokeResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return RevokeResult{}, &Error{Kind: "write", Message: "could not commit revocation"}
	}
	store.secureSidecars()
	result.Record = &record
	result.ApplyToken = ""
	result.Applied = true
	return result, nil
}

// ActiveResolution returns the active record for one demand identity.
// Manufacturer and part number match case-insensitively, mirroring the
// approval key.
func (store *Store) ActiveResolution(
	ctx context.Context,
	manufacturer, partNumber string,
) (Record, bool, error) {
	return activeRecordForKey(ctx, store.db, demandKey(manufacturer, partNumber))
}

// StoreStatus returns record counts by status and audit-event volume.
func (store *Store) StoreStatus(ctx context.Context) (Status, error) {
	status := Status{Path: store.path, Exists: true}
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").
		Scan(&status.SchemaVersion); err != nil {
		return Status{}, &Error{Kind: "read", Message: "could not read resolutions schema version"}
	}
	err := store.db.QueryRowContext(
		ctx,
		`SELECT
			COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'superseded' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'revoked' THEN 1 ELSE 0 END), 0)
		FROM resolution_records`,
	).Scan(&status.ActiveCount, &status.SupersededCount, &status.RevokedCount)
	if err != nil {
		return Status{}, &Error{Kind: "read", Message: "could not summarize resolutions"}
	}
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM resolution_events`,
	).Scan(&status.EventCount); err != nil {
		return Status{}, &Error{Kind: "read", Message: "could not count resolution events"}
	}
	return status, nil
}

// Close releases SQLite resources.
func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.secureSidecars()
	return store.db.Close()
}

type queryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func activeRecordForKey(
	ctx context.Context,
	executor queryExecutor,
	key string,
) (Record, bool, error) {
	row := storedRow{}
	err := executor.QueryRowContext(
		ctx,
		`SELECT resolution_id, demand_key, manufacturer, part_number, status,
			approved_at_ns, updated_at_ns, record_json, record_sha256
		FROM resolution_records
		WHERE demand_key = ? AND status = 'active'`,
		key,
	).Scan(
		&row.resolutionID,
		&row.demandKey,
		&row.manufacturer,
		&row.partNumber,
		&row.status,
		&row.approvedAtNS,
		&row.updatedAtNS,
		&row.recordJSON,
		&row.recordSHA256,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, &Error{Kind: "read", Message: "could not read active resolution"}
	}
	record, err := decodeRow(row)
	if err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func rowByID(
	ctx context.Context,
	executor queryExecutor,
	resolutionID string,
) (storedRow, bool, error) {
	row := storedRow{}
	err := executor.QueryRowContext(
		ctx,
		`SELECT resolution_id, demand_key, manufacturer, part_number, status,
			approved_at_ns, updated_at_ns, record_json, record_sha256
		FROM resolution_records
		WHERE resolution_id = ?`,
		resolutionID,
	).Scan(
		&row.resolutionID,
		&row.demandKey,
		&row.manufacturer,
		&row.partNumber,
		&row.status,
		&row.approvedAtNS,
		&row.updatedAtNS,
		&row.recordJSON,
		&row.recordSHA256,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRow{}, false, nil
	}
	if err != nil {
		return storedRow{}, false, &Error{Kind: "read", Message: "could not read resolution"}
	}
	return row, true, nil
}

func insertRecord(
	ctx context.Context,
	executor queryExecutor,
	record Record,
) error {
	row, err := encodeRecord(record)
	if err != nil {
		return err
	}
	if _, err := executor.ExecContext(
		ctx,
		`INSERT INTO resolution_records (
			resolution_id, demand_key, manufacturer, part_number, status,
			approved_at_ns, updated_at_ns, record_json, record_sha256
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.resolutionID,
		row.demandKey,
		row.manufacturer,
		row.partNumber,
		row.status,
		row.approvedAtNS,
		row.updatedAtNS,
		row.recordJSON,
		row.recordSHA256,
	); err != nil {
		return &Error{Kind: "write", Message: "could not write resolution record"}
	}
	return nil
}

func updateRecord(
	ctx context.Context,
	executor queryExecutor,
	record Record,
) error {
	row, err := encodeRecord(record)
	if err != nil {
		return err
	}
	execution, err := executor.ExecContext(
		ctx,
		`UPDATE resolution_records SET
			status = ?, updated_at_ns = ?, record_json = ?, record_sha256 = ?
		WHERE resolution_id = ?`,
		row.status,
		row.updatedAtNS,
		row.recordJSON,
		row.recordSHA256,
		row.resolutionID,
	)
	if err != nil {
		return &Error{Kind: "write", Message: "could not update resolution record"}
	}
	updated, err := execution.RowsAffected()
	if err != nil || updated != 1 {
		return &Error{Kind: "write", Message: "resolution record update did not apply"}
	}
	return nil
}

func appendEvent(
	ctx context.Context,
	executor queryExecutor,
	event Event,
) error {
	if _, err := executor.ExecContext(
		ctx,
		`INSERT INTO resolution_events (
			resolution_id, action, actor, demand_key,
			manufacturer, part_number, details, occurred_at_ns
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ResolutionID,
		event.Action,
		event.Actor,
		demandKey(event.Manufacturer, event.PartNumber),
		event.Manufacturer,
		event.PartNumber,
		event.Details,
		event.OccurredAt.UTC().UnixNano(),
	); err != nil {
		return &Error{Kind: "write", Message: "could not append resolution event"}
	}
	return nil
}

// encodeRecord builds one storable row and refuses to produce anything the
// read path would reject, so write and read enforce one contract.
func encodeRecord(record Record) (storedRow, error) {
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return storedRow{}, &Error{Kind: "encode", Message: "could not encode resolution record"}
	}
	digest := sha256.Sum256(recordJSON)
	row := storedRow{
		resolutionID: record.ResolutionID,
		demandKey:    demandKey(record.Manufacturer, record.PartNumber),
		manufacturer: record.Manufacturer,
		partNumber:   record.PartNumber,
		status:       record.Status,
		approvedAtNS: record.ApprovedAt.UTC().UnixNano(),
		updatedAtNS:  record.UpdatedAt.UTC().UnixNano(),
		recordJSON:   recordJSON,
		recordSHA256: hex.EncodeToString(digest[:]),
	}
	if _, err := decodeRow(row); err != nil {
		return storedRow{}, &Error{
			Kind:    "invalid",
			Message: "refusing to store a record the read path would reject: " + err.Error(),
		}
	}
	return row, nil
}

func decodeRow(row storedRow) (Record, error) {
	digest := sha256.Sum256(row.recordJSON)
	if !constantTimeTextEqual(hex.EncodeToString(digest[:]), row.recordSHA256) {
		return Record{}, &Error{Kind: "corrupt", Message: "resolution checksum does not match"}
	}
	var record Record
	if err := json.Unmarshal(row.recordJSON, &record); err != nil {
		return Record{}, &Error{Kind: "corrupt", Message: "resolution record is invalid JSON"}
	}
	if record.ResolutionID != row.resolutionID ||
		record.Status != row.status ||
		demandKey(record.Manufacturer, record.PartNumber) != row.demandKey ||
		record.Manufacturer != row.manufacturer ||
		record.PartNumber != row.partNumber ||
		record.ApprovedAt.UTC().UnixNano() != row.approvedAtNS ||
		record.UpdatedAt.UTC().UnixNano() != row.updatedAtNS {
		return Record{}, &Error{Kind: "corrupt", Message: "resolution record does not match its row"}
	}
	switch record.Status {
	case StatusActive, StatusSuperseded, StatusRevoked:
	default:
		return Record{}, &Error{Kind: "corrupt", Message: "resolution status is invalid"}
	}
	validation := Request{
		Manufacturer:    record.Manufacturer,
		PartNumber:      record.PartNumber,
		Replacement:     record.Replacement,
		ApprovedBy:      record.ApprovedBy,
		Note:            record.Note,
		SourceDocuments: record.SourceDocuments,
	}
	if err := ValidateRequest(&validation); err != nil {
		return Record{}, &Error{Kind: "corrupt", Message: "resolution content is invalid: " + err.Error()}
	}
	return record, nil
}

func newResolutionID(key string, request Request, now time.Time) string {
	hasher := sha256.New()
	hasher.Write([]byte("bom-builder-resolution-id-v1\n"))
	hasher.Write([]byte(key))
	hasher.Write([]byte{0})
	hasher.Write([]byte(request.Replacement.Manufacturer))
	hasher.Write([]byte{0})
	hasher.Write([]byte(request.Replacement.PartNumber))
	hasher.Write([]byte{0})
	hasher.Write([]byte(request.Replacement.Provider))
	hasher.Write([]byte{0})
	hasher.Write([]byte(request.Replacement.ProviderSKU))
	hasher.Write([]byte{0})
	hasher.Write([]byte(request.ApprovedBy))
	hasher.Write([]byte{0})
	hasher.Write([]byte(fmt.Sprintf("%d", now.UTC().UnixNano())))
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}

func revokeToken(resolutionID, recordSHA256 string) string {
	hasher := sha256.New()
	hasher.Write([]byte("bom-builder-resolutions-revoke-v1\n"))
	hasher.Write([]byte(resolutionID))
	hasher.Write([]byte{'\n'})
	hasher.Write([]byte(recordSHA256))
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredRow(scanner rowScanner) (storedRow, error) {
	row := storedRow{}
	if err := scanner.Scan(
		&row.resolutionID,
		&row.demandKey,
		&row.manufacturer,
		&row.partNumber,
		&row.status,
		&row.approvedAtNS,
		&row.updatedAtNS,
		&row.recordJSON,
		&row.recordSHA256,
	); err != nil {
		return storedRow{}, &Error{Kind: "read", Message: "could not decode resolution row"}
	}
	return row, nil
}

func constantTimeTextEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// sqliteDSN renders one absolute path as a portable SQLite file URI. A raw
// Windows path inside url.URL.Path would surface the drive letter as the URI
// authority ("file://C:%5C..."), which SQLite rejects; the canonical form on
// every platform is a slash-separated path after "file://" with the drive
// letter as the first segment ("file:///C:/...").
func sqliteDSN(absolute string, query url.Values) string {
	path := filepath.ToSlash(absolute)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func validatedPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", &Error{Kind: "configuration", Message: "resolutions database path is empty"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", &Error{Kind: "configuration", Message: "resolutions database path is invalid"}
	}
	if filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return "", &Error{Kind: "configuration", Message: "resolutions database path must name a file"}
	}
	return absolute, nil
}

func validateExistingFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &Error{Kind: "open", Message: "could not inspect resolutions database"}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &Error{Kind: "permissions", Message: "resolutions database may not be a symbolic link"}
	}
	if !info.Mode().IsRegular() {
		return &Error{Kind: "permissions", Message: "resolutions database must be a regular file"}
	}
	return nil
}

func (store *Store) secureSidecars() {
	if store == nil {
		return
	}
	for _, path := range []string{store.path + "-wal", store.path + "-shm"} {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			_ = os.Chmod(path, 0o600)
		}
	}
}
