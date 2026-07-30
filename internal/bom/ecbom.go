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
func EurocircuitsCSV(design contract.Design) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	writer.Comma = ';'
	writer.UseCRLF = true
	if err := writer.Write(EurocircuitsHeader); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
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
		if err := writer.Write(row); err != nil {
			return nil, fmt.Errorf("write row %d: %w", index+1, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("render eC-BOM: %w", err)
	}
	return buffer.Bytes(), nil
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
