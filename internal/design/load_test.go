package design

import (
	"strings"
	"testing"
)

const validDesign = `{
  "design": "Demo",
  "parts": [{
    "part_number": "RC0402FR-0710KL",
    "manufacturer": "Yageo",
    "quantity": 2
  }]
}`

func TestLoadSourcesAcceptsStrictStdinDesign(t *testing.T) {
	designs, err := LoadSources([]string{"-"}, strings.NewReader(validDesign))
	if err != nil {
		t.Fatalf("LoadSources() error = %v", err)
	}
	if len(designs) != 1 || designs[0].Design != "Demo" {
		t.Fatalf("unexpected designs: %#v", designs)
	}
	if got := designs[0].Parts[0].Quantity; got != 2 {
		t.Fatalf("quantity = %d", got)
	}
}

func TestLoadSourcesAcceptsWrappedDesigns(t *testing.T) {
	payload := `{"designs":[` + validDesign + `]}`
	designs, err := LoadSources([]string{"-"}, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("LoadSources() error = %v", err)
	}
	if len(designs) != 1 {
		t.Fatalf("design count = %d", len(designs))
	}
}

func TestLoadSourcesRejectsUnknownFields(t *testing.T) {
	payload := strings.Replace(validDesign, `"quantity": 2`, `"quantity": 2, "mystery": true`, 1)
	if _, err := LoadSources([]string{"-"}, strings.NewReader(payload)); err == nil {
		t.Fatal("LoadSources() accepted an unknown field")
	}
}

func TestLoadSourcesRejectsEmptyIdentifiers(t *testing.T) {
	payload := strings.Replace(validDesign, `"Yageo"`, `"   "`, 1)
	if _, err := LoadSources([]string{"-"}, strings.NewReader(payload)); err == nil {
		t.Fatal("LoadSources() accepted an empty manufacturer")
	}
}

func TestLoadSourcesRejectsRepeatedStdin(t *testing.T) {
	if _, err := LoadSources([]string{"-", "-"}, strings.NewReader(validDesign)); err == nil {
		t.Fatal("LoadSources() accepted stdin twice")
	}
}
