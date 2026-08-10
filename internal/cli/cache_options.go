// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/lookupcache"
)

func consumeCacheFlags(args []string) ([]string, lookupcache.Config, error) {
	var err error
	args, policyText, hasPolicy, err := consumeValueFlag(args, "--cache-policy")
	if err != nil {
		return nil, lookupcache.Config{}, err
	}
	args, databasePath, hasDatabasePath, err := consumeValueFlag(args, "--cache-db")
	if err != nil {
		return nil, lookupcache.Config{}, err
	}
	args, ttlText, hasTTL, err := consumeValueFlag(args, "--cache-ttl")
	if err != nil {
		return nil, lookupcache.Config{}, err
	}
	if !hasPolicy {
		policyText = strings.TrimSpace(os.Getenv("BOM_BUILDER_CACHE_POLICY"))
		if policyText == "" {
			policyText = string(lookupcache.PolicyPrefer)
		}
	}
	policy, err := lookupcache.ParsePolicy(policyText)
	if err != nil {
		return nil, lookupcache.Config{}, err
	}
	if !hasDatabasePath {
		databasePath = strings.TrimSpace(os.Getenv("BOM_BUILDER_CACHE_DB"))
	}
	ttl := lookupcache.DefaultTTL
	if !hasTTL {
		ttlText = strings.TrimSpace(os.Getenv("BOM_BUILDER_CACHE_TTL"))
	}
	if ttlText != "" {
		ttl, err = time.ParseDuration(ttlText)
		if err != nil {
			return nil, lookupcache.Config{}, fmt.Errorf(
				"cache TTL must be a Go duration such as 30m or 24h",
			)
		}
	}
	if ttl < time.Minute || ttl > lookupcache.MaxTTL {
		return nil, lookupcache.Config{}, fmt.Errorf(
			"cache TTL must be between 1m and 8760h",
		)
	}
	return args, lookupcache.Config{
		Policy: policy,
		Path:   strings.TrimSpace(databasePath),
		TTL:    ttl,
	}, nil
}

func cacheRunMetadata(session *lookupcache.Session) *contract.CacheRunMetadata {
	stats := session.Snapshot()
	return &contract.CacheRunMetadata{
		Policy:               string(stats.Policy),
		Hits:                 stats.Hits,
		StaleHits:            stats.StaleHits,
		Misses:               stats.Misses,
		Refreshes:            stats.Refreshes,
		Writes:               stats.Writes,
		Bypasses:             stats.Bypasses,
		ErrorCount:           stats.ErrorCount,
		ReusedSourceRequests: stats.ReusedSourceRequests,
	}
}
