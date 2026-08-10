// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

func openTestModel(t *testing.T) (model, *resolutions.Store) {
	t.Helper()
	return openTestModelWithLookup(t, nil)
}

func openTestModelWithLookup(
	t *testing.T,
	lookup LookupRunner,
) (model, *resolutions.Store) {
	t.Helper()
	store, err := resolutions.Open(filepath.Join(t.TempDir(), "resolutions.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	created, err := newModel(store, lookup)
	if err != nil {
		t.Fatalf("new model: %v", err)
	}
	// A deterministic but advancing clock: the store derives resolution
	// identities from request content and time, so a frozen clock would
	// make an identical re-approval collide (as it should).
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tick := 0
	created.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Second)
	}
	return created, store
}

func press(t *testing.T, interactive model, keys ...tea.KeyMsg) model {
	t.Helper()
	for _, key := range keys {
		updated, _ := interactive.Update(key)
		interactive = updated.(model)
	}
	return interactive
}

func typeText(t *testing.T, interactive model, text string) model {
	t.Helper()
	return press(t, interactive, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func key(keyType tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: keyType}
}

func rune_(character rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}}
}

func approveViaForm(t *testing.T, interactive model, partNumber string) model {
	t.Helper()
	interactive = press(t, interactive, rune_('a'))
	for _, value := range []string{
		"Texas Instruments",
		partNumber,
		"Texas Instruments",
		"TMP421AQDCNRQ1",
		"mouser",
		"595-TMP421AQDCNRQ1",
		"J. Ihlenburg",
		"cleared for Rev A",
	} {
		interactive = typeText(t, interactive, value)
		interactive = press(t, interactive, key(tea.KeyTab))
	}
	return press(t, interactive, key(tea.KeyCtrlS))
}

func TestEmptyStoreRendersEmptyList(t *testing.T) {
	interactive, _ := openTestModel(t)
	view := interactive.View()
	if !strings.Contains(view, "no resolutions recorded") {
		t.Fatalf("expected an empty-list message, got:\n%s", view)
	}
	if !strings.Contains(view, "approved resolutions") {
		t.Fatalf("expected the list title, got:\n%s", view)
	}
}

func TestApproveFormRecordsResolution(t *testing.T) {
	interactive, store := openTestModel(t)
	interactive = approveViaForm(t, interactive, "TMP421-Q1")

	if interactive.state != stateList {
		t.Fatalf("expected return to the list, state = %d (%s)", interactive.state, interactive.message)
	}
	if interactive.isError || !strings.Contains(interactive.message, "approved resolution") {
		t.Fatalf("expected an approval status message, got %q", interactive.message)
	}
	records, err := store.List(context.Background(), "", "", 100, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 1 ||
		records[0].PartNumber != "TMP421-Q1" ||
		records[0].ApprovedBy != "J. Ihlenburg" ||
		records[0].Replacement.ProviderSKU != "595-TMP421AQDCNRQ1" {
		t.Fatalf("stored record does not match the form: %+v", records)
	}
	view := interactive.View()
	if !strings.Contains(view, "TMP421-Q1") || !strings.Contains(view, "active") {
		t.Fatalf("expected the new resolution in the list view:\n%s", view)
	}
}

func TestApproveFormRejectsIncompleteRequestAndStaysOnForm(t *testing.T) {
	interactive, store := openTestModel(t)
	interactive = press(t, interactive, rune_('a'))
	interactive = press(t, interactive, key(tea.KeyCtrlS))
	if interactive.state != stateApprove {
		t.Fatalf("expected to stay on the form, state = %d", interactive.state)
	}
	if !interactive.isError || interactive.message == "" {
		t.Fatalf("expected a validation error message, got %q", interactive.message)
	}
	records, err := store.List(context.Background(), "", "", 100, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("nothing may be stored on validation failure: %+v", records)
	}
}

func TestRevokeFlowRetiresSelectedResolution(t *testing.T) {
	interactive, store := openTestModel(t)
	interactive = approveViaForm(t, interactive, "TMP421-Q1")

	interactive = press(t, interactive, rune_('r'))
	if interactive.state != stateRevoke {
		t.Fatalf("expected the revoke form, state = %d", interactive.state)
	}
	view := interactive.View()
	if !strings.Contains(view, "Revoke resolution") {
		t.Fatalf("expected the revoke summary:\n%s", view)
	}
	interactive = typeText(t, interactive, "J. Ihlenburg")
	interactive = press(t, interactive, key(tea.KeyTab))
	interactive = typeText(t, interactive, "design change")
	interactive = press(t, interactive, key(tea.KeyCtrlS))

	if interactive.state != stateList || interactive.isError {
		t.Fatalf("expected a successful revocation, state=%d message=%q",
			interactive.state, interactive.message)
	}
	records, err := store.List(context.Background(), "", "", 100, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 1 || records[0].Status != resolutions.StatusRevoked {
		t.Fatalf("expected one revoked record, got %+v", records)
	}
	events, err := store.History(context.Background(), "", "", 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if events[0].Action != resolutions.ActionRevoked ||
		events[0].Details != "design change" {
		t.Fatalf("expected a revoked audit event, got %+v", events[0])
	}
}

func TestRevokeWithoutRevokerFailsVisibly(t *testing.T) {
	interactive, _ := openTestModel(t)
	interactive = approveViaForm(t, interactive, "TMP421-Q1")
	interactive = press(t, interactive, rune_('r'), key(tea.KeyCtrlS))
	if interactive.state != stateRevoke || !interactive.isError {
		t.Fatalf("expected a visible failure on the revoke form, state=%d message=%q",
			interactive.state, interactive.message)
	}
}

func TestHistoryViewShowsAuditTrailForSelection(t *testing.T) {
	interactive, _ := openTestModel(t)
	interactive = approveViaForm(t, interactive, "TMP421-Q1")
	interactive = press(t, interactive, rune_('h'))
	if interactive.state != stateHistory {
		t.Fatalf("expected the history view, state = %d", interactive.state)
	}
	view := interactive.View()
	if !strings.Contains(view, "approved") || !strings.Contains(view, "J. Ihlenburg") {
		t.Fatalf("expected the approval event in history:\n%s", view)
	}
	interactive = press(t, interactive, key(tea.KeyEsc))
	if interactive.state != stateList {
		t.Fatalf("expected esc to return to the list, state = %d", interactive.state)
	}
}

func TestInactiveToggleRevealsSupersededRecords(t *testing.T) {
	interactive, _ := openTestModel(t)
	interactive = approveViaForm(t, interactive, "TMP421-Q1")
	// Approving the same demand again supersedes the first record.
	interactive = approveViaForm(t, interactive, "TMP421-Q1")

	if len(interactive.records) != 1 {
		t.Fatalf("active-only list must hide the superseded record: %d", len(interactive.records))
	}
	interactive = press(t, interactive, rune_('i'))
	if len(interactive.records) != 2 {
		t.Fatalf("expected both records after the toggle: %d", len(interactive.records))
	}
	if !strings.Contains(interactive.View(), "superseded") {
		t.Fatalf("expected the superseded record to render:\n%s", interactive.View())
	}
}

func TestDetailViewShowsFullRecord(t *testing.T) {
	interactive, _ := openTestModel(t)
	interactive = approveViaForm(t, interactive, "TMP421-Q1")
	interactive = press(t, interactive, key(tea.KeyEnter))
	if interactive.state != stateDetail {
		t.Fatalf("expected the detail view, state = %d", interactive.state)
	}
	view := interactive.View()
	for _, expected := range []string{
		"TMP421-Q1",
		"mouser 595-TMP421AQDCNRQ1",
		"J. Ihlenburg",
		"cleared for Rev A",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("detail view missing %q:\n%s", expected, view)
		}
	}
}

func TestQuitKeysIssueQuitCommand(t *testing.T) {
	interactive, _ := openTestModel(t)
	_, command := interactive.Update(rune_('q'))
	if command == nil {
		t.Fatal("expected q to quit from the list view")
	}
	_, command = interactive.Update(key(tea.KeyCtrlC))
	if command == nil {
		t.Fatal("expected ctrl+c to quit from any view")
	}
}
