// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package documents discovers and safely fetches component evidence documents.
package documents

import (
	"net/url"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// LinksFromOffer returns the evidence links carried by one normalized offer.
func LinksFromOffer(offer procurement.Offer) []contract.DocumentLink {
	var links []contract.DocumentLink
	if value := normalizedWebURL(offer.DatasheetURL); value != "" {
		links = append(links, contract.DocumentLink{
			Kind:                   "datasheet",
			Provider:               offer.Provider,
			URL:                    value,
			ManufacturerPartNumber: offer.ManufacturerPartNumber,
			Downloadable:           strings.HasPrefix(value, "https://"),
		})
	}
	if value := normalizedWebURL(offer.ProductURL); value != "" {
		links = append(links, contract.DocumentLink{
			Kind:                   "product_page",
			Provider:               offer.Provider,
			URL:                    value,
			ManufacturerPartNumber: offer.ManufacturerPartNumber,
			Downloadable:           false,
		})
	}
	return links
}

// NormalizeLinks deduplicates, orders, and marks the best datasheet candidate.
func NormalizeLinks(links []contract.DocumentLink) []contract.DocumentLink {
	seen := map[string]struct{}{}
	result := make([]contract.DocumentLink, 0, len(links))
	for _, link := range links {
		link.Kind = strings.ToLower(strings.TrimSpace(link.Kind))
		link.Provider = strings.ToLower(strings.TrimSpace(link.Provider))
		link.URL = normalizedWebURL(link.URL)
		if link.URL == "" || link.Provider == "" ||
			link.Kind != "datasheet" && link.Kind != "product_page" {
			continue
		}
		key := link.Kind + "\x00" + link.Provider + "\x00" + link.URL
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		link.Preferred = false
		link.Downloadable = link.Kind == "datasheet" &&
			strings.HasPrefix(link.URL, "https://")
		result = append(result, link)
	}
	sort.SliceStable(result, func(left, right int) bool {
		leftRank := documentRank(result[left])
		rightRank := documentRank(result[right])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if result[left].Provider != result[right].Provider {
			return result[left].Provider < result[right].Provider
		}
		return result[left].URL < result[right].URL
	})
	for index := range result {
		if result[index].Kind == "datasheet" && result[index].Downloadable {
			result[index].Preferred = true
			break
		}
	}
	return result
}

func documentRank(link contract.DocumentLink) int {
	switch {
	case link.Kind == "datasheet" && link.Downloadable &&
		!isDistributorHost(link.URL):
		return 0
	case link.Kind == "datasheet" && link.Downloadable:
		return 1
	case link.Kind == "datasheet":
		return 2
	default:
		return 3
	}
}

func isDistributorHost(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return true
	}
	host := strings.ToLower(parsed.Hostname())
	for _, suffix := range []string{
		"digikey.com",
		"mouser.com",
	} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func normalizedWebURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		parsed.Scheme != "https" && parsed.Scheme != "http" {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}
