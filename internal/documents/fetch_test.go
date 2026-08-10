// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package documents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testFetcher(body string, maxBytes int64) *Fetcher {
	return &Fetcher{
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader(body)),
				ContentLength: int64(len(body)),
				Header:        http.Header{"Content-Type": []string{"application/pdf"}},
				Request:       request,
			}, nil
		})},
		maxBytes: maxBytes,
		now: func() time.Time {
			return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
		},
		validateURL: func(context.Context, *url.URL) error { return nil },
	}
}

func TestFetchWritesVerifiedPDFAndMetadata(t *testing.T) {
	body := "%PDF-1.7\nexample\n%%EOF\n"
	fetcher := testFetcher(body, 1024)
	output := filepath.Join(t.TempDir(), "datasheet.pdf")
	artifact, err := fetcher.Fetch(context.Background(), "https://example.test/a.pdf", output)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(body))
	if !bytes.Equal(data, []byte(body)) ||
		artifact.SizeBytes != int64(len(body)) ||
		artifact.SHA256 != hex.EncodeToString(digest[:]) ||
		artifact.MIMEType != "application/pdf" ||
		!artifact.FetchedAt.Equal(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected artifact: %#v", artifact)
	}
}

func TestFetchRefusesExistingOutput(t *testing.T) {
	fetcher := testFetcher("%PDF-1.7\n%%EOF\n", 1024)
	output := filepath.Join(t.TempDir(), "datasheet.pdf")
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fetcher.Fetch(context.Background(), "https://example.test/a.pdf", output)
	var fetchError *FetchError
	if !errors.As(err, &fetchError) || fetchError.Kind != "exists" {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: %q, %v", data, readErr)
	}
}

func TestFetchRejectsNonPDFAndOversizedResponseWithoutOutput(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		max  int64
		kind string
	}{
		{name: "not pdf", body: "<html>no</html>", max: 1024, kind: "validation"},
		{name: "embedded marker", body: "<html>%PDF-1.7</html>", max: 1024, kind: "validation"},
		{name: "too large", body: "%PDF-1.7\nfar too large", max: 8, kind: "limit"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fetcher := testFetcher(testCase.body, testCase.max)
			output := filepath.Join(t.TempDir(), "datasheet.pdf")
			_, err := fetcher.Fetch(context.Background(), "https://example.test/a.pdf", output)
			var fetchError *FetchError
			if !errors.As(err, &fetchError) || fetchError.Kind != testCase.kind {
				t.Fatalf("error = %v", err)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("output exists after failure: %v", statErr)
			}
		})
	}
}

func TestPublicIPRejectsPrivateAndSharedNetworks(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1"} {
		if publicIP(net.ParseIP(value)) {
			t.Fatalf("%s should not be public", value)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address was rejected")
	}
}
