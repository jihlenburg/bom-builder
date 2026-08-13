// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package lookupcache persists normalized provider results for deterministic reuse.
package lookupcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

const (
	// SchemaVersion is the current on-disk SQLite schema generation.
	SchemaVersion = 1
	// FormatVersion identifies the cached normalized-result encoding.
	FormatVersion = 1
	// DefaultTTL is the default lifetime of a normalized provider result.
	DefaultTTL = 24 * time.Hour
	// MaxTTL bounds accidental effectively-permanent cache entries.
	MaxTTL = 365 * 24 * time.Hour
)

// Policy controls whether a resolver may read or write the persistent cache.
type Policy string

const (
	PolicyPrefer  Policy = "prefer"
	PolicyRefresh Policy = "refresh"
	PolicyOnly    Policy = "only"
	PolicyOffline Policy = "offline"
	PolicyOff     Policy = "off"
)

// ParsePolicy validates a public cache-policy value.
func ParsePolicy(value string) (Policy, error) {
	switch policy := Policy(strings.ToLower(strings.TrimSpace(value))); policy {
	case PolicyPrefer, PolicyRefresh, PolicyOnly, PolicyOffline, PolicyOff:
		return policy, nil
	default:
		return "", fmt.Errorf(
			"cache policy must be one of prefer, refresh, only, offline, or off",
		)
	}
}

// Config defines one command's persistent-cache behavior.
type Config struct {
	Policy Policy
	Path   string
	TTL    time.Duration
	Now    func() time.Time
}

// Error is a stable cache-layer failure.
type Error struct {
	Kind    string
	Message string
}

func (cacheError *Error) Error() string {
	if cacheError == nil {
		return ""
	}
	return "cache " + cacheError.Kind + ": " + cacheError.Message
}

// DefaultPath returns the platform-native per-user cache database path.
func DefaultPath() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", &Error{Kind: "configuration", Message: "user cache directory is unavailable"}
	}
	return filepath.Join(root, "bom-builder", "lookups-v1.sqlite3"), nil
}

// AdapterVersion invalidates normalized cache entries when adapter semantics change.
func AdapterVersion(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "mouser":
		// v2: search moved from the partnumberandmanufacturer endpoint to
		// the plain partnumber endpoint; the manufacturer filter is local
		// only. v1 entries can carry false not_found for parts whose own
		// catalog manufacturer name that endpoint refused (2026-08-13).
		return "mouser-normalized-v2"
	case "digikey":
		// v2: stock now sourced from ProductDetails variations instead
		// of the pricing endpoint's unpopulated QuantityAvailable —
		// v1 entries carry wrong zero availability (2026-07-30).
		return "digikey-normalized-v2"
	case "ti":
		return "ti-normalized-v1"
	case "microchip":
		return "microchip-normalized-v1"
	case "farnell":
		return "farnell-normalized-v1"
	default:
		return "unknown-normalized-v1"
	}
}

// ProviderContextHash binds cache entries to non-secret pricing context.
func ProviderContextHash(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	values := map[string]string{"provider": provider}
	switch provider {
	case "mouser":
		values["api_url"] = envValue("BOM_BUILDER_MOUSER_API_URL")
	case "digikey":
		values["api_base_url"] = envValue("BOM_BUILDER_DIGIKEY_API_BASE_URL")
		values["account_id_hash"] = hashText(envValue("DIGIKEY_ACCOUNT_ID"))
		values["site"] = envDefault("DIGIKEY_LOCALE_SITE", "DE")
		values["language"] = envDefault("DIGIKEY_LOCALE_LANGUAGE", "en")
		values["currency"] = envDefault("DIGIKEY_LOCALE_CURRENCY", "EUR")
		values["ship_to_country"] = envDefault("DIGIKEY_LOCALE_SHIP_TO_COUNTRY", "de")
	case "ti":
		values["products_url"] = envValue("BOM_BUILDER_TI_PRODUCTS_URL")
		values["currency"] = envDefault("TI_STORE_PRICE_CURRENCY", "USD")
	case "microchip":
		// No currency in this context: the Product API carries no
		// pricing. (Until 2026-08-10 this case wrongly hashed
		// NXP_STORE_CURRENCY — a copy-paste from the removed NXP
		// adapter — so changing that unrelated variable re-keyed
		// microchip cache entries.)
		values["products_url"] = envValue("BOM_BUILDER_MICROCHIP_PRODUCTS_URL")
	case "farnell":
		// The store decides both the catalog view and the implied price
		// currency, so entries from one store must never satisfy a
		// lookup against another.
		values["api_url"] = envValue("BOM_BUILDER_FARNELL_API_URL")
		values["store"] = strings.ToLower(envDefault("FARNELL_STORE_ID", "de.farnell.com"))
		values["currency"] = strings.ToUpper(envValue("FARNELL_PRICE_CURRENCY"))
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, key := range keys {
		canonical.WriteString(key)
		canonical.WriteByte('=')
		canonical.WriteString(strings.TrimSpace(values[key]))
		canonical.WriteByte('\n')
	}
	return hashText(canonical.String())
}

func cacheKey(
	provider, adapterVersion, contextHash string,
	demand procurement.Demand,
) (string, error) {
	request := keyRequest{
		FormatVersion:    FormatVersion,
		Provider:         strings.ToLower(strings.TrimSpace(provider)),
		AdapterVersion:   adapterVersion,
		ContextHash:      contextHash,
		PartNumber:       strings.ToUpper(strings.TrimSpace(demand.PartNumber)),
		Manufacturer:     normalizedWords(demand.Manufacturer),
		RequiredQuantity: demand.RequiredQuantity,
		Package:          strings.ToUpper(strings.TrimSpace(demand.Package)),
		Pins:             demand.Pins,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", &Error{Kind: "internal", Message: "could not encode cache key"}
	}
	return hashText(string(encoded)), nil
}

type keyRequest struct {
	FormatVersion    int    `json:"format_version"`
	Provider         string `json:"provider"`
	AdapterVersion   string `json:"adapter_version"`
	ContextHash      string `json:"context_hash"`
	PartNumber       string `json:"part_number"`
	Manufacturer     string `json:"manufacturer"`
	RequiredQuantity int    `json:"required_quantity"`
	Package          string `json:"package"`
	Pins             int    `json:"pins"`
}

func compactDemand(demand procurement.Demand) procurement.Demand {
	return procurement.Demand{
		PartNumber:       strings.TrimSpace(demand.PartNumber),
		Manufacturer:     strings.TrimSpace(demand.Manufacturer),
		RequiredQuantity: demand.RequiredQuantity,
		Package:          strings.TrimSpace(demand.Package),
		Pins:             demand.Pins,
	}
}

func normalizedWords(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func envValue(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func envDefault(name, fallback string) string {
	if value := envValue(name); value != "" {
		return value
	}
	return fallback
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
