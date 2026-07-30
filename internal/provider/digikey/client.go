// Package digikey implements Digi-Key Product Information V4.
package digikey

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
	defaultAPIBaseURL = "https://api.digikey.com"
	defaultTokenURL   = "https://api.digikey.com/v1/oauth2/token"
	maxResponseBytes  = 8 * 1024 * 1024
)

// Error is a credential-safe provider failure with a stable kind.
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
			"digikey %s (HTTP %d): %s",
			providerError.Kind,
			providerError.StatusCode,
			providerError.Message,
		)
	}
	return fmt.Sprintf("digikey %s: %s", providerError.Kind, providerError.Message)
}

// Config defines credentials, locale, transport, and retry policy.
type Config struct {
	HTTPClient   *http.Client
	APIBaseURL   string
	TokenURL     string
	ClientID     string
	ClientSecret string
	AccountID    string
	Locale       Locale
	MaxAttempts  int
	Backoff      time.Duration
}

// Client caches OAuth tokens and performs bounded Product Information requests.
type Client struct {
	httpClient   *http.Client
	apiBaseURL   string
	tokenURL     string
	clientID     string
	clientSecret string
	accountID    string
	locale       Locale
	maxAttempts  int
	backoff      time.Duration

	mu              sync.Mutex
	accessToken     string
	tokenExpiresAt  time.Time
	requestCount    int
	rateLimitRemain *int
}

// New validates and constructs a Digi-Key client.
func New(configuration Config) (*Client, error) {
	clientID := strings.TrimSpace(configuration.ClientID)
	clientSecret := strings.TrimSpace(configuration.ClientSecret)
	accountID := strings.TrimSpace(configuration.AccountID)
	if clientID == "" || clientSecret == "" {
		return nil, &Error{
			Kind:    "configuration",
			Message: "client ID and client secret are required",
		}
	}
	if accountID == "" {
		return nil, &Error{
			Kind:    "configuration",
			Message: "account ID is required for two-legged pricing",
		}
	}
	apiBaseURL := strings.TrimRight(strings.TrimSpace(configuration.APIBaseURL), "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	tokenURL := strings.TrimSpace(configuration.TokenURL)
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	for _, endpoint := range []string{apiBaseURL, tokenURL} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" ||
			parsed.Scheme != "https" && parsed.Scheme != "http" {
			return nil, &Error{Kind: "configuration", Message: "invalid API endpoint"}
		}
	}
	locale, err := validateLocale(configuration.Locale)
	if err != nil {
		return nil, &Error{Kind: "configuration", Message: err.Error()}
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
		apiBaseURL:   apiBaseURL,
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		accountID:    accountID,
		locale:       locale,
		maxAttempts:  maxAttempts,
		backoff:      backoff,
	}, nil
}

// NewFromEnvironment reads Digi-Key credentials and locale configuration.
func NewFromEnvironment() (*Client, error) {
	maxAttempts := 3
	if configured := strings.TrimSpace(os.Getenv("BOM_BUILDER_DIGIKEY_MAX_ATTEMPTS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil {
			return nil, &Error{
				Kind:    "configuration",
				Message: "BOM_BUILDER_DIGIKEY_MAX_ATTEMPTS must be an integer",
			}
		}
		maxAttempts = parsed
	}
	return New(Config{
		APIBaseURL:   strings.TrimSpace(os.Getenv("BOM_BUILDER_DIGIKEY_API_BASE_URL")),
		TokenURL:     strings.TrimSpace(os.Getenv("BOM_BUILDER_DIGIKEY_TOKEN_URL")),
		ClientID:     os.Getenv("DIGIKEY_CLIENT_ID"),
		ClientSecret: os.Getenv("DIGIKEY_CLIENT_SECRET"),
		AccountID:    os.Getenv("DIGIKEY_ACCOUNT_ID"),
		Locale: Locale{
			Site:          envDefault("DIGIKEY_LOCALE_SITE", "DE"),
			Language:      envDefault("DIGIKEY_LOCALE_LANGUAGE", "en"),
			Currency:      envDefault("DIGIKEY_LOCALE_CURRENCY", "EUR"),
			ShipToCountry: envDefault("DIGIKEY_LOCALE_SHIP_TO_COUNTRY", "de"),
		},
		MaxAttempts: maxAttempts,
	})
}

// RequestCount includes OAuth and Product Information requests.
func (client *Client) RequestCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.requestCount
}

// RateLimitRemaining returns the most recent API quota header when available.
func (client *Client) RateLimitRemaining() *int {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.rateLimitRemain == nil {
		return nil
	}
	value := *client.rateLimitRemain
	return &value
}

// Locale returns the validated market context.
func (client *Client) Locale() Locale {
	return client.locale
}

// PricingByQuantity requests all account and standard packaging options.
func (client *Client) PricingByQuantity(
	ctx context.Context,
	productNumber string,
	quantity int,
) (PricingResult, error) {
	productNumber = strings.TrimSpace(productNumber)
	if productNumber == "" || quantity < 1 {
		return PricingResult{}, &Error{Kind: "input", Message: "product number and quantity are required"}
	}
	path := "/products/v4/search/" + url.PathEscape(productNumber) +
		"/pricingbyquantity/" + strconv.Itoa(quantity)
	data, headerMode, rateRemaining, err := client.apiGET(ctx, path)
	if err != nil {
		return PricingResult{}, err
	}
	var raw pricingResponse
	if err := decodeJSON(data, &raw); err != nil {
		return PricingResult{}, &Error{Kind: "response", Message: "pricing response was not valid JSON"}
	}
	result := PricingResult{
		RequestedProduct:       strings.TrimSpace(raw.RequestedProduct),
		RequestedQuantity:      raw.RequestedQuantity,
		ManufacturerName:       strings.TrimSpace(raw.Manufacturer.Name),
		ManufacturerPartNumber: strings.TrimSpace(raw.ManufacturerPartNumber),
		ProductURL:             strings.TrimSpace(raw.ProductURL),
		Currency:               strings.ToUpper(strings.TrimSpace(raw.SettingsUsed.SearchLocaleUsed.Currency)),
		HeaderMode:             headerMode,
		RateLimitRemaining:     rateRemaining,
		MyPricingOptions:       normalizeOptions(raw.MyPricingOptions),
		StandardPricingOptions: normalizeOptions(raw.StandardPricingOptions),
	}
	if result.Currency == "" {
		result.Currency = client.locale.Currency
	}
	return result, nil
}

// ProductInformation returns document links plus product and
// per-variation stock from ProductDetails. This endpoint is the only
// reliable Digi-Key stock source; see ProductInfo.
func (client *Client) ProductInformation(
	ctx context.Context,
	productNumber string,
) (ProductInfo, error) {
	productNumber = strings.TrimSpace(productNumber)
	if productNumber == "" {
		return ProductInfo{}, &Error{Kind: "input", Message: "product number is required"}
	}
	path := "/products/v4/search/" + url.PathEscape(productNumber) + "/productdetails"
	data, _, _, err := client.apiGET(ctx, path)
	if err != nil {
		return ProductInfo{}, err
	}
	var raw productDetailsResponse
	if err := decodeJSON(data, &raw); err != nil {
		return ProductInfo{}, &Error{Kind: "response", Message: "product response was not valid JSON"}
	}
	info := ProductInfo{
		DatasheetURL:      strings.TrimSpace(raw.Product.DatasheetURL),
		ProductURL:        strings.TrimSpace(raw.Product.ProductURL),
		QuantityAvailable: raw.Product.QuantityAvailable,
	}
	for _, variation := range raw.Product.ProductVariations {
		sku := strings.TrimSpace(variation.DigiKeyProductNumber)
		if sku == "" || variation.QuantityAvailableforPackageType == nil {
			continue
		}
		if info.VariationQuantities == nil {
			info.VariationQuantities = make(map[string]int)
		}
		info.VariationQuantities[sku] = *variation.QuantityAvailableforPackageType
	}
	return info, nil
}

func (client *Client) apiGET(
	ctx context.Context,
	path string,
) ([]byte, string, *int, error) {
	refreshedAfterUnauthorized := false
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		token, err := client.token(ctx)
		if err != nil {
			return nil, "", nil, err
		}
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			client.apiBaseURL+path,
			nil,
		)
		if err != nil {
			return nil, "", nil, &Error{Kind: "internal", Message: "could not create request"}
		}
		client.applyHeaders(request, token)
		client.incrementRequests()
		response, transportErr := client.httpClient.Do(request)
		if transportErr != nil {
			if ctx.Err() != nil {
				return nil, "", nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			if attempt == client.maxAttempts {
				return nil, "", nil, &Error{Kind: "transport", Message: "request failed after retries"}
			}
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return nil, "", nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}
		data, readErr := readResponse(response)
		rateRemaining := headerInteger(response.Header, "X-RateLimit-Remaining")
		client.setRateLimit(rateRemaining)
		status := response.StatusCode
		response.Body.Close()
		if readErr != nil {
			return nil, "", nil, readErr
		}
		if status >= 200 && status < 300 {
			return data, "account_id", rateRemaining, nil
		}
		if status == http.StatusUnauthorized && !refreshedAfterUnauthorized {
			client.clearToken()
			refreshedAfterUnauthorized = true
			attempt--
			continue
		}
		if (status == http.StatusTooManyRequests || status >= 500) &&
			attempt < client.maxAttempts {
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return nil, "", nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}
		return nil, "", nil, client.responseError(status, data)
	}
	return nil, "", nil, &Error{Kind: "response", Message: "request attempts exhausted"}
}

func (client *Client) token(ctx context.Context) (string, error) {
	client.mu.Lock()
	if client.accessToken != "" && time.Now().Before(client.tokenExpiresAt) {
		token := client.accessToken
		client.mu.Unlock()
		return token, nil
	}
	client.mu.Unlock()

	form := url.Values{
		"client_id":     {client.clientID},
		"client_secret": {client.clientSecret},
		"grant_type":    {"client_credentials"},
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
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	client.incrementRequests()
	response, transportErr := client.httpClient.Do(request)
	if transportErr != nil {
		if ctx.Err() != nil {
			return "", &Error{Kind: "timeout", Message: "token request deadline exceeded"}
		}
		return "", &Error{Kind: "transport", Message: "token request failed"}
	}
	data, readErr := readResponse(response)
	status := response.StatusCode
	response.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if status < 200 || status >= 300 {
		return "", client.responseError(status, data)
	}
	var token tokenResponse
	if err := decodeJSON(data, &token); err != nil ||
		strings.TrimSpace(token.AccessToken) == "" ||
		token.ExpiresIn < 1 {
		return "", &Error{Kind: "authentication", Message: "token response was invalid"}
	}
	lifetime := time.Duration(token.ExpiresIn) * time.Second
	if lifetime > 30*time.Second {
		lifetime -= 30 * time.Second
	}
	client.mu.Lock()
	client.accessToken = token.AccessToken
	client.tokenExpiresAt = time.Now().Add(lifetime)
	client.mu.Unlock()
	return token.AccessToken, nil
}

func (client *Client) applyHeaders(request *http.Request, token string) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-DIGIKEY-Client-Id", client.clientID)
	request.Header.Set("X-DIGIKEY-Account-Id", client.accountID)
	request.Header.Set("X-DIGIKEY-Locale-Site", client.locale.Site)
	request.Header.Set("X-DIGIKEY-Locale-Language", client.locale.Language)
	request.Header.Set("X-DIGIKEY-Locale-Currency", client.locale.Currency)
	request.Header.Set("X-DIGIKEY-Locale-ShipToCountry", client.locale.ShipToCountry)
	request.Header.Set("User-Agent", "bom-builder-go/3")
}

func (client *Client) responseError(status int, data []byte) error {
	kind := "response"
	switch status {
	case http.StatusBadRequest:
		kind = "request"
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = "authentication"
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
		Title   string `json:"title"`
		Detail  string `json:"detail"`
		Message string `json:"ErrorMessage"`
	}
	if json.Unmarshal(data, &problem) == nil {
		for _, candidate := range []string{problem.Detail, problem.Message, problem.Title} {
			if strings.TrimSpace(candidate) != "" {
				message = candidate
				break
			}
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
	for _, secret := range []string{
		client.clientID,
		client.clientSecret,
		client.accountID,
	} {
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
		message = message[:300]
	}
	return message
}

func (client *Client) clearToken() {
	client.mu.Lock()
	client.accessToken = ""
	client.tokenExpiresAt = time.Time{}
	client.mu.Unlock()
}

func (client *Client) incrementRequests() {
	client.mu.Lock()
	client.requestCount++
	client.mu.Unlock()
}

func (client *Client) setRateLimit(value *int) {
	if value == nil {
		return
	}
	client.mu.Lock()
	copied := *value
	client.rateLimitRemain = &copied
	client.mu.Unlock()
}

func validateLocale(locale Locale) (Locale, error) {
	locale.Site = strings.ToUpper(strings.TrimSpace(locale.Site))
	locale.Language = strings.ToLower(strings.TrimSpace(locale.Language))
	locale.Currency = strings.ToUpper(strings.TrimSpace(locale.Currency))
	locale.ShipToCountry = strings.ToLower(strings.TrimSpace(locale.ShipToCountry))
	if len(locale.Site) != 2 ||
		len(locale.Language) < 2 || len(locale.Language) > 3 ||
		len(locale.Currency) != 3 ||
		len(locale.ShipToCountry) != 2 {
		return Locale{}, errors.New("invalid Digi-Key locale")
	}
	return locale, nil
}

func normalizeOptions(raw []rawPricingOption) []PricingOption {
	options := make([]PricingOption, 0, len(raw))
	for _, option := range raw {
		products := make([]PricingProduct, 0, len(option.Products))
		for _, product := range option.Products {
			products = append(products, PricingProduct{
				ProductNumber:        strings.TrimSpace(product.ProductNumber),
				Quantity:             product.Quantity,
				MinimumOrderQuantity: product.MinimumOrderQuantity,
				UnitPrice:            product.UnitPrice,
				ExtendedPrice:        product.ExtendedPrice,
				PackageType:          strings.TrimSpace(product.PackageType.Name),
				Marketplace:          product.Marketplace,
			})
		}
		options = append(options, PricingOption{
			Name:              strings.TrimSpace(option.Name),
			TotalQuantity:     option.TotalQuantity,
			TotalPrice:        option.TotalPrice,
			QuantityAvailable: option.QuantityAvailable,
			Products:          products,
		})
	}
	return options
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

func headerInteger(header http.Header, name string) *int {
	value := strings.TrimSpace(header.Get(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &parsed
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

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
