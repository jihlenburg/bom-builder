// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package documents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

const DefaultMaxBytes int64 = 25 * 1024 * 1024

// FetchError is a stable document input, transport, validation, or write error.
type FetchError struct {
	Kind    string
	Message string
}

func (fetchError *FetchError) Error() string {
	if fetchError == nil {
		return ""
	}
	return "document " + fetchError.Kind + ": " + fetchError.Message
}

// Fetcher downloads HTTPS PDFs through a public-network-only transport.
type Fetcher struct {
	client      *http.Client
	maxBytes    int64
	now         func() time.Time
	validateURL func(context.Context, *url.URL) error
}

// NewFetcher constructs a bounded production fetcher.
func NewFetcher(maxBytes int64) (*Fetcher, error) {
	if maxBytes < 1 || maxBytes > 100*1024*1024 {
		return nil, &FetchError{
			Kind:    "input",
			Message: "maximum document size must be between 1 and 104857600 bytes",
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           publicDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	fetcher := &Fetcher{
		maxBytes:    maxBytes,
		now:         time.Now,
		validateURL: validatePublicHTTPSURL,
	}
	fetcher.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("document redirect limit exceeded")
			}
			return fetcher.validateURL(request.Context(), request.URL)
		},
	}
	return fetcher, nil
}

// Fetch downloads and verifies one PDF, refusing to overwrite an existing path.
func (fetcher *Fetcher) Fetch(
	ctx context.Context,
	sourceURL, outputPath string,
) (*contract.DocumentArtifact, error) {
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || parsed == nil {
		return nil, inputError("a valid HTTPS document URL is required")
	}
	if err := fetcher.validateURL(ctx, parsed); err != nil {
		return nil, inputError(err.Error())
	}
	absolutePath, err := validatedOutputPath(outputPath)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, inputError("a valid HTTPS document URL is required")
	}
	request.Header.Set("Accept", "application/pdf")
	request.Header.Set("User-Agent", "bom-builder/3 documents")
	response, err := fetcher.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &FetchError{Kind: "timeout", Message: "document request deadline exceeded"}
		}
		return nil, &FetchError{Kind: "network", Message: "document request failed"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &FetchError{
			Kind:    "response",
			Message: "document server returned HTTP " + strconv.Itoa(response.StatusCode),
		}
	}
	if response.ContentLength > fetcher.maxBytes {
		return nil, sizeError(fetcher.maxBytes)
	}

	parent := filepath.Dir(absolutePath)
	base := filepath.Base(absolutePath)
	temporary, err := os.CreateTemp(parent, "."+base+".partial-*")
	if err != nil {
		return nil, &FetchError{Kind: "filesystem", Message: "could not create temporary output"}
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		temporary.Close()
		if !keepTemporary {
			os.Remove(temporaryPath)
		}
	}()

	limited := &io.LimitedReader{R: response.Body, N: fetcher.maxBytes + 1}
	prefixLimit := int64(1024)
	if fetcher.maxBytes+1 < prefixLimit {
		prefixLimit = fetcher.maxBytes + 1
	}
	prefix, err := io.ReadAll(io.LimitReader(limited, prefixLimit))
	if err != nil {
		return nil, &FetchError{Kind: "network", Message: "document response could not be read"}
	}
	header := bytes.TrimLeft(prefix, "\x00\t\n\r ")
	if !bytes.HasPrefix(header, []byte("%PDF-")) {
		return nil, &FetchError{Kind: "validation", Message: "document response is not a PDF"}
	}
	hasher := sha256.New()
	writer := io.MultiWriter(temporary, hasher)
	written, err := writer.Write(prefix)
	if err == nil {
		var copied int64
		copied, err = io.Copy(writer, limited)
		written += int(copied)
	}
	if err != nil {
		return nil, &FetchError{Kind: "filesystem", Message: "document output could not be written"}
	}
	if int64(written) > fetcher.maxBytes {
		return nil, sizeError(fetcher.maxBytes)
	}
	if err := temporary.Sync(); err != nil {
		return nil, &FetchError{Kind: "filesystem", Message: "document output could not be synchronized"}
	}
	if err := temporary.Close(); err != nil {
		return nil, &FetchError{Kind: "filesystem", Message: "document output could not be finalized"}
	}
	if err := installExclusive(temporaryPath, absolutePath); err != nil {
		return nil, err
	}
	keepTemporary = false

	finalURL := parsed.String()
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return &contract.DocumentArtifact{
		SourceURL:  parsed.String(),
		FinalURL:   finalURL,
		OutputPath: absolutePath,
		MIMEType:   "application/pdf",
		SizeBytes:  int64(written),
		SHA256:     hex.EncodeToString(hasher.Sum(nil)),
		FetchedAt:  fetcher.now().UTC(),
	}, nil
}

func validatedOutputPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", inputError("document output path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", inputError("document output path is invalid")
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.IsDir() {
			return "", inputError("document output path is a directory")
		}
		return "", &FetchError{Kind: "exists", Message: "document output already exists"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", &FetchError{Kind: "filesystem", Message: "document output path could not be inspected"}
	}
	info, err := os.Stat(filepath.Dir(absolute))
	if err != nil || !info.IsDir() {
		return "", inputError("document output directory does not exist")
	}
	return absolute, nil
}

func installExclusive(temporaryPath, outputPath string) error {
	source, err := os.Open(temporaryPath)
	if err != nil {
		return &FetchError{Kind: "filesystem", Message: "temporary document could not be reopened"}
	}
	defer source.Close()
	destination, err := os.OpenFile(
		outputPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o644,
	)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return &FetchError{Kind: "exists", Message: "document output already exists"}
		}
		return &FetchError{Kind: "filesystem", Message: "document output could not be created"}
	}
	completed := false
	defer func() {
		destination.Close()
		if !completed {
			os.Remove(outputPath)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return &FetchError{Kind: "filesystem", Message: "document output could not be installed"}
	}
	if err := destination.Sync(); err != nil {
		return &FetchError{Kind: "filesystem", Message: "document output could not be synchronized"}
	}
	if err := destination.Close(); err != nil {
		return &FetchError{Kind: "filesystem", Message: "document output could not be finalized"}
	}
	completed = true
	return nil
}

func validatePublicHTTPSURL(ctx context.Context, parsed *url.URL) error {
	if parsed == nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil {
		return errors.New("only HTTPS document URLs without embedded credentials are allowed")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return errors.New("document URLs may only use HTTPS port 443")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return errors.New("document host could not be resolved")
	}
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return errors.New("document host resolves to a non-public address")
		}
	}
	return nil
}

func publicDialContext(
	ctx context.Context,
	network, address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid document network address")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("document host could not be resolved")
	}
	for _, candidate := range addresses {
		if !publicIP(candidate.IP) {
			return nil, errors.New("document host resolves to a non-public address")
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(
		ctx,
		network,
		net.JoinHostPort(addresses[0].IP.String(), port),
	)
}

func publicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	sharedCarrierNetwork := &net.IPNet{
		IP:   net.IPv4(100, 64, 0, 0),
		Mask: net.CIDRMask(10, 32),
	}
	return !sharedCarrierNetwork.Contains(ip)
}

func inputError(message string) error {
	return &FetchError{Kind: "input", Message: message}
}

func sizeError(maxBytes int64) error {
	return &FetchError{
		Kind:    "limit",
		Message: fmt.Sprintf("document exceeds the %d-byte limit", maxBytes),
	}
}
