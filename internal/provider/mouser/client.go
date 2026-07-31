// Package mouser implements the Mouser Search API adapter.
package mouser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultEndpoint = "https://api.mouser.com/api/v2/search/partnumberandmanufacturer"
	maxResponseSize = 8 * 1024 * 1024
)

// Error is a sanitized provider failure with a stable kind.
type Error struct {
	Kind       string
	StatusCode int
	Message    string
}

func (providerError *Error) Error() string {
	if providerError == nil {
		return ""
	}
	if providerError.StatusCode != 0 {
		return fmt.Sprintf(
			"mouser %s (HTTP %d): %s",
			providerError.Kind,
			providerError.StatusCode,
			providerError.Message,
		)
	}
	return fmt.Sprintf("mouser %s: %s", providerError.Kind, providerError.Message)
}

// Client is a bounded, key-rotating Mouser Search API client.
type Client struct {
	httpClient  *http.Client
	endpoint    string
	keys        []string
	keyIndex    int
	maxAttempts int
	backoff     time.Duration

	mu           sync.Mutex
	requestCount int
}

// Config allows tests and callers to provide transport policy explicitly.
type Config struct {
	HTTPClient  *http.Client
	Endpoint    string
	APIKeys     []string
	MaxAttempts int
	Backoff     time.Duration
}

// New validates and constructs a client.
func New(configuration Config) (*Client, error) {
	keys := uniqueNonEmpty(configuration.APIKeys)
	if len(keys) == 0 {
		return nil, &Error{Kind: "configuration", Message: "no API key is configured"}
	}
	endpoint := strings.TrimSpace(configuration.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, &Error{Kind: "configuration", Message: "invalid API endpoint"}
	}
	httpClient := configuration.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	maxAttempts := configuration.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 3
	}
	if maxAttempts < 1 || maxAttempts > 8 {
		return nil, &Error{Kind: "configuration", Message: "max attempts must be between 1 and 8"}
	}
	backoff := configuration.Backoff
	if backoff == 0 {
		backoff = 250 * time.Millisecond
	}
	if backoff < 0 || backoff > 30*time.Second {
		return nil, &Error{Kind: "configuration", Message: "invalid retry backoff"}
	}
	return &Client{
		httpClient:  httpClient,
		endpoint:    endpoint,
		keys:        keys,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}, nil
}

// NewFromEnvironment reads credentials and non-secret transport overrides.
func NewFromEnvironment() (*Client, error) {
	keys := splitKeys(os.Getenv("MOUSER_API_KEYS"))
	if single := strings.TrimSpace(os.Getenv("MOUSER_API_KEY")); single != "" {
		keys = append(keys, single)
	}
	endpoint := strings.TrimSpace(os.Getenv("BOM_BUILDER_MOUSER_API_URL"))
	maxAttempts := 3
	if configured := strings.TrimSpace(os.Getenv("BOM_BUILDER_MOUSER_MAX_ATTEMPTS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil {
			return nil, &Error{
				Kind:    "configuration",
				Message: "BOM_BUILDER_MOUSER_MAX_ATTEMPTS must be an integer",
			}
		}
		maxAttempts = parsed
	}
	return New(Config{
		Endpoint:    endpoint,
		APIKeys:     keys,
		MaxAttempts: maxAttempts,
	})
}

// RequestCount returns the number of live HTTP requests made by this client.
func (client *Client) RequestCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.requestCount
}

// Search performs an official v2 part-number-and-manufacturer endpoint search.
// The manufacturer is deliberately filtered locally because Mouser's optional
// manufacturerName parameter requires an exact catalog spelling.
func (client *Client) Search(
	ctx context.Context,
	partNumber string,
	manufacturer string,
	exact bool,
) ([]Part, error) {
	partNumber = strings.TrimSpace(partNumber)
	if len(partNumber) < 3 || len(partNumber) > 40 {
		return nil, &Error{
			Kind:    "input",
			Message: "part number must contain between 3 and 40 characters",
		}
	}
	searchOption := "None"
	if exact {
		searchOption = "Exact"
	}
	body, err := json.Marshal(searchRequestRoot{
		Request: searchRequest{
			ManufacturerName: strings.TrimSpace(manufacturer),
			PartNumber:       partNumber,
			SearchOption:     searchOption,
		},
	})
	if err != nil {
		return nil, &Error{Kind: "internal", Message: "could not encode request"}
	}

	attempt := 0
	for {
		attempt++
		response, requestErr := client.do(ctx, body)
		if requestErr != nil {
			if ctx.Err() != nil {
				return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			if attempt >= client.maxAttempts {
				return nil, &Error{Kind: "transport", Message: "request failed after retries"}
			}
			if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
				return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}

		payload, decodeErr := decodeSearchResponse(response)
		status := response.StatusCode
		response.Body.Close()
		if decodeErr != nil {
			if status >= 500 && attempt < client.maxAttempts {
				if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
					return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
				}
				continue
			}
			return nil, decodeErr
		}
		if status >= 200 && status < 300 && len(payload.Errors) == 0 {
			return payload.SearchResults.Parts, nil
		}

		code, message := responseError(payload.Errors, status)
		dailyLimit := status == http.StatusForbidden &&
			strings.EqualFold(code, "TooManyRequests") &&
			strings.Contains(strings.ToLower(message), "per day")
		throttled := status == http.StatusTooManyRequests ||
			status == http.StatusForbidden && strings.EqualFold(code, "TooManyRequests")
		if dailyLimit || throttled {
			if client.rotateKey() {
				attempt--
				continue
			}
			if !dailyLimit && attempt < client.maxAttempts {
				if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
					return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
				}
				continue
			}
			kind := "rate_limit"
			if dailyLimit {
				kind = "quota"
			}
			return nil, &Error{Kind: kind, StatusCode: status, Message: client.sanitizeMessage(message)}
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, &Error{
				Kind:       "authentication",
				StatusCode: status,
				Message:    client.sanitizeMessage(message),
			}
		}
		if status >= 500 && attempt < client.maxAttempts {
			if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
				return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}
		return nil, &Error{
			Kind:       "response",
			StatusCode: status,
			Message:    client.sanitizeMessage(message),
		}
	}
}

func (client *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	client.mu.Lock()
	key := client.keys[client.keyIndex]
	client.requestCount++
	client.mu.Unlock()

	endpoint, err := url.Parse(client.endpoint)
	if err != nil {
		return nil, errors.New("invalid endpoint")
	}
	query := endpoint.Query()
	query.Set("apiKey", key)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint.String(),
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, errors.New("could not create request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "bom-builder-go/3")
	response, err := client.httpClient.Do(request)
	if err != nil {
		// net/http errors commonly include the full request URL. Do not return
		// their text because Mouser requires the API key in the query string.
		return nil, errors.New("HTTP transport failed")
	}
	return response, nil
}

func decodeSearchResponse(response *http.Response) (searchResponseRoot, error) {
	reader := io.LimitReader(response.Body, maxResponseSize+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return searchResponseRoot{}, &Error{Kind: "response", Message: "could not read response"}
	}
	if len(data) > maxResponseSize {
		return searchResponseRoot{}, &Error{Kind: "response", Message: "response exceeded size limit"}
	}
	var payload searchResponseRoot
	if len(bytes.TrimSpace(data)) == 0 {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return payload, &Error{Kind: "response", Message: "response was empty"}
		}
		return payload, nil
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return searchResponseRoot{}, &Error{Kind: "response", Message: "response was not valid JSON"}
	}
	return payload, nil
}

func (client *Client) rotateKey() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.keyIndex+1 >= len(client.keys) {
		return false
	}
	client.keyIndex++
	return true
}

func waitForRetry(ctx context.Context, base time.Duration, attempt int) error {
	delay := base
	for range max(attempt-1, 0) {
		if delay > 15*time.Second {
			delay = 30 * time.Second
			break
		}
		delay *= 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func responseError(errors []apiError, status int) (string, string) {
	if len(errors) > 0 {
		message := strings.TrimSpace(errors[0].Message)
		if message == "" {
			message = http.StatusText(status)
		}
		return strings.TrimSpace(errors[0].Code), message
	}
	message := http.StatusText(status)
	if message == "" {
		message = "unexpected provider response"
	}
	return "", message
}

func (client *Client) sanitizeMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "provider rejected the request"
	}
	client.mu.Lock()
	keys := append([]string(nil), client.keys...)
	client.mu.Unlock()
	for _, key := range keys {
		message = strings.ReplaceAll(message, key, "[REDACTED]")
	}
	if len(message) > 300 {
		// The byte cut may split a multi-byte rune; scrub the torn
		// tail so JSON error fields never carry invalid UTF-8.
		message = strings.ToValidUTF8(message[:300], "")
	}
	return message
}

func splitKeys(raw string) []string {
	return uniqueNonEmpty(strings.FieldsFunc(raw, func(character rune) bool {
		return character == ',' || character == ';' || character == '\n'
	}))
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type searchRequestRoot struct {
	Request searchRequest `json:"SearchByPartMfrNameRequest"`
}

type searchRequest struct {
	ManufacturerName string `json:"manufacturerName"`
	PartNumber       string `json:"mouserPartNumber"`
	SearchOption     string `json:"partSearchOptions"`
	PaysDuties       bool   `json:"mouserPaysCustomsAndDuties"`
}

type searchResponseRoot struct {
	Errors        []apiError    `json:"Errors"`
	SearchResults searchResults `json:"SearchResults"`
}

type apiError struct {
	Code         string `json:"Code"`
	Message      string `json:"Message"`
	PropertyName string `json:"PropertyName"`
}

type searchResults struct {
	NumberOfResult int    `json:"NumberOfResult"`
	Parts          []Part `json:"Parts"`
}
