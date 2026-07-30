package schemas

import (
	"encoding/json"
	"testing"
)

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
