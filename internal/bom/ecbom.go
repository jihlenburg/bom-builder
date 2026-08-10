// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package bom

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

// EurocircuitsHeader is the eC-BOM column set the Eurocircuits assembly
// upload expects, in order.
var EurocircuitsHeader = []string{
	"Item", "Quantity", "Designators", "Manufacturer", "MPN",
	"Description", "Value", "Package", "Mounted", "Comment",
}

// EurocircuitsCSV renders one design as a semicolon-separated,
// CRLF-terminated eC-BOM ready for the Eurocircuits assembly upload.
// Rows keep the authored part order; Item numbers start at 1. Parts
// without an explicit mounted flag are treated as mounted.
//
// Authored text that a spreadsheet would evaluate as a formula is
// neutralized with a leading apostrophe, and every neutralized cell is
// reported as an explicit warning so the fidelity change is visible
// instead of silent.
func EurocircuitsCSV(design contract.Design) ([]byte, []contract.Issue, error) {
	var buffer bytes.Buffer
	warnings := []contract.Issue{}
	writer := csv.NewWriter(&buffer)
	writer.Comma = ';'
	writer.UseCRLF = true
	if err := writer.Write(EurocircuitsHeader); err != nil {
		return nil, nil, fmt.Errorf("write header: %w", err)
	}
	for index, part := range design.Parts {
		mounted := "Yes"
		if part.Mounted != nil && !*part.Mounted {
			mounted = "No"
		}
		row := []string{
			strconv.Itoa(index + 1),
			strconv.Itoa(part.Quantity),
			strings.Join(part.Designators, ","),
			part.Manufacturer,
			part.PartNumber,
			stringOrEmpty(part.Description),
			stringOrEmpty(part.Value),
			stringOrEmpty(part.Package),
			mounted,
			stringOrEmpty(part.Comment),
		}
		// Columns 2.. carry authored text; Item and Quantity are
		// generated integers and never need the guard.
		for column := 2; column < len(row); column++ {
			guarded, changed := guardFormulaCell(row[column])
			if !changed {
				continue
			}
			row[column] = guarded
			warnings = append(warnings, contract.Issue{
				Code: "CSV_FORMULA_CONTENT_ESCAPED",
				Message: fmt.Sprintf(
					"row %d column %s starts with formula syntax and was prefixed with an apostrophe",
					index+1,
					EurocircuitsHeader[column],
				),
			})
		}
		if err := writer.Write(row); err != nil {
			return nil, nil, fmt.Errorf("write row %d: %w", index+1, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, nil, fmt.Errorf("render eC-BOM: %w", err)
	}
	return buffer.Bytes(), warnings, nil
}

// guardFormulaCell neutralizes spreadsheet formula injection. Cells
// beginning with '=', '@', tab, or carriage return are always escaped;
// cells beginning with '+' or '-' are escaped unless the whole cell is a
// plain decimal number, so ordinary values like "-40" survive verbatim
// while DDE payloads ("-cmd|…", "+cmd|…") do not.
func guardFormulaCell(value string) (string, bool) {
	if value == "" {
		return value, false
	}
	switch value[0] {
	case '=', '@', '\t', '\r':
		return "'" + value, true
	case '+', '-':
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "'" + value, true
		}
	}
	return value, false
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
