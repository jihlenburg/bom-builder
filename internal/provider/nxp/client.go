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

	mu           sync.Mutex
	process      *cdpProcess
	requestCount int
	disabled     error
}

// New validates and constructs a browser-backed NXP client.
func New(configuration Config) (*Client, error) {
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
	for _, command := range []string{
		"google-chrome",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
	} {
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
	operationContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	process, err := client.ensureProcess(operationContext)
	if err != nil {
		return nil, err
	}
	if err := client.disabledError(); err != nil {
		return nil, err
	}
	process.clearEvents()
	if err := process.callPage(operationContext, "Page.enable", map[string]any{}, nil); err != nil {
		return nil, browserError(operationContext, err)
	}
	if err := process.callPage(operationContext, "Network.enable", map[string]any{}, nil); err != nil {
		return nil, browserError(operationContext, err)
	}
	searchURL := client.searchURL(query)
	client.incrementRequests()
	if err := process.callPage(operationContext, "Page.navigate", map[string]any{
		"url": searchURL,
	}, nil); err != nil {
		return nil, browserError(operationContext, err)
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
		client.disable(errors.New("NXP store search response was not observed"))
		return nil, browserError(operationContext, err)
	}
	var responseEvent struct {
		RequestID string `json:"requestId"`
		Response  struct {
			URL    string  `json:"url"`
			Status float64 `json:"status"`
		} `json:"response"`
	}
	if json.Unmarshal(params, &responseEvent) != nil ||
		responseEvent.RequestID == "" ||
		responseEvent.Response.Status < 200 ||
		responseEvent.Response.Status >= 300 {
		client.disable(errors.New("NXP store search returned an invalid response"))
		return nil, &Error{Kind: "response", Message: "NXP store search returned an invalid response"}
	}
	var bodyResult struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := process.callPage(operationContext, "Network.getResponseBody", map[string]any{
		"requestId": responseEvent.RequestID,
	}, &bodyResult); err != nil {
		client.disable(errors.New("NXP store search body was unavailable"))
		return nil, browserError(operationContext, err)
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
	operationContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	process, err := client.ensureProcess(operationContext)
	if err != nil {
		return nil, err
	}
	process.clearEvents()
	if err := process.callPage(operationContext, "Page.enable", map[string]any{}, nil); err != nil {
		return nil, browserError(operationContext, err)
	}
	client.incrementRequests()
	if err := process.callPage(operationContext, "Page.navigate", map[string]any{
		"url": client.partURL(query),
	}, nil); err != nil {
		return nil, browserError(operationContext, err)
	}
	if _, err := process.waitEvent(operationContext, "Page.loadEventFired", nil); err != nil {
		return nil, browserError(operationContext, err)
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
		return nil, browserError(operationContext, err)
	}
	return parsePartDetail(query, matchedPartID, evaluated.Result.Value), nil
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
