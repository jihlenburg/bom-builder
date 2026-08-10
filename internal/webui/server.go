// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package webui implements BOM Builder's local web interface: a
// loopback-only HTTP server over the resolutions store and the resolver
// flow, parallel to the terminal interface in internal/tui.
//
// Security posture: the server is for one human on one machine. The CLI
// binds it to a loopback address only, every request must carry a
// per-session bearer token (so a malicious website cannot drive the API
// with cross-origin requests), the Host header must be a loopback name (so
// DNS-rebinding cannot reach it), and browser-origin state changes must
// come from a loopback origin. The JSON this API speaks is an internal
// contract between the binary and its own embedded frontend — the public
// machine interface remains the CLI.
//
// The frontend is hand-written embedded HTML/CSS/JS with no build step and
// no external requests; the dependency budget for this feature is the Go
// standard library alone.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

//go:embed static/index.html static/app.js static/app.css
var static embed.FS

// LookupRunner sources one demand for the resolver flow, identically to
// the terminal interface's runner. A nil runner disables /api/lookup.
type LookupRunner func(
	ctx context.Context,
	demand procurement.Demand,
	providers string,
) (procurement.SourcedPart, error)

const (
	maxBodyBytes   = 1024 * 1024
	lookupDeadline = 2 * time.Minute
)

// Options configures one web session.
type Options struct {
	// DatabasePath locates the resolutions SQLite database; the server
	// opens it read-write, exactly like interactive mode.
	DatabasePath string
	// Lookup sources one demand for the resolver flow; nil disables it.
	Lookup LookupRunner
}

// Server owns the resolutions store, the session token, and the handler.
type Server struct {
	store   *resolutions.Store
	lookup  LookupRunner
	token   string
	handler http.Handler
	now     func() time.Time
}

// New opens the store and constructs a ready-to-serve handler.
func New(options Options) (*Server, error) {
	if strings.TrimSpace(options.DatabasePath) == "" {
		return nil, errors.New("resolutions database path is required")
	}
	store, err := resolutions.Open(options.DatabasePath)
	if err != nil {
		return nil, err
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		store.Close()
		return nil, errors.New("could not generate a session token")
	}
	server := &Server{
		store:  store,
		lookup: options.Lookup,
		token:  hex.EncodeToString(tokenBytes[:]),
		now:    func() time.Time { return time.Now().UTC() },
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", server.handleIndex)
	mux.HandleFunc("GET /app.js", server.handleStaticFile("static/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /app.css", server.handleStaticFile("static/app.css", "text/css; charset=utf-8"))
	// Browsers request a favicon unprompted; answer without content so
	// the console stays free of misleading 404s under the strict CSP.
	mux.HandleFunc("GET /favicon.ico", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/status", server.authorized(server.handleStatus))
	mux.HandleFunc("GET /api/resolutions", server.authorized(server.handleList))
	mux.HandleFunc("GET /api/history", server.authorized(server.handleHistory))
	mux.HandleFunc("POST /api/approve", server.authorized(server.handleApprove))
	mux.HandleFunc("POST /api/revoke", server.authorized(server.handleRevoke))
	mux.HandleFunc("POST /api/lookup", server.authorized(server.handleLookup))
	server.handler = server.protect(mux)
	return server, nil
}

// Token returns the per-session bearer token.
func (server *Server) Token() string {
	return server.token
}

// DatabasePath returns the absolute resolutions database path.
func (server *Server) DatabasePath() string {
	return server.store.Path()
}

// Handler returns the fully protected HTTP handler.
func (server *Server) Handler() http.Handler {
	return server.handler
}

// Close releases the underlying store.
func (server *Server) Close() error {
	return server.store.Close()
}

// protect enforces the loopback Host, checks browser origins on state
// changes, and sets conservative response headers on every request.
func (server *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !loopbackHost(request.Host) {
			writeError(writer, http.StatusForbidden, "the web interface accepts loopback requests only")
			return
		}
		if request.Method != http.MethodGet && !allowedOrigin(request.Header.Get("Origin")) {
			writeError(writer, http.StatusForbidden, "cross-origin requests are not allowed")
			return
		}
		headers := writer.Header()
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set(
			"Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; "+
				"connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'",
		)
		headers.Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func loopbackHost(hostPort string) bool {
	host := hostPort
	if split, _, err := net.SplitHostPort(hostPort); err == nil {
		host = split
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func allowedOrigin(origin string) bool {
	// Non-browser clients (curl, tests) send no Origin header; browser
	// requests from our own page carry a loopback origin.
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	return loopbackHost(parsed.Host)
}

func (server *Server) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get("X-BOM-Builder-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(server.token)) != 1 {
			writeError(writer, http.StatusUnauthorized, "missing or invalid session token")
			return
		}
		next(writer, request)
	}
}

func (server *Server) handleIndex(writer http.ResponseWriter, request *http.Request) {
	document, err := static.ReadFile("static/index.html")
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "embedded interface is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Write(document)
}

func (server *Server) handleStaticFile(name, contentType string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		document, err := static.ReadFile(name)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "embedded interface is unavailable")
			return
		}
		writer.Header().Set("Content-Type", contentType)
		writer.Write(document)
	}
}

func (server *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := server.store.StoreStatus(request.Context())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, map[string]any{
		"status":      "ok",
		"resolutions": status,
		"resolver":    server.lookup != nil,
	})
}

func (server *Server) handleList(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	limit, err := queryLimit(query.Get("limit"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	records, err := server.store.List(
		request.Context(),
		query.Get("manufacturer"),
		query.Get("part"),
		limit,
		query.Get("include_inactive") == "true",
	)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, map[string]any{"status": "ok", "records": records})
}

func (server *Server) handleHistory(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	limit, err := queryLimit(query.Get("limit"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	events, err := server.store.History(
		request.Context(),
		query.Get("manufacturer"),
		query.Get("part"),
		limit,
	)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, map[string]any{"status": "ok", "events": events})
}

func (server *Server) handleApprove(writer http.ResponseWriter, request *http.Request) {
	var approval resolutions.Request
	if !decodeBody(writer, request, &approval) {
		return
	}
	record, superseded, err := server.store.Approve(request.Context(), approval, server.now())
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	response := map[string]any{"status": "ok", "resolution": record}
	if superseded != nil {
		response["superseded"] = superseded
	}
	writeJSON(writer, response)
}

type revokeRequest struct {
	ResolutionID string `json:"resolution_id"`
	RevokedBy    string `json:"revoked_by"`
	Reason       string `json:"reason"`
	ApplyToken   string `json:"apply_token"`
}

func (server *Server) handleRevoke(writer http.ResponseWriter, request *http.Request) {
	var revocation revokeRequest
	if !decodeBody(writer, request, &revocation) {
		return
	}
	result, err := server.store.Revoke(
		request.Context(),
		revocation.ResolutionID,
		revocation.RevokedBy,
		revocation.Reason,
		revocation.ApplyToken,
		server.now(),
	)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, map[string]any{"status": "ok", "revoke": result})
}

type lookupRequest struct {
	PartNumber   string `json:"part_number"`
	Manufacturer string `json:"manufacturer"`
	Quantity     int    `json:"quantity"`
	Providers    string `json:"providers"`
}

func (server *Server) handleLookup(writer http.ResponseWriter, request *http.Request) {
	if server.lookup == nil {
		writeError(writer, http.StatusServiceUnavailable, "the resolver is unavailable in this session")
		return
	}
	var lookup lookupRequest
	if !decodeBody(writer, request, &lookup) {
		return
	}
	lookup.PartNumber = strings.TrimSpace(lookup.PartNumber)
	lookup.Manufacturer = strings.TrimSpace(lookup.Manufacturer)
	if lookup.PartNumber == "" || lookup.Manufacturer == "" {
		writeError(writer, http.StatusBadRequest, "part_number and manufacturer are required")
		return
	}
	if lookup.Quantity == 0 {
		lookup.Quantity = 1
	}
	if lookup.Quantity < 1 {
		writeError(writer, http.StatusBadRequest, "quantity must be a positive integer")
		return
	}
	providers := strings.TrimSpace(lookup.Providers)
	if providers == "" {
		providers = "auto"
	}
	ctx, cancel := context.WithTimeout(request.Context(), lookupDeadline)
	defer cancel()
	part, err := server.lookup(ctx, procurement.Demand{
		PartNumber:       lookup.PartNumber,
		Manufacturer:     lookup.Manufacturer,
		QuantityPerUnit:  lookup.Quantity,
		RequiredQuantity: lookup.Quantity,
	}, providers)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(writer, map[string]any{"status": "ok", "part": part})
}

func decodeBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	body := http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request JSON: "+err.Error())
		return false
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		writeError(writer, http.StatusBadRequest, "request must contain exactly one JSON document")
		return false
	}
	return true
}

func queryLimit(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 100, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("limit must be an integer between 1 and 1000")
	}
	return limit, nil
}

func writeJSON(writer http.ResponseWriter, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeError(writer http.ResponseWriter, code int, message string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(code)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"status":  "error",
		"message": message,
	})
}

func writeStoreError(writer http.ResponseWriter, err error) {
	var storeErr *resolutions.Error
	if errors.As(err, &storeErr) {
		switch storeErr.Kind {
		case "input", "configuration":
			writeError(writer, http.StatusBadRequest, storeErr.Message)
			return
		case "not_found":
			writeError(writer, http.StatusNotFound, storeErr.Message)
			return
		case "stale_preview":
			writeError(writer, http.StatusConflict, storeErr.Message)
			return
		}
	}
	writeError(writer, http.StatusInternalServerError, err.Error())
}
