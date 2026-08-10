// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package lookupcache

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

	"github.com/jihlenburg/bom-builder/internal/procurement"
	_ "modernc.org/sqlite"
)

// Store owns one process-safe SQLite cache connection.
type Store struct {
	db   *sql.DB
	path string
}

// Entry is one verified normalized lookup record.
type Entry struct {
	Provider       string
	Key            string
	ContextHash    string
	AdapterVersion string
	Demand         procurement.Demand
	Result         procurement.SourcedPart
	FetchedAt      time.Time
	ExpiresAt      time.Time
	SourceRequests int
	Stale          bool
}

// EntryMetadata exposes safe cache facts without provider payloads.
type EntryMetadata struct {
	Provider         string    `json:"provider"`
	Key              string    `json:"key"`
	PartNumber       string    `json:"part_number"`
	Manufacturer     string    `json:"manufacturer"`
	RequiredQuantity int       `json:"required_quantity"`
	ResultStatus     string    `json:"result_status"`
	AdapterVersion   string    `json:"adapter_version"`
	FetchedAt        time.Time `json:"fetched_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	SourceRequests   int       `json:"source_requests"`
	Stale            bool      `json:"stale"`
}

// Status summarizes one cache database.
type Status struct {
	Path          string     `json:"path"`
	Exists        bool       `json:"exists"`
	SchemaVersion int        `json:"schema_version"`
	EntryCount    int        `json:"entry_count"`
	FreshCount    int        `json:"fresh_count"`
	StaleCount    int        `json:"stale_count"`
	SizeBytes     int64      `json:"size_bytes"`
	OldestEntry   *time.Time `json:"oldest_entry,omitempty"`
	NewestEntry   *time.Time `json:"newest_entry,omitempty"`
}

// VerifyReport records SQLite and payload-integrity verification.
type VerifyReport struct {
	OK             bool     `json:"ok"`
	IntegrityCheck string   `json:"integrity_check"`
	CheckedEntries int      `json:"checked_entries"`
	InvalidEntries int      `json:"invalid_entries"`
	Issues         []string `json:"issues"`
}

// PruneResult is an exact preview or applied deletion set.
type PruneResult struct {
	Scope        string `json:"scope"`
	MatchedCount int    `json:"matched_count"`
	ApplyToken   string `json:"apply_token,omitempty"`
	Applied      bool   `json:"applied"`
	DeletedCount int64  `json:"deleted_count"`
}

type storedRow struct {
	provider       string
	key            string
	contextHash    string
	adapterVersion string
	demandJSON     []byte
	resultJSON     []byte
	resultSHA256   string
	status         string
	fetchedAtNS    int64
	expiresAtNS    int64
	sourceRequests int
}

type pruneIdentity struct {
	provider    string
	key         string
	digest      string
	fetchedAtNS int64
	expiresAtNS int64
}

// Open creates or opens a cache database after validating the target path.
func Open(path string) (*Store, error) {
	absolute, err := validatedPath(path)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, &Error{Kind: "open", Message: "could not create cache directory"}
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
			return nil, &Error{Kind: "open", Message: "could not securely create cache database"}
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, &Error{Kind: "open", Message: "could not initialize cache database file"}
		}
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		return nil, &Error{Kind: "permissions", Message: "could not secure cache database"}
	}
	for _, sidecar := range []string{absolute + "-wal", absolute + "-shm"} {
		if err := validateExistingFile(sidecar); err != nil {
			return nil, &Error{
				Kind:    "permissions",
				Message: "cache SQLite sidecar must be a regular non-symlink file",
			}
		}
	}
	// Session pragmas ride in the DSN so the driver applies them to every
	// connection it opens — including a replacement connection after
	// driver.ErrBadConn. A pool-level Exec would configure only the first
	// connection, and a recycled one would silently run with
	// busy_timeout = 0, turning cross-process contention into immediate
	// SQLITE_BUSY write failures. (journal_mode is not needed here: WAL
	// persists in the database file and initialize verifies it.)
	pragmas := url.Values{}
	for _, pragma := range []string{
		"busy_timeout(5000)",
		"foreign_keys(ON)",
		"synchronous(NORMAL)",
	} {
		pragmas.Add("_pragma", pragma)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: pragmas.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
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
		return nil, &Error{Kind: "permissions", Message: "could not secure cache database"}
	}
	store.secureSidecars()
	return store, nil
}

// Path returns the absolute cache database path.
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
	// The session pragmas arrive via the DSN (see Open); verify one here
	// so a driver change that stops honoring _pragma parameters fails
	// loudly at open instead of silently degrading write contention.
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
		return &Error{Kind: "migration", Message: "could not read cache schema version"}
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
		return &Error{Kind: "migration", Message: "could not start cache migration"}
	}
	defer transaction.Rollback()
	if version == 0 {
		statements := []string{
			`CREATE TABLE lookup_entries (
				provider TEXT NOT NULL,
				cache_key TEXT NOT NULL,
				context_hash TEXT NOT NULL,
				adapter_version TEXT NOT NULL,
				demand_json BLOB NOT NULL,
				result_json BLOB NOT NULL,
				result_sha256 TEXT NOT NULL,
				result_status TEXT NOT NULL,
				fetched_at_ns INTEGER NOT NULL,
				expires_at_ns INTEGER NOT NULL,
				source_requests INTEGER NOT NULL CHECK (source_requests >= 0),
				PRIMARY KEY (provider, cache_key)
			) WITHOUT ROWID`,
			`CREATE INDEX lookup_entries_expiry
				ON lookup_entries (expires_at_ns, provider)`,
			`PRAGMA user_version = 1`,
		}
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return &Error{Kind: "migration", Message: "could not create cache schema"}
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		return &Error{Kind: "migration", Message: "could not commit cache migration"}
	}
	return nil
}

// Get reads and verifies one entry. Expired entries are returned with Stale set.
func (store *Store) Get(
	ctx context.Context,
	provider, key string,
	now time.Time,
) (Entry, bool, error) {
	row := storedRow{}
	err := store.db.QueryRowContext(
		ctx,
		`SELECT provider, cache_key, context_hash, adapter_version,
			demand_json, result_json, result_sha256, result_status,
			fetched_at_ns, expires_at_ns, source_requests
		FROM lookup_entries
		WHERE provider = ? AND cache_key = ?`,
		strings.ToLower(strings.TrimSpace(provider)),
		key,
	).Scan(
		&row.provider,
		&row.key,
		&row.contextHash,
		&row.adapterVersion,
		&row.demandJSON,
		&row.resultJSON,
		&row.resultSHA256,
		&row.status,
		&row.fetchedAtNS,
		&row.expiresAtNS,
		&row.sourceRequests,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, &Error{Kind: "read", Message: "could not read cache entry"}
	}
	entry, err := decodeRow(row, now)
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

// Put atomically records a successful normalized provider result.
func (store *Store) Put(
	ctx context.Context,
	provider, key, contextHash, adapterVersion string,
	demand procurement.Demand,
	result procurement.SourcedPart,
	fetchedAt, expiresAt time.Time,
	sourceRequests int,
) error {
	safeDemand := compactDemand(demand)
	safeResult := result
	safeResult.Demand = safeDemand
	demandJSON, err := json.Marshal(safeDemand)
	if err != nil {
		return &Error{Kind: "encode", Message: "could not encode cache demand"}
	}
	resultJSON, err := json.Marshal(safeResult)
	if err != nil {
		return &Error{Kind: "encode", Message: "could not encode cache result"}
	}
	digest := sha256.Sum256(resultJSON)
	checksum := hex.EncodeToString(digest[:])
	// Refuse to persist anything the read path would reject: decodeRow
	// treats invalid entries as corrupt with deliberately no network
	// fallback, so a Put that bypasses its contract (most likely a new
	// adapter status missing from validCachedStatus) would poison every
	// later lookup of this part until expiry or prune. Round-tripping the
	// exact row we are about to write makes the write path and read path
	// enforce one contract by construction.
	candidate := storedRow{
		provider:       strings.ToLower(strings.TrimSpace(provider)),
		key:            key,
		contextHash:    contextHash,
		adapterVersion: adapterVersion,
		demandJSON:     demandJSON,
		resultJSON:     resultJSON,
		resultSHA256:   checksum,
		status:         result.Status,
		fetchedAtNS:    fetchedAt.UTC().UnixNano(),
		expiresAtNS:    expiresAt.UTC().UnixNano(),
		sourceRequests: sourceRequests,
	}
	if _, decodeErr := decodeRow(candidate, fetchedAt.UTC()); decodeErr != nil {
		return &Error{
			Kind:    "invalid",
			Message: "refusing to cache an entry the read path would reject: " + decodeErr.Error(),
		}
	}
	_, err = store.db.ExecContext(
		ctx,
		`INSERT INTO lookup_entries (
			provider, cache_key, context_hash, adapter_version,
			demand_json, result_json, result_sha256, result_status,
			fetched_at_ns, expires_at_ns, source_requests
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, cache_key) DO UPDATE SET
			context_hash = excluded.context_hash,
			adapter_version = excluded.adapter_version,
			demand_json = excluded.demand_json,
			result_json = excluded.result_json,
			result_sha256 = excluded.result_sha256,
			result_status = excluded.result_status,
			fetched_at_ns = excluded.fetched_at_ns,
			expires_at_ns = excluded.expires_at_ns,
			source_requests = excluded.source_requests`,
		candidate.provider,
		key,
		contextHash,
		adapterVersion,
		demandJSON,
		resultJSON,
		checksum,
		result.Status,
		fetchedAt.UTC().UnixNano(),
		expiresAt.UTC().UnixNano(),
		sourceRequests,
	)
	if err != nil {
		return &Error{Kind: "write", Message: "could not write cache entry"}
	}
	store.secureSidecars()
	return nil
}

// CacheStatus returns entry counts and file metadata.
func (store *Store) CacheStatus(ctx context.Context, now time.Time) (Status, error) {
	status := Status{
		Path:   store.path,
		Exists: true,
	}
	// Report the database's actual schema version, not the compile-time
	// constant: echoing the constant would mask drift if the open-time
	// migration invariant ever loosens.
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").
		Scan(&status.SchemaVersion); err != nil {
		return Status{}, &Error{Kind: "read", Message: "could not read cache schema version"}
	}
	var oldestNS, newestNS sql.NullInt64
	err := store.db.QueryRowContext(
		ctx,
		`SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN expires_at_ns > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN expires_at_ns <= ? THEN 1 ELSE 0 END), 0),
			MIN(fetched_at_ns),
			MAX(fetched_at_ns)
		FROM lookup_entries`,
		now.UTC().UnixNano(),
		now.UTC().UnixNano(),
	).Scan(
		&status.EntryCount,
		&status.FreshCount,
		&status.StaleCount,
		&oldestNS,
		&newestNS,
	)
	if err != nil {
		return Status{}, &Error{Kind: "read", Message: "could not summarize cache"}
	}
	if oldestNS.Valid {
		oldest := time.Unix(0, oldestNS.Int64).UTC()
		status.OldestEntry = &oldest
	}
	if newestNS.Valid {
		newest := time.Unix(0, newestNS.Int64).UTC()
		status.NewestEntry = &newest
	}
	for _, path := range []string{store.path, store.path + "-wal", store.path + "-shm"} {
		if info, statErr := os.Stat(path); statErr == nil {
			status.SizeBytes += info.Size()
		}
	}
	return status, nil
}

// List returns bounded safe metadata without cached provider payloads.
func (store *Store) List(
	ctx context.Context,
	provider string,
	limit int,
	includeStale bool,
	now time.Time,
) ([]EntryMetadata, error) {
	if limit < 1 || limit > 1000 {
		return nil, &Error{Kind: "input", Message: "list limit must be between 1 and 1000"}
	}
	conditions := []string{"1 = 1"}
	arguments := []any{}
	if provider = strings.ToLower(strings.TrimSpace(provider)); provider != "" {
		conditions = append(conditions, "provider = ?")
		arguments = append(arguments, provider)
	}
	if !includeStale {
		conditions = append(conditions, "expires_at_ns > ?")
		arguments = append(arguments, now.UTC().UnixNano())
	}
	arguments = append(arguments, limit)
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT provider, cache_key, context_hash, adapter_version,
			demand_json, result_json, result_sha256, result_status,
			fetched_at_ns, expires_at_ns, source_requests
		FROM lookup_entries
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY fetched_at_ns DESC, provider, cache_key
		LIMIT ?`,
		arguments...,
	)
	if err != nil {
		return nil, &Error{Kind: "read", Message: "could not list cache entries"}
	}
	defer rows.Close()
	entries := []EntryMetadata{}
	for rows.Next() {
		row, scanErr := scanStoredRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entry, decodeErr := decodeRow(row, now)
		if decodeErr != nil {
			return nil, decodeErr
		}
		entries = append(entries, EntryMetadata{
			Provider:         entry.Provider,
			Key:              entry.Key,
			PartNumber:       entry.Demand.PartNumber,
			Manufacturer:     entry.Demand.Manufacturer,
			RequiredQuantity: entry.Demand.RequiredQuantity,
			ResultStatus:     entry.Result.Status,
			AdapterVersion:   entry.AdapterVersion,
			FetchedAt:        entry.FetchedAt,
			ExpiresAt:        entry.ExpiresAt,
			SourceRequests:   entry.SourceRequests,
			Stale:            entry.Stale,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, &Error{Kind: "read", Message: "could not finish listing cache entries"}
	}
	return entries, nil
}

// Verify checks SQLite integrity, result hashes, JSON, and cache-key identity.
func (store *Store) Verify(ctx context.Context, now time.Time) (VerifyReport, error) {
	report := VerifyReport{Issues: []string{}}
	if err := store.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&report.IntegrityCheck); err != nil {
		return VerifyReport{}, &Error{Kind: "verify", Message: "SQLite integrity check failed"}
	}
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT provider, cache_key, context_hash, adapter_version,
			demand_json, result_json, result_sha256, result_status,
			fetched_at_ns, expires_at_ns, source_requests
		FROM lookup_entries
		ORDER BY provider, cache_key`,
	)
	if err != nil {
		return VerifyReport{}, &Error{Kind: "verify", Message: "could not scan cache entries"}
	}
	defer rows.Close()
	for rows.Next() {
		report.CheckedEntries++
		row, scanErr := scanStoredRow(rows)
		if scanErr != nil {
			return VerifyReport{}, scanErr
		}
		if _, decodeErr := decodeRow(row, now); decodeErr != nil {
			report.InvalidEntries++
			report.Issues = append(
				report.Issues,
				fmt.Sprintf("%s/%s: %s", row.provider, row.key, decodeErr.Error()),
			)
		}
	}
	if err := rows.Err(); err != nil {
		return VerifyReport{}, &Error{Kind: "verify", Message: "could not finish cache scan"}
	}
	report.OK = strings.EqualFold(report.IntegrityCheck, "ok") && report.InvalidEntries == 0
	return report, nil
}

// Prune previews or applies deletion of expired entries, or all entries.
func (store *Store) Prune(
	ctx context.Context,
	all bool,
	applyToken string,
	now time.Time,
) (PruneResult, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, &Error{Kind: "prune", Message: "could not start prune transaction"}
	}
	defer transaction.Rollback()
	scope := "expired"
	condition := "expires_at_ns <= ?"
	arguments := []any{now.UTC().UnixNano()}
	if all {
		scope = "all"
		condition = "1 = 1"
		arguments = nil
	}
	rows, err := transaction.QueryContext(
		ctx,
		`SELECT provider, cache_key, result_sha256, fetched_at_ns, expires_at_ns
		 FROM lookup_entries
		 WHERE `+condition+`
		 ORDER BY provider, cache_key`,
		arguments...,
	)
	if err != nil {
		return PruneResult{}, &Error{Kind: "prune", Message: "could not preview prune set"}
	}
	identities := []pruneIdentity{}
	for rows.Next() {
		var item pruneIdentity
		if err := rows.Scan(
			&item.provider,
			&item.key,
			&item.digest,
			&item.fetchedAtNS,
			&item.expiresAtNS,
		); err != nil {
			rows.Close()
			return PruneResult{}, &Error{Kind: "prune", Message: "could not read prune set"}
		}
		identities = append(identities, item)
	}
	if err := rows.Close(); err != nil {
		return PruneResult{}, &Error{Kind: "prune", Message: "could not close prune preview"}
	}
	token := pruneToken(scope, identities)
	result := PruneResult{
		Scope:        scope,
		MatchedCount: len(identities),
		ApplyToken:   token,
	}
	if strings.TrimSpace(applyToken) == "" {
		return result, nil
	}
	if !constantTimeTextEqual(token, strings.TrimSpace(applyToken)) {
		return PruneResult{}, &Error{
			Kind:    "stale_preview",
			Message: "apply token does not match the current prune set; preview again",
		}
	}
	for _, item := range identities {
		execution, deleteErr := transaction.ExecContext(
			ctx,
			`DELETE FROM lookup_entries
			 WHERE provider = ? AND cache_key = ? AND result_sha256 = ?
			   AND fetched_at_ns = ? AND expires_at_ns = ?`,
			item.provider,
			item.key,
			item.digest,
			item.fetchedAtNS,
			item.expiresAtNS,
		)
		if deleteErr != nil {
			return PruneResult{}, &Error{Kind: "prune", Message: "could not delete cache entry"}
		}
		deleted, rowsErr := execution.RowsAffected()
		if rowsErr != nil || deleted != 1 {
			return PruneResult{}, &Error{
				Kind:    "stale_preview",
				Message: "cache changed while prune was applying; no entries were deleted",
			}
		}
		result.DeletedCount += deleted
	}
	if err := transaction.Commit(); err != nil {
		return PruneResult{}, &Error{Kind: "prune", Message: "could not commit cache prune"}
	}
	result.Applied = true
	result.ApplyToken = ""
	store.secureSidecars()
	return result, nil
}

func decodeRow(row storedRow, now time.Time) (Entry, error) {
	digest := sha256.Sum256(row.resultJSON)
	if !constantTimeTextEqual(hex.EncodeToString(digest[:]), row.resultSHA256) {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached result checksum does not match"}
	}
	var demand procurement.Demand
	if err := json.Unmarshal(row.demandJSON, &demand); err != nil {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached demand is invalid JSON"}
	}
	var result procurement.SourcedPart
	if err := json.Unmarshal(row.resultJSON, &result); err != nil {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached result is invalid JSON"}
	}
	if !validCachedStatus(result.Status) || result.Status != row.status {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached result status is invalid"}
	}
	expectedKey, err := cacheKey(row.provider, row.adapterVersion, row.contextHash, demand)
	if err != nil {
		return Entry{}, err
	}
	if !constantTimeTextEqual(expectedKey, row.key) {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached demand does not match its key"}
	}
	resultKey, err := cacheKey(
		row.provider,
		row.adapterVersion,
		row.contextHash,
		result.Demand,
	)
	if err != nil || !constantTimeTextEqual(resultKey, row.key) {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached result demand does not match its key"}
	}
	if result.Offer != nil &&
		!strings.EqualFold(strings.TrimSpace(result.Offer.Provider), row.provider) {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached offer provider is invalid"}
	}
	if result.Status == "priced" &&
		(result.Offer == nil ||
			result.Offer.SelectedPlan == nil ||
			result.Offer.ReviewRequired ||
			!result.Offer.SelectedPlan.StockVerified ||
			result.Offer.SelectedPlan.PurchasedQuantity < demand.RequiredQuantity) {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached priced result is unsafe"}
	}
	fetchedAt := time.Unix(0, row.fetchedAtNS).UTC()
	expiresAt := time.Unix(0, row.expiresAtNS).UTC()
	if !expiresAt.After(fetchedAt) {
		return Entry{}, &Error{Kind: "corrupt", Message: "cached expiry is invalid"}
	}
	return Entry{
		Provider:       row.provider,
		Key:            row.key,
		ContextHash:    row.contextHash,
		AdapterVersion: row.adapterVersion,
		Demand:         demand,
		Result:         result,
		FetchedAt:      fetchedAt,
		ExpiresAt:      expiresAt,
		SourceRequests: row.sourceRequests,
		Stale:          !expiresAt.After(now.UTC()),
	}, nil
}

func validCachedStatus(status string) bool {
	switch status {
	case "priced",
		"shortage",
		"stock_unknown",
		"unavailable",
		"review",
		"not_found",
		"not_applicable":
		return true
	default:
		return false
	}
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredRow(scanner rowScanner) (storedRow, error) {
	row := storedRow{}
	if err := scanner.Scan(
		&row.provider,
		&row.key,
		&row.contextHash,
		&row.adapterVersion,
		&row.demandJSON,
		&row.resultJSON,
		&row.resultSHA256,
		&row.status,
		&row.fetchedAtNS,
		&row.expiresAtNS,
		&row.sourceRequests,
	); err != nil {
		return storedRow{}, &Error{Kind: "read", Message: "could not decode cache row"}
	}
	return row, nil
}

func pruneToken(scope string, identities []pruneIdentity) string {
	hasher := sha256.New()
	hasher.Write([]byte("bom-builder-cache-prune-v1\n"))
	hasher.Write([]byte(scope))
	hasher.Write([]byte{'\n'})
	for _, item := range identities {
		hasher.Write([]byte(item.provider))
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.key))
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.digest))
		hasher.Write([]byte{0})
		hasher.Write([]byte(fmt.Sprintf("%d:%d", item.fetchedAtNS, item.expiresAtNS)))
		hasher.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func constantTimeTextEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func validatedPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", &Error{Kind: "configuration", Message: "cache database path is empty"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", &Error{Kind: "configuration", Message: "cache database path is invalid"}
	}
	if filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return "", &Error{Kind: "configuration", Message: "cache database path must name a file"}
	}
	return absolute, nil
}

func validateExistingFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &Error{Kind: "open", Message: "could not inspect cache database"}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &Error{Kind: "permissions", Message: "cache database may not be a symbolic link"}
	}
	if !info.Mode().IsRegular() {
		return &Error{Kind: "permissions", Message: "cache database must be a regular file"}
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

// Close releases SQLite resources.
func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.secureSidecars()
	return store.db.Close()
}
