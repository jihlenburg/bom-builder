// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package nxp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultSearchBaseURL = "https://www.nxp.com/store:STORE"
	defaultPartBaseURL   = "https://www.nxp.com/part"
	searchEndpointMarker = "webapp-rest/api/search/getAsset/allResults/"
)

// Error is a stable NXP browser/store failure.
type Error struct {
	Kind    string
	Message string
}

func (providerError *Error) Error() string {
	if providerError == nil {
		return ""
	}
	return fmt.Sprintf("nxp %s: %s", providerError.Kind, providerError.Message)
}

// Config defines the local browser and bounded NXP store context.
type Config struct {
	BrowserPath   string
	Currency      string
	SearchBaseURL string
	PartBaseURL   string
	Timeout       time.Duration
}

// Client captures the NXP page's structured store response over Chrome CDP.
type Client struct {
	browserPath   string
	currency      string
	searchBaseURL string
	partBaseURL   string
	timeout       time.Duration

	// opMu serializes whole Search/PartDetail operations: cdpProcess is
	// a single-consumer transport, so concurrent operations would steal
	// each other's responses. mu guards only the small shared fields and
	// may be taken while opMu is held, never the other way around.
	opMu sync.Mutex

	mu           sync.Mutex
	process      *cdpProcess
	requestCount int
	disabled     error
}

// New validates and constructs a browser-backed NXP client.
func New(configuration Config) (*Client, error) {
	if !PipeTransportSupported() {
		return nil, &Error{
			Kind: "configuration",
			Message: "the NXP browser pipe transport is not supported on " +
				runtime.GOOS + " yet; select the other providers explicitly",
		}
	}
	browserPath := strings.TrimSpace(configuration.BrowserPath)
	if browserPath == "" {
		browserPath = FindSystemBrowser()
	}
	if browserPath == "" {
		return nil, &Error{Kind: "configuration", Message: "Chrome or Edge executable was not found"}
	}
	info, err := os.Stat(browserPath)
	if err != nil || info.IsDir() {
		return nil, &Error{Kind: "configuration", Message: "configured browser executable is invalid"}
	}
	currency := strings.ToUpper(strings.TrimSpace(configuration.Currency))
	if currency == "" {
		currency = "USD"
	}
	if !validCurrency(currency) {
		return nil, &Error{Kind: "configuration", Message: "invalid NXP store currency"}
	}
	searchBaseURL := strings.TrimRight(strings.TrimSpace(configuration.SearchBaseURL), "/")
	if searchBaseURL == "" {
		searchBaseURL = defaultSearchBaseURL
	}
	partBaseURL := strings.TrimRight(strings.TrimSpace(configuration.PartBaseURL), "/")
	if partBaseURL == "" {
		partBaseURL = defaultPartBaseURL
	}
	for _, endpoint := range []string{searchBaseURL, partBaseURL} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" ||
			parsed.Scheme != "https" && parsed.Scheme != "http" {
			return nil, &Error{Kind: "configuration", Message: "invalid NXP page URL"}
		}
	}
	timeout := configuration.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout < time.Second || timeout > 5*time.Minute {
		return nil, &Error{Kind: "configuration", Message: "NXP timeout must be between 1s and 5m"}
	}
	return &Client{
		browserPath:   browserPath,
		currency:      currency,
		searchBaseURL: searchBaseURL,
		partBaseURL:   partBaseURL,
		timeout:       timeout,
	}, nil
}

// NewFromEnvironment reads only non-secret browser and market configuration.
func NewFromEnvironment() (*Client, error) {
	timeout := 30 * time.Second
	if configured := strings.TrimSpace(os.Getenv("BOM_BUILDER_NXP_TIMEOUT")); configured != "" {
		parsed, err := time.ParseDuration(configured)
		if err != nil {
			return nil, &Error{Kind: "configuration", Message: "BOM_BUILDER_NXP_TIMEOUT is invalid"}
		}
		timeout = parsed
	}
	return New(Config{
		BrowserPath:   strings.TrimSpace(os.Getenv("BOM_BUILDER_NXP_BROWSER")),
		Currency:      envDefault("NXP_STORE_CURRENCY", "USD"),
		SearchBaseURL: strings.TrimSpace(os.Getenv("BOM_BUILDER_NXP_SEARCH_URL")),
		PartBaseURL:   strings.TrimSpace(os.Getenv("BOM_BUILDER_NXP_PART_URL")),
		Timeout:       timeout,
	})
}

// PipeTransportSupported reports whether this host can run the adapter's
// browser transport. The DevTools connection rides on inherited file
// descriptors 3 and 4 (--remote-debugging-pipe), which os/exec cannot
// provide on Windows: exec.Cmd.ExtraFiles is POSIX-only. Failing here is
// explicit and immediate instead of a confusing launch error.
func PipeTransportSupported() bool {
	return runtime.GOOS != "windows"
}

// FindSystemBrowser returns a supported Chrome/Edge executable path.
func FindSystemBrowser() string {
	if runtime.GOOS == "darwin" {
		for _, candidate := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	if runtime.GOOS == "windows" {
		// Chrome and Edge do not register themselves on PATH on Windows;
		// they install under the Program Files trees (Edge ships with the
		// operating system) or, for per-user Chrome installs, under
		// LocalAppData. Resolve the roots from the environment rather
		// than hard-coding drive letters.
		for _, root := range []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("LocalAppData"),
		} {
			if strings.TrimSpace(root) == "" {
				continue
			}
			for _, candidate := range []string{
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(root, "Chromium", "Application", "chrome.exe"),
			} {
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate
				}
			}
		}
	}
	commands := []string{
		"google-chrome",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
	}
	if runtime.GOOS == "windows" {
		// exec.LookPath consults PATHEXT, so extension-free names find
		// chrome.exe/msedge.exe when a browser is on PATH after all.
		commands = []string{"chrome", "msedge", "chromium"}
	}
	for _, command := range commands {
		if path, err := exec.LookPath(command); err == nil {
			if absolute, err := filepath.Abs(path); err == nil {
				return absolute
			}
			return path
		}
	}
	return ""
}

// BrowserPath returns the configured non-secret executable path.
func (client *Client) BrowserPath() string {
	return client.browserPath
}

// Currency returns the explicit NXP store price context.
func (client *Client) Currency() string {
	return client.currency
}

// RequestCount counts logical store/detail page navigations.
func (client *Client) RequestCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.requestCount
}

// Search captures and validates the structured NXP store-search payload.
func (client *Client) Search(
	ctx context.Context,
	query string,
) (*SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 80 {
		return nil, &Error{Kind: "input", Message: "valid NXP part number is required"}
	}
	// A disabled client refuses before touching the browser at all.
	if err := client.disabledError(); err != nil {
		return nil, err
	}
	client.opMu.Lock()
	defer client.opMu.Unlock()
	operationContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	process, err := client.ensureProcess(operationContext)
	if err != nil {
		return nil, err
	}
	process.clearEvents()
	if err := process.callPage(operationContext, "Page.enable", map[string]any{}, nil); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	if err := process.callPage(operationContext, "Network.enable", map[string]any{}, nil); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	searchURL := client.searchURL(query)
	client.incrementRequests()
	if err := process.callPage(operationContext, "Page.navigate", map[string]any{
		"url": searchURL,
	}, nil); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	params, err := process.waitEvent(
		operationContext,
		"Network.responseReceived",
		func(raw json.RawMessage) bool {
			var event struct {
				Response struct {
					URL string `json:"url"`
				} `json:"response"`
			}
			return json.Unmarshal(raw, &event) == nil &&
				strings.Contains(event.Response.URL, searchEndpointMarker)
		},
	)
	if err != nil {
		// A missing response event is transient (slow page, dropped
		// navigation): fail this lookup only. Disabling here would
		// let one slow page kill direct pricing for the whole run.
		return nil, client.browserFailure(operationContext, process, err)
	}
	var responseEvent struct {
		RequestID string `json:"requestId"`
		Response  struct {
			URL    string  `json:"url"`
			Status float64 `json:"status"`
		} `json:"response"`
	}
	if json.Unmarshal(params, &responseEvent) != nil || responseEvent.RequestID == "" {
		// An event that no longer carries a request ID is evidence of
		// CDP/site drift: fail closed for the rest of the run.
		client.disable(errors.New("NXP store search response event changed shape"))
		return nil, &Error{Kind: "schema", Message: "NXP store search response event changed shape"}
	}
	if responseEvent.Response.Status < 200 || responseEvent.Response.Status >= 300 {
		// Server-side statuses (5xx, 429, redirects) are transient
		// conditions, not schema drift; the next lookup may succeed.
		return nil, &Error{
			Kind:    "response",
			Message: fmt.Sprintf("NXP store search returned HTTP status %d", int(responseEvent.Response.Status)),
		}
	}
	// CDP guarantees the response body only after Network.loadingFinished
	// for the same request; fetching on responseReceived races Chrome and
	// intermittently yields "No data found for resource".
	if _, err := process.waitEvent(
		operationContext,
		"Network.loadingFinished",
		func(raw json.RawMessage) bool {
			var event struct {
				RequestID string `json:"requestId"`
			}
			return json.Unmarshal(raw, &event) == nil &&
				event.RequestID == responseEvent.RequestID
		},
	); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	var bodyResult struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := process.callPage(operationContext, "Network.getResponseBody", map[string]any{
		"requestId": responseEvent.RequestID,
	}, &bodyResult); err != nil {
		// Body unavailability after loadingFinished is still treated
		// as transient: eviction from Chrome's buffer is timing, not
		// schema drift.
		return nil, client.browserFailure(operationContext, process, err)
	}
	process.callPage(operationContext, "Network.disable", map[string]any{}, nil)
	body := []byte(bodyResult.Body)
	if bodyResult.Base64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(bodyResult.Body)
		if err != nil {
			client.disable(errors.New("NXP store search body encoding changed"))
			return nil, &Error{Kind: "schema", Message: "NXP store search body encoding changed"}
		}
		body = decoded
	}
	result, err := selectBestResult(query, body, client.currency)
	if err != nil {
		client.disable(err)
		return nil, &Error{Kind: "schema", Message: err.Error()}
	}
	return result, nil
}

// PartDetail reads MOQ and package-quantity text from the NXP part page.
func (client *Client) PartDetail(
	ctx context.Context,
	query, matchedPartID string,
) (*PartDetail, error) {
	// The same refusal and serialization discipline as Search: a disabled
	// client must not touch the browser, and cdpProcess is single-consumer.
	if err := client.disabledError(); err != nil {
		return nil, err
	}
	client.opMu.Lock()
	defer client.opMu.Unlock()
	operationContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	process, err := client.ensureProcess(operationContext)
	if err != nil {
		return nil, err
	}
	process.clearEvents()
	if err := process.callPage(operationContext, "Page.enable", map[string]any{}, nil); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	client.incrementRequests()
	if err := process.callPage(operationContext, "Page.navigate", map[string]any{
		"url": client.partURL(query),
	}, nil); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	if _, err := process.waitEvent(operationContext, "Page.loadEventFired", nil); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	expression := `new Promise(resolve => setTimeout(() => resolve(` +
		`document.body ? document.body.innerText : ""), 1500))`
	var evaluated struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := process.callPage(operationContext, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
	}, &evaluated); err != nil {
		return nil, client.browserFailure(operationContext, process, err)
	}
	return parsePartDetail(query, matchedPartID, evaluated.Result.Value), nil
}

// browserFailure converts a transport-level error for callers and, when the
// underlying browser connection is gone, discards the dead process so the
// next lookup launches a fresh browser instead of failing forever. Transient
// failures (timeouts, dropped events) never disable the client — only
// confirmed schema drift does, at its detection sites.
func (client *Client) browserFailure(ctx context.Context, process *cdpProcess, err error) error {
	if errors.Is(err, errBrowserGone) {
		client.mu.Lock()
		if client.process == process {
			client.process = nil
		}
		client.mu.Unlock()
		process.Close()
	}
	return browserError(ctx, err)
}

func (client *Client) ensureProcess(ctx context.Context) (*cdpProcess, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.process != nil {
		return client.process, nil
	}
	process, err := launchCDP(ctx, client.browserPath)
	if err != nil {
		return nil, &Error{Kind: "browser", Message: err.Error()}
	}
	client.process = process
	return process, nil
}

func (client *Client) disabledError() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.disabled == nil {
		return nil
	}
	return &Error{Kind: "schema", Message: client.disabled.Error()}
}

func (client *Client) disable(err error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.disabled == nil {
		client.disabled = err
	}
}

func (client *Client) incrementRequests() {
	client.mu.Lock()
	client.requestCount++
	client.mu.Unlock()
}

func (client *Client) searchURL(query string) string {
	endpoint, _ := url.Parse(client.searchBaseURL)
	values := endpoint.Query()
	values.Set("collection", "salesitem")
	values.Set("keyword", query)
	values.Set("language", "en")
	values.Set("max", "12")
	values.Set("query", "typeTax>>t000")
	values.Set("siblings", "false")
	values.Set("start", "0")
	endpoint.RawQuery = values.Encode()
	return endpoint.String()
}

func (client *Client) partURL(query string) string {
	return client.partBaseURL + "/" + url.PathEscape(strings.TrimSpace(query))
}

// Close terminates the private headless browser and removes its temporary profile.
func (client *Client) Close() {
	client.mu.Lock()
	process := client.process
	client.process = nil
	client.mu.Unlock()
	if process != nil {
		process.Close()
	}
}

func browserError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &Error{Kind: "timeout", Message: "NXP browser deadline exceeded"}
	}
	return &Error{Kind: "browser", Message: err.Error()}
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, character := range currency {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
