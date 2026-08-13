// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package schemas

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/provider"
)

func TestSchemaProviderEnumsCoverEveryRegisteredAdapter(t *testing.T) {
	t.Parallel()
	// Every provider with a pricing runtime can appear as a provider
	// name in output, cache, and resolutions documents: in run metadata,
	// on an offer, on a document link, as the selected provider, and in
	// a stored approval. An enum that omits one publishes a contract the
	// tool itself violates, which is how `--providers microchip` came to
	// fail its own schema after the adapter shipped.
	//
	// Extra names are allowed and deliberate: a removed adapter stays
	// listed so durable stored data keeps decoding (see the nxp note in
	// cache.schema.json).
	required := provider.PricingProviderNames()
	if len(required) < 2 {
		t.Fatalf("implausible pricing-provider registry: %v", required)
	}
	for _, target := range []string{"output", "cache", "resolutions"} {
		document, err := Get(target)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(document, &schema); err != nil {
			t.Fatal(err)
		}
		enums := 0
		var walk func(path string, node any)
		walk = func(path string, node any) {
			switch typed := node.(type) {
			case map[string]any:
				if names, isProviderEnum := providerEnum(typed); isProviderEnum {
					enums++
					for _, name := range required {
						if !names[name] {
							t.Errorf("%s%s enum is missing %q", target, path, name)
						}
					}
				}
				for key, child := range typed {
					walk(path+"/"+key, child)
				}
			case []any:
				for index, child := range typed {
					walk(fmt.Sprintf("%s[%d]", path, index), child)
				}
			}
		}
		walk("", schema)
		if enums == 0 {
			t.Fatalf("%s schema contains no provider-name enum", target)
		}
	}
}

// providerEnum recognizes a provider-name enum by its anchor member.
// Every provider list in the published contracts has named mouser since
// the first version, which distinguishes them from unrelated string
// enums such as status or kind.
func providerEnum(node map[string]any) (map[string]bool, bool) {
	entries, ok := node["enum"].([]any)
	if !ok {
		return nil, false
	}
	names := map[string]bool{}
	for _, entry := range entries {
		name, isString := entry.(string)
		if !isString {
			return nil, false
		}
		names[name] = true
	}
	return names, names["mouser"]
}

func TestEmbeddedSchemasAreValid(t *testing.T) {
	for _, target := range []string{"input", "alternatives", "cache", "output", "providers"} {
		document, err := Get(target)
		if err != nil {
			t.Fatalf("Get(%q) error = %v", target, err)
		}
		if !json.Valid(document) {
			t.Fatalf("Get(%q) returned invalid JSON", target)
		}
	}
}

func TestBundleContainsAllContracts(t *testing.T) {
	bundle, err := Bundle()
	if err != nil {
		t.Fatalf("Bundle() error = %v", err)
	}
	if len(bundle.Input) == 0 ||
		len(bundle.Alternatives) == 0 ||
		len(bundle.Cache) == 0 ||
		len(bundle.Output) == 0 ||
		len(bundle.Providers) == 0 {
		t.Fatal("Bundle() omitted a schema")
	}
}

func TestOutputSchemaDescribesExportArtifact(t *testing.T) {
	t.Parallel()
	document, err := Get("output")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["$defs"].(map[string]any)
	exportArtifact, ok := definitions["exportArtifact"].(map[string]any)
	if !ok {
		t.Fatal("output schema is missing $defs/exportArtifact")
	}

	// The def must describe exactly the wire fields contract.ExportArtifact
	// emits; export output failing its own published schema is contract
	// drift.
	expected := map[string]bool{}
	artifactType := reflect.TypeOf(contract.ExportArtifact{})
	for index := range artifactType.NumField() {
		tag := strings.Split(artifactType.Field(index).Tag.Get("json"), ",")[0]
		expected[tag] = true
	}
	required := map[string]bool{}
	for _, name := range exportArtifact["required"].([]any) {
		required[name.(string)] = true
	}
	properties, _ := exportArtifact["properties"].(map[string]any)
	for tag := range expected {
		if !required[tag] {
			t.Errorf("exportArtifact.required is missing %q", tag)
		}
		if _, described := properties[tag]; !described {
			t.Errorf("exportArtifact.properties is missing %q", tag)
		}
	}
	for name := range required {
		if !expected[name] {
			t.Errorf("exportArtifact.required lists %q, which ExportArtifact never emits", name)
		}
	}

	// The envelope's artifact slot is shared by documents fetch and export;
	// it must admit both artifact shapes.
	artifact := schema["properties"].(map[string]any)["artifact"].(map[string]any)
	branches, ok := artifact["oneOf"].([]any)
	if !ok {
		t.Fatalf("envelope artifact must be a oneOf of artifact kinds, got %#v", artifact)
	}
	references := map[string]bool{}
	for _, branch := range branches {
		if ref, found := branch.(map[string]any)["$ref"].(string); found {
			references[ref] = true
		}
	}
	for _, want := range []string{"#/$defs/documentArtifact", "#/$defs/exportArtifact"} {
		if !references[want] {
			t.Errorf("envelope artifact oneOf is missing %s", want)
		}
	}
}

func TestSchemasDescribeEveryRequiredField(t *testing.T) {
	t.Parallel()
	// A schema object that lists a field in "required" but omits it from
	// "properties" demands data it never describes; consumers reading the
	// contract get no type information for a mandatory field.
	var walk func(t *testing.T, path string, node any)
	walk = func(t *testing.T, path string, node any) {
		switch typed := node.(type) {
		case map[string]any:
			properties, hasProperties := typed["properties"].(map[string]any)
			required, hasRequired := typed["required"].([]any)
			if hasProperties && hasRequired {
				for _, entry := range required {
					name := entry.(string)
					if _, described := properties[name]; !described {
						t.Errorf("%s requires %q but does not describe it", path, name)
					}
				}
			}
			for key, child := range typed {
				walk(t, path+"/"+key, child)
			}
		case []any:
			for index, child := range typed {
				walk(t, fmt.Sprintf("%s[%d]", path, index), child)
			}
		}
	}
	for _, target := range []string{"input", "alternatives", "cache", "output", "providers"} {
		document, err := Get(target)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(document, &schema); err != nil {
			t.Fatal(err)
		}
		walk(t, target, schema)
	}
}

func TestInputSchemaDescribesAllAcceptedShapes(t *testing.T) {
	t.Parallel()
	// The design loader accepts a single design object, a top-level array
	// of designs, and a {"designs": [...]} wrapper; the published schema
	// must describe all three or agents driving the CLI from `schema
	// input` will reject documents the CLI accepts.
	document, err := Get("input")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	definitions, _ := schema["$defs"].(map[string]any)
	design, ok := definitions["design"].(map[string]any)
	if !ok {
		t.Fatal("input schema is missing $defs/design")
	}
	requiredNames := map[string]bool{}
	for _, name := range design["required"].([]any) {
		requiredNames[name.(string)] = true
	}
	if !requiredNames["design"] || !requiredNames["parts"] {
		t.Fatalf("design def must require design and parts, got %v", design["required"])
	}

	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) != 3 {
		t.Fatalf("input schema must offer the three accepted document shapes, got %#v", schema["oneOf"])
	}
	var hasSingle, hasArray, hasWrapper bool
	for _, branch := range branches {
		typed := branch.(map[string]any)
		switch {
		case typed["$ref"] == "#/$defs/design":
			hasSingle = true
		case typed["type"] == "array":
			hasArray = true
		default:
			if properties, found := typed["properties"].(map[string]any); found {
				_, hasWrapper = properties["designs"]
			}
		}
	}
	if !hasSingle || !hasArray || !hasWrapper {
		t.Fatalf("input schema shapes incomplete: single=%v array=%v wrapper=%v", hasSingle, hasArray, hasWrapper)
	}
}

func TestOutputSchemaDeclaresExactMoneyStrings(t *testing.T) {
	t.Parallel()
	document, err := Get("output")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["$defs"].(map[string]any)
	decimal := definitions["decimal"].(map[string]any)
	if decimal["type"] != "string" || decimal["pattern"] != `^[0-9]+\.[0-9]{6}$` {
		t.Fatalf("unexpected decimal schema: %#v", decimal)
	}
}
