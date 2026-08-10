// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func fakeOffers() []procurement.Offer {
	stock := 2940
	price, _ := money.Parse("0.48")
	return []procurement.Offer{
		{
			Provider:               "mouser",
			DistributorPartNumber:  "595-TMP421AQDCNRQ1",
			ManufacturerPartNumber: "TMP421AQDCNRQ1",
			Manufacturer:           "Texas Instruments",
			MatchMethod:            "exact",
			AvailableQuantity:      &stock,
			SelectedPlan: &procurement.PurchasePlan{
				RequiredQuantity:  10,
				PurchasedQuantity: 10,
				UnitPrice:         price,
				Currency:          "EUR",
				StockVerified:     true,
			},
		},
		{
			Provider:               "digikey",
			DistributorPartNumber:  "296-TMP421-ND",
			ManufacturerPartNumber: "TMP421ADGKR",
			Manufacturer:           "Texas Instruments",
			MatchMethod:            "prefix",
			ReviewRequired:         true,
		},
	}
}

func fakeLookupRunner(t *testing.T, calls *[]procurement.Demand) LookupRunner {
	t.Helper()
	return func(
		_ context.Context,
		demand procurement.Demand,
		providers string,
	) (procurement.SourcedPart, error) {
		if providers != "auto" {
			t.Errorf("expected the default provider selection, got %q", providers)
		}
		*calls = append(*calls, demand)
		return procurement.SourcedPart{
			Demand: demand,
			Status: "priced",
			Offers: fakeOffers(),
		}, nil
	}
}

// runLookup drives the form and executes the returned async command.
func runLookup(t *testing.T, interactive model) model {
	t.Helper()
	interactive = press(t, interactive, rune_('l'))
	if interactive.state != stateLookupForm {
		t.Fatalf("expected the lookup form, state = %d", interactive.state)
	}
	interactive = typeText(t, interactive, "TMP421-Q1")
	interactive = press(t, interactive, key(tea.KeyTab))
	interactive = typeText(t, interactive, "Texas Instruments")
	updated, command := interactive.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	interactive = updated.(model)
	if interactive.state != stateLookupWait {
		t.Fatalf("expected the wait screen, state = %d (%s)", interactive.state, interactive.message)
	}
	if command == nil {
		t.Fatal("expected an asynchronous lookup command")
	}
	result, _ := interactive.Update(command())
	return result.(model)
}

func TestResolverFlowRecordsChosenCandidate(t *testing.T) {
	calls := []procurement.Demand{}
	interactive, store := openTestModelWithLookup(t, fakeLookupRunner(t, &calls))
	interactive = runLookup(t, interactive)

	if interactive.state != stateCandidates {
		t.Fatalf("expected the candidates view, state = %d (%s)", interactive.state, interactive.message)
	}
	if len(calls) != 1 || calls[0].PartNumber != "TMP421-Q1" ||
		calls[0].RequiredQuantity != 1 {
		t.Fatalf("unexpected lookup demand: %+v", calls)
	}
	view := interactive.View()
	for _, expected := range []string{"mouser", "digikey", "safe", "review", "0.480000 EUR"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("candidates view missing %q:\n%s", expected, view)
		}
	}

	// Choose the first (safe) candidate; the approve form must be
	// prefilled with the lookup demand and the offer identity.
	interactive = press(t, interactive, key(tea.KeyEnter))
	if interactive.state != stateApprove {
		t.Fatalf("expected the approve form, state = %d", interactive.state)
	}
	if value := interactive.approveInputs[fieldPartNumber].Value(); value != "TMP421-Q1" {
		t.Fatalf("original part not prefilled: %q", value)
	}
	if value := interactive.approveInputs[fieldReplacementPartNumber].Value(); value != "TMP421AQDCNRQ1" {
		t.Fatalf("replacement part not prefilled: %q", value)
	}
	if value := interactive.approveInputs[fieldProviderSKU].Value(); value != "595-TMP421AQDCNRQ1" {
		t.Fatalf("provider SKU not prefilled: %q", value)
	}
	note := interactive.approveInputs[fieldNote].Value()
	if !strings.Contains(note, "mouser") || !strings.Contains(note, "stock 2940") {
		t.Fatalf("evidence note not prefilled: %q", note)
	}
	if interactive.focusIndex != fieldApprovedBy {
		t.Fatalf("focus must land on approved_by, got field %d", interactive.focusIndex)
	}

	// The approver still has to identify themselves before submitting.
	interactive = typeText(t, interactive, "J. Ihlenburg")
	interactive = press(t, interactive, key(tea.KeyCtrlS))
	if interactive.state != stateList || interactive.isError {
		t.Fatalf("expected a recorded resolution, state=%d message=%q",
			interactive.state, interactive.message)
	}
	records, err := store.List(context.Background(), "", "", 100, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 1 ||
		records[0].Replacement.ProviderSKU != "595-TMP421AQDCNRQ1" ||
		records[0].ApprovedBy != "J. Ihlenburg" {
		t.Fatalf("stored resolution does not match the chosen candidate: %+v", records)
	}
}

func TestResolverPrefillNeverPrefillsApprover(t *testing.T) {
	calls := []procurement.Demand{}
	interactive, store := openTestModelWithLookup(t, fakeLookupRunner(t, &calls))
	interactive = runLookup(t, interactive)
	interactive = press(t, interactive, key(tea.KeyEnter))
	if value := interactive.approveInputs[fieldApprovedBy].Value(); value != "" {
		t.Fatalf("approved_by must never be prefilled, got %q", value)
	}
	// Submitting without an approver must fail and store nothing.
	interactive = press(t, interactive, key(tea.KeyCtrlS))
	if interactive.state != stateApprove || !interactive.isError {
		t.Fatalf("expected a visible validation failure, state=%d", interactive.state)
	}
	records, err := store.List(context.Background(), "", "", 100, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("nothing may be stored without an approver: %+v", records)
	}
}

func TestResolverLookupErrorReturnsToFormVisibly(t *testing.T) {
	runner := func(
		context.Context,
		procurement.Demand,
		string,
	) (procurement.SourcedPart, error) {
		return procurement.SourcedPart{}, errors.New("mouser: missing API key")
	}
	interactive, _ := openTestModelWithLookup(t, runner)
	interactive = press(t, interactive, rune_('l'))
	interactive = typeText(t, interactive, "TMP421-Q1")
	interactive = press(t, interactive, key(tea.KeyTab))
	interactive = typeText(t, interactive, "Texas Instruments")
	updated, command := interactive.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	interactive = updated.(model)
	result, _ := interactive.Update(command())
	interactive = result.(model)
	if interactive.state != stateLookupForm || !interactive.isError ||
		!strings.Contains(interactive.message, "missing API key") {
		t.Fatalf("expected a visible failure on the form, state=%d message=%q",
			interactive.state, interactive.message)
	}
}

func TestResolverAbandonedLookupIgnoresLateResult(t *testing.T) {
	calls := []procurement.Demand{}
	interactive, _ := openTestModelWithLookup(t, fakeLookupRunner(t, &calls))
	interactive = press(t, interactive, rune_('l'))
	interactive = typeText(t, interactive, "TMP421-Q1")
	interactive = press(t, interactive, key(tea.KeyTab))
	interactive = typeText(t, interactive, "Texas Instruments")
	updated, command := interactive.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	interactive = updated.(model)
	// Abandon before the result arrives, then deliver the late result.
	interactive = press(t, interactive, key(tea.KeyEsc))
	if interactive.state != stateLookupForm {
		t.Fatalf("expected esc to abandon the lookup, state = %d", interactive.state)
	}
	result, _ := interactive.Update(command())
	interactive = result.(model)
	if interactive.state != stateLookupForm {
		t.Fatalf("a late result must not change the abandoned state, state = %d",
			interactive.state)
	}
}

func TestResolverLookupFormValidatesInput(t *testing.T) {
	calls := []procurement.Demand{}
	interactive, _ := openTestModelWithLookup(t, fakeLookupRunner(t, &calls))
	interactive = press(t, interactive, rune_('l'), key(tea.KeyCtrlS))
	if interactive.state != stateLookupForm || !interactive.isError {
		t.Fatalf("expected an empty form to be rejected, state=%d message=%q",
			interactive.state, interactive.message)
	}
	if len(calls) != 0 {
		t.Fatal("the runner must not be called for invalid input")
	}
}

func TestResolverHiddenWithoutRunner(t *testing.T) {
	interactive, _ := openTestModel(t)
	if strings.Contains(interactive.View(), "resolve a part") {
		t.Fatalf("resolver key must be hidden without a runner:\n%s", interactive.View())
	}
	interactive = press(t, interactive, rune_('l'))
	if interactive.state != stateList {
		t.Fatalf("the l key must be inert without a runner, state = %d", interactive.state)
	}
}
