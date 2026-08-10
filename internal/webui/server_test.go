// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func newTestServer(t *testing.T, lookup LookupRunner) (*Server, *httptest.Server) {
	t.Helper()
	server, err := New(Options{
		DatabasePath: filepath.Join(t.TempDir(), "resolutions.sqlite3"),
		Lookup:       lookup,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { server.Close() })
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)
	return server, web
}

func call(
	t *testing.T,
	web *httptest.Server,
	token, method, path string,
	body any,
	mutate func(*http.Request),
) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, web.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		request.Header.Set("X-BOM-Builder-Token", token)
	}
	if mutate != nil {
		mutate(request)
	}
	response, err := web.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	payload := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil && err != io.EOF {
		t.Fatalf("decode response: %v", err)
	}
	return response.StatusCode, payload
}

func approval(part string) map[string]any {
	return map[string]any{
		"manufacturer": "Texas Instruments",
		"part_number":  part,
		"replacement": map[string]any{
			"manufacturer": "Texas Instruments",
			"part_number":  "TMP421AQDCNRQ1",
			"provider":     "mouser",
			"provider_sku": "595-TMP421AQDCNRQ1",
		},
		"approved_by": "J. Ihlenburg",
		"note":        "cleared for Rev A",
	}
}

func TestAPILifecycleApproveListHistoryRevoke(t *testing.T) {
	server, web := newTestServer(t, nil)
	token := server.Token()

	code, payload := call(t, web, token, "POST", "/api/approve", approval("TMP421-Q1"), nil)
	if code != http.StatusOK {
		t.Fatalf("approve = %d %v", code, payload)
	}
	resolution := payload["resolution"].(map[string]any)
	resolutionID := resolution["resolution_id"].(string)

	// Superseding approval reports the old record.
	code, payload = call(t, web, token, "POST", "/api/approve", approval("TMP421-Q1"), nil)
	if code != http.StatusOK || payload["superseded"] == nil {
		t.Fatalf("expected a superseding approval, got %d %v", code, payload)
	}
	newID := payload["resolution"].(map[string]any)["resolution_id"].(string)
	if newID == resolutionID {
		t.Fatal("superseding approval must mint a new resolution id")
	}

	code, payload = call(t, web, token, "GET", "/api/resolutions", nil, nil)
	if code != http.StatusOK || len(payload["records"].([]any)) != 1 {
		t.Fatalf("expected one active record, got %d %v", code, payload)
	}
	code, payload = call(t, web, token, "GET", "/api/resolutions?include_inactive=true", nil, nil)
	if code != http.StatusOK || len(payload["records"].([]any)) != 2 {
		t.Fatalf("expected two records with inactive, got %d %v", code, payload)
	}

	code, payload = call(
		t, web, token, "GET",
		"/api/history?manufacturer=Texas%20Instruments&part=TMP421-Q1", nil, nil,
	)
	if code != http.StatusOK || len(payload["events"].([]any)) != 3 {
		t.Fatalf("expected three audit events, got %d %v", code, payload)
	}

	// Preview then apply; a wrong token conflicts.
	revoke := map[string]any{
		"resolution_id": newID,
		"revoked_by":    "J. Ihlenburg",
		"reason":        "design change",
	}
	code, payload = call(t, web, token, "POST", "/api/revoke", revoke, nil)
	if code != http.StatusOK {
		t.Fatalf("revoke preview = %d %v", code, payload)
	}
	preview := payload["revoke"].(map[string]any)
	if preview["applied"] != false || preview["apply_token"] == "" {
		t.Fatalf("expected an unapplied preview, got %v", preview)
	}
	revoke["apply_token"] = "sha256:wrong"
	if code, _ = call(t, web, token, "POST", "/api/revoke", revoke, nil); code != http.StatusConflict {
		t.Fatalf("a wrong token must conflict, got %d", code)
	}
	revoke["apply_token"] = preview["apply_token"]
	code, payload = call(t, web, token, "POST", "/api/revoke", revoke, nil)
	if code != http.StatusOK || payload["revoke"].(map[string]any)["applied"] != true {
		t.Fatalf("revoke apply = %d %v", code, payload)
	}

	if code, _ = call(t, web, token, "POST", "/api/revoke", map[string]any{
		"resolution_id": "missing", "revoked_by": "X",
	}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown id must be 404, got %d", code)
	}
}

func TestAPIRequiresSessionToken(t *testing.T) {
	server, web := newTestServer(t, nil)
	for _, attempt := range []string{"", "wrong-token"} {
		code, _ := call(t, web, attempt, "GET", "/api/status", nil, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("token %q: expected 401, got %d", attempt, code)
		}
	}
	code, payload := call(t, web, server.Token(), "GET", "/api/status", nil, nil)
	if code != http.StatusOK || payload["resolutions"] == nil {
		t.Fatalf("expected authorized status, got %d %v", code, payload)
	}
	if payload["resolver"] != false {
		t.Fatalf("resolver must be reported unavailable without a runner: %v", payload)
	}
}

func TestNonLoopbackHostAndForeignOriginAreRejected(t *testing.T) {
	server, web := newTestServer(t, nil)
	code, payload := call(t, web, server.Token(), "GET", "/api/status", nil,
		func(request *http.Request) { request.Host = "attacker.example" })
	if code != http.StatusForbidden {
		t.Fatalf("DNS-rebound host must be rejected, got %d %v", code, payload)
	}
	code, _ = call(t, web, server.Token(), "POST", "/api/approve", approval("TMP421-Q1"),
		func(request *http.Request) { request.Header.Set("Origin", "https://attacker.example") })
	if code != http.StatusForbidden {
		t.Fatalf("foreign origin must be rejected, got %d", code)
	}
	// Our own loopback origin stays allowed.
	code, _ = call(t, web, server.Token(), "POST", "/api/approve", approval("TMP421-Q1"),
		func(request *http.Request) { request.Header.Set("Origin", "http://"+request.Host) })
	if code != http.StatusOK {
		t.Fatalf("loopback origin must be allowed, got %d", code)
	}
}

func TestApproveValidationErrorsAreBadRequests(t *testing.T) {
	server, web := newTestServer(t, nil)
	token := server.Token()
	code, payload := call(t, web, token, "POST", "/api/approve", map[string]any{
		"manufacturer": "TI",
	}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("incomplete approval must be 400, got %d %v", code, payload)
	}
	code, _ = call(t, web, token, "POST", "/api/approve", map[string]any{
		"manufacturer": "TI", "surprise": true,
	}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown fields must be 400, got %d", code)
	}
}

func TestLookupEndpointRunsResolverAndValidates(t *testing.T) {
	stock := 2940
	calls := 0
	runner := func(
		_ context.Context,
		demand procurement.Demand,
		providers string,
	) (procurement.SourcedPart, error) {
		calls++
		if providers != "auto" || demand.RequiredQuantity != 25 {
			return procurement.SourcedPart{}, fmt.Errorf(
				"unexpected lookup: %q %d", providers, demand.RequiredQuantity,
			)
		}
		return procurement.SourcedPart{
			Demand: demand,
			Status: "priced",
			Offers: []procurement.Offer{{
				Provider:               "mouser",
				ManufacturerPartNumber: "TMP421AQDCNRQ1",
				DistributorPartNumber:  "595-TMP421AQDCNRQ1",
				MatchMethod:            "exact",
				AvailableQuantity:      &stock,
			}},
		}, nil
	}
	server, web := newTestServer(t, runner)
	token := server.Token()

	code, payload := call(t, web, token, "POST", "/api/lookup", map[string]any{
		"part_number": "TMP421-Q1", "manufacturer": "Texas Instruments", "quantity": 25,
	}, nil)
	if code != http.StatusOK || calls != 1 {
		t.Fatalf("lookup = %d (calls %d) %v", code, calls, payload)
	}
	offers := payload["part"].(map[string]any)["offers"].([]any)
	if len(offers) != 1 ||
		offers[0].(map[string]any)["distributor_part_number"] != "595-TMP421AQDCNRQ1" {
		t.Fatalf("unexpected offers: %v", offers)
	}

	if code, _ = call(t, web, token, "POST", "/api/lookup", map[string]any{
		"manufacturer": "TI",
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("missing part number must be 400, got %d", code)
	}

	status, payload := call(t, web, token, "GET", "/api/status", nil, nil)
	if status != http.StatusOK || payload["resolver"] != true {
		t.Fatalf("resolver must be reported available: %v", payload)
	}
}

func TestLookupWithoutRunnerIsUnavailable(t *testing.T) {
	server, web := newTestServer(t, nil)
	code, _ := call(t, web, server.Token(), "POST", "/api/lookup", map[string]any{
		"part_number": "TMP421-Q1", "manufacturer": "TI",
	}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without a runner, got %d", code)
	}
}

func TestStaticInterfaceIsServedWithStrictHeaders(t *testing.T) {
	_, web := newTestServer(t, nil)
	response, err := web.Client().Get(web.URL + "/")
	if err != nil {
		t.Fatalf("get index: %v", err)
	}
	defer response.Body.Close()
	page, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(page), "BOM Builder") {
		t.Fatalf("index = %d", response.StatusCode)
	}
	csp := response.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") ||
		strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("index must carry a strict CSP, got %q", csp)
	}
	for _, path := range []string{"/app.js", "/app.css"} {
		asset, err := web.Client().Get(web.URL + path)
		if err != nil || asset.StatusCode != http.StatusOK {
			t.Fatalf("asset %s unavailable", path)
		}
		asset.Body.Close()
	}
}
