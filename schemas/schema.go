// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package schemas embeds the public JSON contracts into the native executable.
package schemas

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

//go:embed *.schema.json
var files embed.FS

// Get returns one named public schema.
func Get(target string) (json.RawMessage, error) {
	filename := target + ".schema.json"
	data, err := files.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("unknown schema target %q", target)
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("embedded schema %q is invalid", target)
	}
	return json.RawMessage(data), nil
}

// Bundle returns every public schema for one-process agent discovery.
func Bundle() (contract.SchemaBundle, error) {
	input, err := Get("input")
	if err != nil {
		return contract.SchemaBundle{}, err
	}
	output, err := Get("output")
	if err != nil {
		return contract.SchemaBundle{}, err
	}
	alternatives, err := Get("alternatives")
	if err != nil {
		return contract.SchemaBundle{}, err
	}
	cache, err := Get("cache")
	if err != nil {
		return contract.SchemaBundle{}, err
	}
	providers, err := Get("providers")
	if err != nil {
		return contract.SchemaBundle{}, err
	}
	resolutions, err := Get("resolutions")
	if err != nil {
		return contract.SchemaBundle{}, err
	}
	return contract.SchemaBundle{
		Input:        input,
		Alternatives: alternatives,
		Cache:        cache,
		Output:       output,
		Providers:    providers,
		Resolutions:  resolutions,
	}, nil
}
