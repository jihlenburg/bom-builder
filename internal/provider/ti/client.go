package ti

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
	defaultProductsURL = "https://transact.ti.com/v2/store/products"
	defaultTokenURL    = "https://transact.ti.com/v1/oauth/accesstoken"
	maxResponseBytes   = 8 * 1024 * 1024
)

// Error is a credential-safe TI provider failure with a stable kind.
type Error struct {
	Kind       string
	StatusCode int
	Message    string
}

func (providerError *Error) Error() string {
	if providerError == nil {
		return ""
	}
	if providerError.StatusCode > 0 {
		return fmt.Sprintf(
			"ti %s (HTTP %d): %s",
			providerError.Kind,
			providerError.StatusCode,
			providerError.Message,
		)
	}
	return fmt.Sprintf("ti %s: %s", providerError.Kind, providerError.Message)
}

// Config defines TI credentials, currency, transport, and retry policy.
type Config struct {
	HTTPClient   *http.Client
	ProductsURL  string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Currency     string
	MaxAttempts  int
	Backoff      time.Duration
}

// Client caches OAuth tokens and queries TI's direct-store product endpoint.
type Client struct {
	httpClient   *http.Client
	productsURL  string
	tokenURL     string
	clientID     string
	clientSecret string
	currency     string
	maxAttempts  int
	backoff      time.Duration

	mu             sync.Mutex
	accessToken    string
	tokenExpiresAt time.Time
	requestCount   int
}

// New validates and constructs a TI Store client.
func New(configuration Config) (*Client, error) {
	clientID := strings.TrimSpace(configuration.ClientID)
	clientSecret := strings.TrimSpace(configuration.ClientSecret)
	if clientID == "" || clientSecret == "" {
		return nil, &Error{
			Kind:    "configuration",
			Message: "API key and API secret are required",
		}
	}
	productsURL := strings.TrimRight(strings.TrimSpace(configuration.ProductsURL), "/")
	if productsURL == "" {
		productsURL = defaultProductsURL
	}
	tokenURL := strings.TrimSpace(configuration.TokenURL)
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	for _, endpoint := range []string{productsURL, tokenURL} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" ||
			parsed.Scheme != "https" && parsed.Scheme != "http" {
			return nil, &Error{Kind: "configuration", Message: "invalid API endpoint"}
		}
	}
	currency := strings.ToUpper(strings.TrimSpace(configuration.Currency))
	if currency == "" {
		currency = "USD"
	}
	if !validCurrency(currency) {
		return nil, &Error{Kind: "configuration", Message: "invalid TI price currency"}
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
		httpClient:   httpClient,
		productsURL:  productsURL,
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		currency:     currency,
		maxAttempts:  maxAttempts,
		backoff:      backoff,
	}, nil
}

// NewFromEnvironment reads TI Store credentials and non-secret policy.
func NewFromEnvironment() (*Client, error) {
	maxAttempts := 3
	if configured := strings.TrimSpace(os.Getenv("BOM_BUILDER_TI_MAX_ATTEMPTS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil {
			return nil, &Error{
				Kind:    "configuration",
				Message: "BOM_BUILDER_TI_MAX_ATTEMPTS must be an integer",
			}
		}
		maxAttempts = parsed
	}
	return New(Config{
		ProductsURL:  strings.TrimSpace(os.Getenv("BOM_BUILDER_TI_PRODUCTS_URL")),
		TokenURL:     strings.TrimSpace(os.Getenv("BOM_BUILDER_TI_TOKEN_URL")),
		ClientID:     os.Getenv("TI_STORE_API_KEY"),
		ClientSecret: os.Getenv("TI_STORE_API_SECRET"),
		Currency:     envDefault("TI_STORE_PRICE_CURRENCY", "USD"),
		MaxAttempts:  maxAttempts,
	})
}

// RequestCount includes OAuth and Store product requests.
func (client *Client) RequestCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.requestCount
}

// Currency returns the configured ISO price currency.
func (client *Client) Currency() string {
	return client.currency
}

// Product returns real-time inventory and pricing for one TI orderable.
func (client *Client) Product(ctx context.Context, partNumber string) (Product, error) {
	partNumber = strings.TrimSpace(partNumber)
	if partNumber == "" || len(partNumber) > 80 {
		return Product{}, &Error{Kind: "input", Message: "valid TI part number is required"}
	}
	refreshedAfterUnauthorized := false
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		token, err := client.token(ctx)
		if err != nil {
			return Product{}, err
		}
		endpoint, err := url.Parse(client.productsURL + "/" + url.PathEscape(partNumber))
		if err != nil {
			return Product{}, &Error{Kind: "internal", Message: "could not create product URL"}
		}
		query := endpoint.Query()
		query.Set("currency", client.currency)
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return Product{}, &Error{Kind: "internal", Message: "could not create product request"}
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("User-Agent", "bom-builder-go/3")
		response, err := client.do(request)
		if err != nil {
			if ctx.Err() != nil {
				return Product{}, &Error{Kind: "timeout", Message: "product request deadline exceeded"}
			}
			if attempt == client.maxAttempts {
				return Product{}, &Error{Kind: "transport", Message: "product request failed after retries"}
			}
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return Product{}, &Error{Kind: "timeout", Message: "product request deadline exceeded"}
			}
			continue
		}
		data, readErr := readResponse(response)
		status := response.StatusCode
		response.Body.Close()
		if readErr != nil {
			return Product{}, readErr
		}
		if status >= 200 && status < 300 {
			return normalizeProduct(partNumber, data)
		}
		if status == http.StatusUnauthorized && !refreshedAfterUnauthorized {
			client.clearToken()
			refreshedAfterUnauthorized = true
			attempt--
			continue
		}
		if retryableStatus(status) && attempt < client.maxAttempts {
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return Product{}, &Error{Kind: "timeout", Message: "product request deadline exceeded"}
			}
			continue
		}
		return Product{}, client.responseError(status, data)
	}
	return Product{}, &Error{Kind: "response", Message: "product request attempts exhausted"}
}

func (client *Client) token(ctx context.Context) (string, error) {
	client.mu.Lock()
	if client.accessToken != "" && time.Now().Before(client.tokenExpiresAt) {
		token := client.accessToken
		client.mu.Unlock()
		return token, nil
	}
	client.mu.Unlock()

	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		form := url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {client.clientID},
			"client_secret": {client.clientSecret},
		}
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			client.tokenURL,
			strings.NewReader(form.Encode()),
		)
		if err != nil {
			return "", &Error{Kind: "internal", Message: "could not create token request"}
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("User-Agent", "bom-builder-go/3")
		response, err := client.do(request)
		if err != nil {
			if ctx.Err() != nil {
				return "", &Error{Kind: "timeout", Message: "token request deadline exceeded"}
			}
			if attempt == client.maxAttempts {
				return "", &Error{Kind: "transport", Message: "token request failed after retries"}
			}
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return "", &Error{Kind: "timeout", Message: "token request deadline exceeded"}
			}
			continue
		}
		data, readErr := readResponse(response)
		status := response.StatusCode
		response.Body.Close()
		if readErr != nil {
			return "", readErr
		}
		if status < 200 || status >= 300 {
			if retryableStatus(status) && attempt < client.maxAttempts {
				if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
					return "", &Error{Kind: "timeout", Message: "token request deadline exceeded"}
				}
				continue
			}
			return "", client.responseError(status, data)
		}
		var token tokenResponse
		if err := decodeJSON(data, &token); err != nil ||
			strings.TrimSpace(token.AccessToken) == "" ||
			token.ExpiresIn < 1 {
			return "", &Error{Kind: "authentication", Message: "token response was invalid"}
		}
		lifetime := time.Duration(token.ExpiresIn) * time.Second
		if lifetime > 60*time.Second {
			lifetime -= 60 * time.Second
		}
		client.mu.Lock()
		client.accessToken = token.AccessToken
		client.tokenExpiresAt = time.Now().Add(lifetime)
		client.mu.Unlock()
		return token.AccessToken, nil
	}
	return "", &Error{Kind: "authentication", Message: "token request attempts exhausted"}
}

func (client *Client) do(request *http.Request) (*http.Response, error) {
	client.mu.Lock()
	client.requestCount++
	client.mu.Unlock()
	return client.httpClient.Do(request)
}

func (client *Client) responseError(status int, data []byte) error {
	kind := "response"
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = "request"
	case http.StatusUnauthorized:
		kind = "authentication"
	case http.StatusForbidden:
		kind = "authorization"
	case http.StatusNotFound:
		kind = "not_found"
	case http.StatusTooManyRequests:
		kind = "rate_limit"
	default:
		if status >= 500 {
			kind = "unavailable"
		}
	}
	message := http.StatusText(status)
	var problem struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Details []struct {
			Message string `json:"message"`
		} `json:"details"`
	}
	if json.Unmarshal(data, &problem) == nil {
		for _, candidate := range []string{problem.Message, problem.Error} {
			if strings.TrimSpace(candidate) != "" {
				message = candidate
				break
			}
		}
		if message == http.StatusText(status) && len(problem.Details) > 0 &&
			strings.TrimSpace(problem.Details[0].Message) != "" {
			message = problem.Details[0].Message
		}
	}
	return &Error{
		Kind:       kind,
		StatusCode: status,
		Message:    client.sanitize(message),
	}
}

func (client *Client) sanitize(message string) string {
	message = strings.TrimSpace(message)
	for _, secret := range []string{client.clientID, client.clientSecret} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	client.mu.Lock()
	token := client.accessToken
	client.mu.Unlock()
	if token != "" {
		message = strings.ReplaceAll(message, token, "[REDACTED]")
	}
	if message == "" {
		message = "provider rejected the request"
	}
	if len(message) > 300 {
		// The byte cut may split a multi-byte rune; scrub the torn
		// tail so JSON error fields never carry invalid UTF-8.
		message = strings.ToValidUTF8(message[:300], "")
	}
	return message
}

func (client *Client) clearToken() {
	client.mu.Lock()
	client.accessToken = ""
	client.tokenExpiresAt = time.Time{}
	client.mu.Unlock()
}

func normalizeProduct(query string, data []byte) (Product, error) {
	var raw rawProduct
	if err := decodeJSON(data, &raw); err != nil {
		return Product{}, &Error{Kind: "response", Message: "product response was not valid JSON"}
	}
	if raw.MinimumOrderQuantity.Present && raw.MinimumOrderQuantity.Value < 1 {
		return Product{}, &Error{Kind: "response", Message: "product response contained invalid MOQ"}
	}
	if raw.StandardPackQuantity.Present && raw.StandardPackQuantity.Value < 0 {
		return Product{}, &Error{Kind: "response", Message: "product response contained invalid pack quantity"}
	}
	if raw.PinCount.Present && raw.PinCount.Value < 0 {
		return Product{}, &Error{Kind: "response", Message: "product response contained invalid pin count"}
	}
	product := Product{
		Query:                query,
		TIPartNumber:         strings.TrimSpace(raw.TIPartNumber),
		GenericPartNumber:    strings.TrimSpace(raw.GenericPartNumber),
		BuyNowURL:            strings.TrimSpace(raw.BuyNowURL),
		Description:          strings.TrimSpace(raw.Description),
		PackageType:          strings.TrimSpace(raw.PackageType),
		PackageCarrier:       strings.TrimSpace(raw.PackageCarrier),
		CustomReel:           raw.CustomReel,
		LifeCycle:            strings.TrimSpace(raw.LifeCycle),
		MinimumOrderQuantity: positiveOrDefault(raw.MinimumOrderQuantity, 1),
		StandardPackQuantity: positiveOrDefault(raw.StandardPackQuantity, 0),
		PinCount:             positiveOrDefault(raw.PinCount, 0),
	}
	if product.TIPartNumber == "" && product.GenericPartNumber == "" {
		return Product{}, &Error{Kind: "response", Message: "product response omitted identifiers"}
	}
	var err error
	product.QuantityAvailable, err = nonNegativePointer(raw.Quantity)
	if err != nil {
		return Product{}, &Error{Kind: "response", Message: "product response contained invalid stock"}
	}
	product.OrderLimit, err = nonNegativePointer(raw.Limit)
	if err != nil {
		return Product{}, &Error{Kind: "response", Message: "product response contained invalid order limit"}
	}
	for _, rawSchedule := range raw.Pricing {
		currency := strings.ToUpper(strings.TrimSpace(rawSchedule.Currency))
		if !validCurrency(currency) {
			continue
		}
		schedule := PricingSchedule{Currency: currency}
		for _, rawBreak := range rawSchedule.PriceBreaks {
			if !rawBreak.Quantity.Present || rawBreak.Quantity.Value < 1 ||
				strings.TrimSpace(rawBreak.Price.String()) == "" {
				continue
			}
			schedule.PriceBreaks = append(schedule.PriceBreaks, PriceBreak{
				Quantity: rawBreak.Quantity.Value,
				Price:    rawBreak.Price,
			})
		}
		if len(schedule.PriceBreaks) > 0 {
			product.Pricing = append(product.Pricing, schedule)
		}
	}
	return product, nil
}

func positiveOrDefault(value flexibleInt, fallback int) int {
	if value.Present && value.Value > 0 {
		return value.Value
	}
	return fallback
}

func nonNegativePointer(value flexibleInt) (*int, error) {
	if !value.Present {
		return nil, nil
	}
	if value.Value < 0 {
		return nil, errors.New("negative value")
	}
	copied := value.Value
	return &copied, nil
}

func readResponse(response *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &Error{Kind: "response", Message: "could not read response"}
	}
	if len(data) > maxResponseBytes {
		return nil, &Error{Kind: "response", Message: "response exceeded size limit"}
	}
	return data, nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(destination)
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
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
