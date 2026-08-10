// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package fx

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/money"
)

// DefaultECBURL is the ECB's daily euro reference-rate document. The
// rates are free, credential-less, and published each working day.
const DefaultECBURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

const maxECBBodyBytes = 1024 * 1024

// ECBClient fetches the daily EUR-based reference quotes.
type ECBClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewECBClientFromEnvironment builds a client. BOM_BUILDER_ECB_URL
// overrides the endpoint for tests and mirrors; like every endpoint
// override it must come from the process environment, never from .env.
func NewECBClientFromEnvironment() (*ECBClient, error) {
	endpoint := strings.TrimSpace(os.Getenv("BOM_BUILDER_ECB_URL"))
	if endpoint == "" {
		endpoint = DefaultECBURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, fmt.Errorf("ECB endpoint URL is invalid")
	}
	return &ECBClient{
		endpoint:   endpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type ecbEnvelope struct {
	DateCubes []struct {
		Time  string `xml:"time,attr"`
		Rates []struct {
			Currency string `xml:"currency,attr"`
			Rate     string `xml:"rate,attr"`
		} `xml:"Cube"`
	} `xml:"Cube>Cube"`
}

// FetchDaily retrieves and validates the current dated quote table.
func (client *ECBClient) FetchDaily(ctx context.Context) (Table, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
	if err != nil {
		return Table{}, fmt.Errorf("build ECB request: %w", err)
	}
	request.Header.Set("Accept", "application/xml")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Table{}, fmt.Errorf("fetch ECB quotes: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Table{}, fmt.Errorf("ECB quotes request returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxECBBodyBytes+1))
	if err != nil {
		return Table{}, fmt.Errorf("read ECB quotes: %w", err)
	}
	if len(body) > maxECBBodyBytes {
		return Table{}, fmt.Errorf("ECB quote document exceeds the size limit")
	}
	var envelope ecbEnvelope
	if err := xml.Unmarshal(body, &envelope); err != nil {
		return Table{}, fmt.Errorf("parse ECB quotes: %w", err)
	}
	if len(envelope.DateCubes) == 0 {
		return Table{}, fmt.Errorf("ECB quote document contains no dated rates")
	}
	dated := envelope.DateCubes[0]
	if strings.TrimSpace(dated.Time) == "" || len(dated.Rates) == 0 {
		return Table{}, fmt.Errorf("ECB quote document is missing its date or rates")
	}
	rates := make(map[string]money.Decimal, len(dated.Rates))
	for _, quoted := range dated.Rates {
		rate, parseErr := money.Parse(quoted.Rate)
		if parseErr != nil {
			return Table{}, fmt.Errorf(
				"ECB rate for %s is invalid: %w",
				quoted.Currency, parseErr,
			)
		}
		rates[quoted.Currency] = rate
	}
	return NewTable("ecb", dated.Time, "EUR", rates)
}
