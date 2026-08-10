// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package lookupcache

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/sourcing"
)

// Stats is a stable snapshot of cache activity for one command.
type Stats struct {
	Policy               Policy `json:"policy"`
	Hits                 int64  `json:"hits"`
	StaleHits            int64  `json:"stale_hits"`
	Misses               int64  `json:"misses"`
	Refreshes            int64  `json:"refreshes"`
	Writes               int64  `json:"writes"`
	Bypasses             int64  `json:"bypasses"`
	ErrorCount           int64  `json:"error_count"`
	ReusedSourceRequests int64  `json:"reused_source_requests"`
}

type counters struct {
	hits       atomic.Int64
	staleHits  atomic.Int64
	misses     atomic.Int64
	refreshes  atomic.Int64
	writes     atomic.Int64
	bypasses   atomic.Int64
	errorCount atomic.Int64
	reused     atomic.Int64
}

// Session shares one store and activity counter set across provider resolvers.
type Session struct {
	config   Config
	store    *Store
	counters counters
}

// NewSession validates a cache configuration and opens its store when enabled.
func NewSession(configuration Config) (*Session, error) {
	if configuration.Policy == "" {
		configuration.Policy = PolicyPrefer
	}
	if _, err := ParsePolicy(string(configuration.Policy)); err != nil {
		return nil, &Error{Kind: "configuration", Message: err.Error()}
	}
	if configuration.TTL == 0 {
		configuration.TTL = DefaultTTL
	}
	if configuration.TTL < time.Minute || configuration.TTL > MaxTTL {
		return nil, &Error{
			Kind:    "configuration",
			Message: "cache TTL must be between 1m and 8760h",
		}
	}
	if configuration.Now == nil {
		configuration.Now = time.Now
	}
	session := &Session{config: configuration}
	if configuration.Policy == PolicyOff {
		return session, nil
	}
	path := configuration.Path
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
		session.config.Path = path
	}
	store, err := Open(path)
	if err != nil {
		return nil, err
	}
	session.store = store
	return session, nil
}

// Resolver wraps one provider resolver with the session policy.
//
// The returned resolver's Lookup must not be called concurrently for the
// same provider: source-request accounting diffs the shared requestCount
// counter around each network lookup, so overlapping lookups would attribute
// each other's HTTP requests. The CLI's sourcing layer resolves lines
// strictly sequentially, which upholds this contract today.
func (session *Session) Resolver(
	provider string,
	network sourcing.Resolver,
	requestCount func() int,
) (sourcing.Resolver, error) {
	if session == nil {
		return nil, &Error{Kind: "internal", Message: "cache session is nil"}
	}
	if session.config.Policy != PolicyOff && session.store == nil {
		return nil, &Error{Kind: "internal", Message: "cache store is unavailable"}
	}
	if session.config.Policy != PolicyOnly &&
		session.config.Policy != PolicyOffline &&
		network == nil {
		return nil, &Error{Kind: "internal", Message: "network resolver is required"}
	}
	if requestCount == nil {
		requestCount = func() int { return 0 }
	}
	return &resolver{
		session:        session,
		provider:       provider,
		adapterVersion: AdapterVersion(provider),
		contextHash:    ProviderContextHash(provider),
		network:        network,
		requestCount:   requestCount,
	}, nil
}

type resolver struct {
	session        *Session
	provider       string
	adapterVersion string
	contextHash    string
	network        sourcing.Resolver
	requestCount   func() int
}

func (resolver *resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	policy := resolver.session.config.Policy
	if policy == PolicyOff {
		resolver.session.counters.bypasses.Add(1)
		return resolver.network.Lookup(ctx, demand)
	}
	key, err := cacheKey(
		resolver.provider,
		resolver.adapterVersion,
		resolver.contextHash,
		demand,
	)
	if err != nil {
		resolver.session.counters.errorCount.Add(1)
		return procurement.SourcedPart{}, err
	}
	if policy != PolicyRefresh {
		entry, found, readErr := resolver.session.store.Get(
			ctx,
			resolver.provider,
			key,
			resolver.session.now(),
		)
		if readErr != nil {
			resolver.session.counters.errorCount.Add(1)
			return procurement.SourcedPart{}, readErr
		}
		if found && !entry.Stale {
			resolver.session.counters.hits.Add(1)
			resolver.session.counters.reused.Add(int64(entry.SourceRequests))
			entry.Result.Demand = demand
			return entry.Result, nil
		}
		if found && entry.Stale && policy == PolicyOffline {
			resolver.session.counters.staleHits.Add(1)
			resolver.session.counters.reused.Add(int64(entry.SourceRequests))
			entry.Result.Demand = demand
			return entry.Result, nil
		}
		resolver.session.counters.misses.Add(1)
		if policy == PolicyOnly || policy == PolicyOffline {
			return cacheMiss(demand, policy), nil
		}
	}

	resolver.session.counters.refreshes.Add(1)
	before := resolver.requestCount()
	result, lookupErr := resolver.network.Lookup(ctx, demand)
	sourceRequests := resolver.requestCount() - before
	if sourceRequests < 0 {
		sourceRequests = 0
	}
	if lookupErr != nil {
		return result, lookupErr
	}
	if result.Status == "" || result.Status == "provider_error" {
		return result, nil
	}
	now := resolver.session.now()
	if writeErr := resolver.session.store.Put(
		ctx,
		resolver.provider,
		key,
		resolver.contextHash,
		resolver.adapterVersion,
		demand,
		result,
		now,
		now.Add(resolver.session.config.TTL),
		sourceRequests,
	); writeErr != nil {
		resolver.session.counters.errorCount.Add(1)
		return result, nil
	}
	resolver.session.counters.writes.Add(1)
	return result, nil
}

func cacheMiss(demand procurement.Demand, policy Policy) procurement.SourcedPart {
	return procurement.SourcedPart{
		Demand:       demand,
		Status:       "unavailable",
		IssueCode:    "CACHE_MISS",
		IssueMessage: fmt.Sprintf("no usable %s cache entry exists", policy),
	}
}

func (session *Session) now() time.Time {
	return session.config.Now().UTC()
}

// Snapshot returns current aggregate activity counters.
func (session *Session) Snapshot() Stats {
	if session == nil {
		return Stats{Policy: PolicyOff}
	}
	return Stats{
		Policy:               session.config.Policy,
		Hits:                 session.counters.hits.Load(),
		StaleHits:            session.counters.staleHits.Load(),
		Misses:               session.counters.misses.Load(),
		Refreshes:            session.counters.refreshes.Load(),
		Writes:               session.counters.writes.Load(),
		Bypasses:             session.counters.bypasses.Load(),
		ErrorCount:           session.counters.errorCount.Load(),
		ReusedSourceRequests: session.counters.reused.Load(),
	}
}

// Store returns the session's store for command-level inspection.
func (session *Session) Store() *Store {
	if session == nil {
		return nil
	}
	return session.store
}

// Close releases the backing store.
func (session *Session) Close() error {
	if session == nil || session.store == nil {
		return nil
	}
	return session.store.Close()
}
