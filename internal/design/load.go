// Package design strictly loads and validates authored BOM documents.
package design

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

const maxDocumentBytes = 16 * 1024 * 1024

// LoadSources loads design files and at most one stdin marker.
func LoadSources(sources []string, stdin io.Reader) ([]contract.Design, error) {
	var designs []contract.Design
	stdinSeen := false
	for _, source := range sources {
		var (
			reader io.Reader
			close  func() error
		)
		if source == "-" {
			if stdinSeen {
				return nil, errors.New("stdin marker '-' may only be used once")
			}
			stdinSeen = true
			reader = stdin
			close = func() error { return nil }
		} else {
			file, err := os.Open(source)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", source, err)
			}
			reader = file
			close = file.Close
		}
		loaded, err := decodeDocument(reader)
		closeErr := close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%s: %w", source, closeErr)
		}
		designs = append(designs, loaded...)
	}
	if len(designs) == 0 {
		return nil, errors.New("at least one design is required")
	}
	for index := range designs {
		if err := validateDesign(&designs[index]); err != nil {
			return nil, fmt.Errorf("design %d: %w", index+1, err)
		}
	}
	return designs, nil
}

func decodeDocument(reader io.Reader) ([]contract.Design, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read JSON: %w", err)
	}
	if len(data) > maxDocumentBytes {
		return nil, fmt.Errorf("JSON exceeds %d-byte limit", maxDocumentBytes)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("JSON document is empty")
	}

	if trimmed[0] == '[' {
		var designs []contract.Design
		if err := decodeStrict(trimmed, &designs); err != nil {
			return nil, err
		}
		return designs, nil
	}

	var shape map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &shape); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if _, wrapped := shape["designs"]; wrapped {
		var wrapper struct {
			Designs []contract.Design `json:"designs"`
		}
		if err := decodeStrict(trimmed, &wrapper); err != nil {
			return nil, err
		}
		return wrapper.Designs, nil
	}

	var single contract.Design
	if err := decodeStrict(trimmed, &single); err != nil {
		return nil, err
	}
	return []contract.Design{single}, nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode JSON: trailing JSON value")
		}
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

func validateDesign(design *contract.Design) error {
	design.Design = strings.TrimSpace(design.Design)
	if design.Design == "" {
		return errors.New("design name is required")
	}
	if len(design.Parts) == 0 {
		return errors.New("parts must contain at least one item")
	}
	for index := range design.Parts {
		part := &design.Parts[index]
		part.PartNumber = strings.TrimSpace(part.PartNumber)
		part.Manufacturer = strings.TrimSpace(part.Manufacturer)
		switch {
		case part.PartNumber == "":
			return fmt.Errorf("parts[%d].part_number is required", index)
		case part.Manufacturer == "":
			return fmt.Errorf("parts[%d].manufacturer is required", index)
		case part.Quantity < 1:
			return fmt.Errorf("parts[%d].quantity must be >= 1", index)
		case part.Pins != nil && *part.Pins < 1:
			return fmt.Errorf("parts[%d].pins must be >= 1", index)
		}
		for position := range part.Designators {
			part.Designators[position] = strings.TrimSpace(part.Designators[position])
			if part.Designators[position] == "" {
				return fmt.Errorf("parts[%d].designators[%d] must not be empty", index, position)
			}
		}
	}
	return nil
}
