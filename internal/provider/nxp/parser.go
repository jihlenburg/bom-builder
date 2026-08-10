// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package nxp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	partIDPattern = regexp.MustCompile(`part_id::(?:<b>)?([^<|]+)`)
	stepPattern   = regexp.MustCompile(`^\s*(\d+)\s*::.*::\s*([0-9][0-9,]*(?:\.[0-9]+)?)\s*$`)
	moqPattern    = regexp.MustCompile(`(?i)min\.\s*order quantity:\s*([0-9,]+)`)
	mpqPattern    = regexp.MustCompile(`(?i)min\.\s*package quantity:\s*([0-9,]+)`)
)

type rawSearchPayload struct {
	Results *[]rawSearchRow `json:"results"`
}

type rawSearchRow struct {
	Summary  string         `json:"summary"`
	MetaData map[string]any `json:"metaData"`
	URL      string         `json:"url"`
}

func selectBestResult(
	query string,
	data []byte,
	configuredCurrency string,
) (*SearchResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload rawSearchPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("NXP store payload was not valid JSON")
	}
	if payload.Results == nil {
		return nil, errors.New("NXP store payload omitted the results list")
	}
	type ranked struct {
		rank   [4]int
		result SearchResult
	}
	var best *ranked
	parsedAny := false
	for _, raw := range *payload.Results {
		result, ok := searchResultFromRow(query, raw, configuredCurrency)
		if !ok {
			continue
		}
		parsedAny = true
		score := candidateScore(query, result.PartID)
		if score == 0 {
			continue
		}
		rank := [4]int{
			score,
			boolRank(result.BuyDirect),
			boolRank(len(result.StepPrices) > 0),
			integerValue(result.StockQuantity),
		}
		if best == nil || compareRank(rank, best.rank) > 0 {
			best = &ranked{rank: rank, result: result}
		}
	}
	if len(*payload.Results) > 0 && !parsedAny {
		return nil, errors.New("NXP store results omitted stable part identifiers")
	}
	if best == nil {
		return nil, nil
	}
	return &best.result, nil
}

func searchResultFromRow(
	query string,
	raw rawSearchRow,
	configuredCurrency string,
) (SearchResult, bool) {
	partID := ""
	if match := partIDPattern.FindStringSubmatch(raw.Summary); len(match) == 2 {
		partID = strings.TrimSpace(match[1])
	}
	if partID == "" {
		partID = metadataString(raw.MetaData, "part_id")
	}
	if partID == "" {
		return SearchResult{}, false
	}
	actions := metadataStrings(raw.MetaData, "Order")
	buyDirect := false
	for _, action := range actions {
		if strings.EqualFold(strings.TrimSpace(action), "Buy Direct") {
			buyDirect = true
		}
	}
	unitPrice := metadataNumber(raw.MetaData, "unitPrice")
	stepPrices := metadataStepPrices(raw.MetaData, "stepPrice")
	currency := firstNonEmpty(
		metadataString(raw.MetaData, "currency"),
		metadataString(raw.MetaData, "Currency"),
		metadataString(raw.MetaData, "currencyCode"),
	)
	if currency == "" && (unitPrice != "" || len(stepPrices) > 0) {
		currency = configuredCurrency
	}
	return SearchResult{
		Query:              query,
		PartID:             partID,
		Description:        metadataString(raw.MetaData, "Description"),
		BuyDirect:          buyDirect,
		OrderActions:       actions,
		UnitPrice:          unitPrice,
		Currency:           strings.ToUpper(strings.TrimSpace(currency)),
		StockQuantity:      metadataIntPointer(raw.MetaData, "stock_quantity"),
		Availability:       metadataString(raw.MetaData, "Availability"),
		PackingName:        metadataString(raw.MetaData, "packing_name"),
		PackingDescription: metadataString(raw.MetaData, "packing_desc"),
		StepPrices:         stepPrices,
		PackageQualityURL:  metadataString(raw.MetaData, "packageQualityUrl"),
		ProductURL:         absoluteNXPURL(strings.TrimSpace(raw.URL)),
	}, true
}

func parsePartDetail(
	query, matchedPartID, body string,
) *PartDetail {
	if strings.Contains(body, "HTTP Status 400") {
		return nil
	}
	rawLines := strings.Split(body, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	target := normalizePartNumber(matchedPartID)
	var candidateIndexes []int
	for index, line := range lines {
		if normalizePartNumber(line) == target {
			candidateIndexes = append(candidateIndexes, index)
		}
	}
	if len(candidateIndexes) == 0 {
		for index, line := range lines {
			if partNumbersRelated(line, matchedPartID) {
				candidateIndexes = append(candidateIndexes, index)
			}
		}
	}
	for _, index := range candidateIndexes {
		if !partNumbersRelated(lines[index], matchedPartID) {
			continue
		}
		end := min(index+18, len(lines))
		window := strings.Join(lines[index:end], "\n")
		return &PartDetail{
			Query:                  query,
			MatchedPartID:          matchedPartID,
			MinimumOrderQuantity:   quantityMatch(moqPattern, window),
			MinimumPackageQuantity: quantityMatch(mpqPattern, window),
		}
	}
	return nil
}

func metadataString(metadata map[string]any, key string) string {
	value, exists := metadata[key]
	if !exists || value == nil {
		return ""
	}
	return strings.TrimSpace(toString(value))
}

func metadataStrings(metadata map[string]any, key string) []string {
	value, exists := metadata[key]
	if !exists {
		return nil
	}
	raw, ok := value.([]any)
	if !ok {
		if one := strings.TrimSpace(toString(value)); one != "" {
			return []string{one}
		}
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := strings.TrimSpace(toString(item)); text != "" {
			values = append(values, text)
		}
	}
	return values
}

func metadataNumber(metadata map[string]any, key string) json.Number {
	value, exists := metadata[key]
	if !exists || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case json.Number:
		return typed
	case string:
		// The raw text is preserved verbatim: money.Parse handles the
		// locale work, and pre-stripping commas here would turn an EU
		// "1.234,56" into "1.23456" — a silent 1000x price error.
		// Integer consumers strip their own grouping separators.
		return json.Number(strings.TrimSpace(typed))
	default:
		return json.Number(toString(typed))
	}
}

func metadataIntPointer(metadata map[string]any, key string) *int {
	number := metadataNumber(metadata, key)
	if number == "" {
		return nil
	}
	// Integer counts such as stock_quantity may arrive US-grouped
	// ("4,310"); commas are unambiguous grouping separators for integers,
	// unlike in price text where they may mark EU decimals.
	value, err := strconv.ParseInt(strings.ReplaceAll(number.String(), ",", ""), 10, 32)
	if err != nil || value < 0 {
		return nil
	}
	converted := int(value)
	return &converted
}

func metadataStepPrices(metadata map[string]any, key string) []StepPrice {
	raw, ok := metadata[key].([]any)
	if !ok {
		return nil
	}
	prices := make([]StepPrice, 0, len(raw))
	for _, entry := range raw {
		match := stepPattern.FindStringSubmatch(toString(entry))
		if len(match) != 3 {
			continue
		}
		quantity, err := strconv.Atoi(match[1])
		if err != nil || quantity < 1 {
			continue
		}
		prices = append(prices, StepPrice{
			Quantity: quantity,
			// Stripping commas is safe here only because stepPattern
			// admits US grouping alone ([0-9][0-9,]*(\.[0-9]+)?): a
			// comma can never be the decimal separator in match[2].
			Price: json.Number(strings.ReplaceAll(match[2], ",", "")),
		})
	}
	sort.SliceStable(prices, func(left, right int) bool {
		return prices[left].Quantity < prices[right].Quantity
	})
	for index := 1; index < len(prices); index++ {
		if prices[index-1].Quantity == prices[index].Quantity {
			return nil
		}
	}
	return prices
}

func candidateScore(query, partID string) int {
	left, right := normalizePartNumber(query), normalizePartNumber(partID)
	switch {
	case left == "" || right == "":
		return 0
	case left == right:
		return 100
	case strings.HasPrefix(right, left):
		return 90
	case strings.HasPrefix(left, right):
		return 80
	case strings.Contains(right, left), strings.Contains(left, right):
		return 70
	default:
		return 0
	}
}

func normalizePartNumber(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToUpper(value) {
		if character >= 'A' && character <= 'Z' || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func partNumbersRelated(left, right string) bool {
	normalizedLeft := normalizePartNumber(left)
	normalizedRight := normalizePartNumber(right)
	return normalizedLeft != "" && normalizedRight != "" &&
		(normalizedLeft == normalizedRight ||
			strings.Contains(normalizedLeft, normalizedRight) ||
			strings.Contains(normalizedRight, normalizedLeft))
}

func absoluteNXPURL(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		if parsed.Scheme == "https" && strings.HasSuffix(strings.ToLower(parsed.Hostname()), "nxp.com") {
			return parsed.String()
		}
		return ""
	}
	base, _ := url.Parse("https://www.nxp.com")
	return base.ResolveReference(parsed).String()
}

func quantityMatch(pattern *regexp.Regexp, text string) *int {
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return nil
	}
	value, err := strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
	if err != nil || value < 1 {
		return nil
	}
	return &value
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func compareRank(left, right [4]int) int {
	for index := range left {
		switch {
		case left[index] < right[index]:
			return -1
		case left[index] > right[index]:
			return 1
		}
	}
	return 0
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}

func integerValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}
