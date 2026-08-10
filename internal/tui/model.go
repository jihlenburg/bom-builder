// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

type sessionState int

const (
	stateList sessionState = iota
	stateDetail
	stateHistory
	stateApprove
	stateRevoke
)

const listLimit = 1000

// approve form field order; the labels below must stay in sync.
const (
	fieldManufacturer = iota
	fieldPartNumber
	fieldReplacementManufacturer
	fieldReplacementPartNumber
	fieldProvider
	fieldProviderSKU
	fieldApprovedBy
	fieldNote
	approveFieldCount
)

var approveFieldLabels = [approveFieldCount]string{
	"Original manufacturer",
	"Original part number",
	"Replacement manufacturer",
	"Replacement part number",
	"Provider (optional: mouser|digikey|ti|nxp|microchip)",
	"Provider SKU (optional)",
	"Approved by (person clearing engineering review)",
	"Note (optional)",
}

const (
	revokeFieldRevokedBy = iota
	revokeFieldReason
	revokeFieldCount
)

var revokeFieldLabels = [revokeFieldCount]string{
	"Revoked by (person retiring the resolution)",
	"Reason (optional)",
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	statusStyle   = lipgloss.NewStyle().Bold(true)
	errorStyle    = lipgloss.NewStyle().Bold(true).Underline(true)
	revokedStyle  = lipgloss.NewStyle().Strikethrough(true)
)

type model struct {
	store *resolutions.Store
	now   func() time.Time

	state           sessionState
	records         []resolutions.Record
	cursor          int
	includeInactive bool
	events          []resolutions.Event
	status          resolutions.Status

	// message holds the outcome of the last action; isError selects its
	// styling. Both reset on the next action.
	message string
	isError bool

	approveInputs [approveFieldCount]textinput.Model
	revokeInputs  [revokeFieldCount]textinput.Model
	focusIndex    int

	width  int
	height int
}

func newModel(store *resolutions.Store) (model, error) {
	created := model{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		state: stateList,
	}
	for index := range created.approveInputs {
		input := textinput.New()
		input.Prompt = "> "
		input.CharLimit = 500
		created.approveInputs[index] = input
	}
	for index := range created.revokeInputs {
		input := textinput.New()
		input.Prompt = "> "
		input.CharLimit = 500
		created.revokeInputs[index] = input
	}
	if err := created.reload(); err != nil {
		return model{}, err
	}
	return created, nil
}

func (interactive *model) reload() error {
	records, err := interactive.store.List(
		context.Background(),
		"",
		"",
		listLimit,
		interactive.includeInactive,
	)
	if err != nil {
		return err
	}
	status, err := interactive.store.StoreStatus(context.Background())
	if err != nil {
		return err
	}
	interactive.records = records
	interactive.status = status
	if interactive.cursor >= len(records) {
		interactive.cursor = len(records) - 1
	}
	if interactive.cursor < 0 {
		interactive.cursor = 0
	}
	return nil
}

func (interactive model) selectedRecord() *resolutions.Record {
	if len(interactive.records) == 0 || interactive.cursor >= len(interactive.records) {
		return nil
	}
	return &interactive.records[interactive.cursor]
}

// Init implements tea.Model.
func (interactive model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model. It is a pure state transition so the whole
// interface is testable without a terminal.
func (interactive model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := message.(type) {
	case tea.WindowSizeMsg:
		interactive.width = typed.Width
		interactive.height = typed.Height
		return interactive, nil
	case tea.KeyMsg:
		if typed.String() == "ctrl+c" {
			return interactive, tea.Quit
		}
		switch interactive.state {
		case stateList:
			return interactive.updateList(typed)
		case stateDetail:
			return interactive.updateDetail(typed)
		case stateHistory:
			return interactive.updateHistory(typed)
		case stateApprove:
			return interactive.updateApprove(typed)
		case stateRevoke:
			return interactive.updateRevoke(typed)
		}
	}
	return interactive, nil
}

func (interactive model) updateList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q":
		return interactive, tea.Quit
	case "up", "k":
		if interactive.cursor > 0 {
			interactive.cursor--
		}
	case "down", "j":
		if interactive.cursor < len(interactive.records)-1 {
			interactive.cursor++
		}
	case "i":
		interactive.includeInactive = !interactive.includeInactive
		interactive.fail(interactive.reload())
	case "enter":
		if interactive.selectedRecord() != nil {
			interactive.state = stateDetail
		}
	case "h":
		if record := interactive.selectedRecord(); record != nil {
			events, err := interactive.store.History(
				context.Background(),
				record.Manufacturer,
				record.PartNumber,
				listLimit,
			)
			if !interactive.fail(err) {
				interactive.events = events
				interactive.state = stateHistory
			}
		}
	case "a":
		interactive.clearMessage()
		for index := range interactive.approveInputs {
			interactive.approveInputs[index].SetValue("")
			interactive.approveInputs[index].Blur()
		}
		interactive.focusIndex = 0
		interactive.approveInputs[0].Focus()
		interactive.state = stateApprove
	case "r":
		if record := interactive.selectedRecord(); record != nil &&
			record.Status == resolutions.StatusActive {
			interactive.clearMessage()
			for index := range interactive.revokeInputs {
				interactive.revokeInputs[index].SetValue("")
				interactive.revokeInputs[index].Blur()
			}
			interactive.focusIndex = 0
			interactive.revokeInputs[0].Focus()
			interactive.state = stateRevoke
		}
	}
	return interactive, nil
}

func (interactive model) updateDetail(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "enter", "q":
		interactive.state = stateList
	}
	return interactive, nil
}

func (interactive model) updateHistory(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "enter", "q":
		interactive.state = stateList
	}
	return interactive, nil
}

func (interactive model) updateApprove(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		interactive.state = stateList
		return interactive, nil
	case "ctrl+s":
		return interactive.submitApprove()
	case "enter":
		if interactive.focusIndex == approveFieldCount-1 {
			return interactive.submitApprove()
		}
		interactive.moveFocus(1)
		return interactive, nil
	case "tab", "down":
		interactive.moveFocus(1)
		return interactive, nil
	case "shift+tab", "up":
		interactive.moveFocus(-1)
		return interactive, nil
	}
	var command tea.Cmd
	interactive.approveInputs[interactive.focusIndex], command =
		interactive.approveInputs[interactive.focusIndex].Update(key)
	return interactive, command
}

func (interactive model) updateRevoke(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		interactive.state = stateList
		return interactive, nil
	case "ctrl+s":
		return interactive.submitRevoke()
	case "enter":
		if interactive.focusIndex == revokeFieldCount-1 {
			return interactive.submitRevoke()
		}
		interactive.moveFocus(1)
		return interactive, nil
	case "tab", "down":
		interactive.moveFocus(1)
		return interactive, nil
	case "shift+tab", "up":
		interactive.moveFocus(-1)
		return interactive, nil
	}
	var command tea.Cmd
	interactive.revokeInputs[interactive.focusIndex], command =
		interactive.revokeInputs[interactive.focusIndex].Update(key)
	return interactive, command
}

func (interactive *model) moveFocus(direction int) {
	count := approveFieldCount
	if interactive.state == stateRevoke {
		count = revokeFieldCount
	}
	next := (interactive.focusIndex + direction + count) % count
	if interactive.state == stateRevoke {
		interactive.revokeInputs[interactive.focusIndex].Blur()
		interactive.revokeInputs[next].Focus()
	} else {
		interactive.approveInputs[interactive.focusIndex].Blur()
		interactive.approveInputs[next].Focus()
	}
	interactive.focusIndex = next
}

func (interactive model) submitApprove() (tea.Model, tea.Cmd) {
	request := resolutions.Request{
		Manufacturer: interactive.approveInputs[fieldManufacturer].Value(),
		PartNumber:   interactive.approveInputs[fieldPartNumber].Value(),
		Replacement: resolutions.Replacement{
			Manufacturer: interactive.approveInputs[fieldReplacementManufacturer].Value(),
			PartNumber:   interactive.approveInputs[fieldReplacementPartNumber].Value(),
			Provider:     interactive.approveInputs[fieldProvider].Value(),
			ProviderSKU:  interactive.approveInputs[fieldProviderSKU].Value(),
		},
		ApprovedBy: interactive.approveInputs[fieldApprovedBy].Value(),
		Note:       interactive.approveInputs[fieldNote].Value(),
	}
	record, superseded, err := interactive.store.Approve(
		context.Background(),
		request,
		interactive.now(),
	)
	if interactive.fail(err) {
		return interactive, nil
	}
	interactive.message = "approved resolution " + record.ResolutionID
	if superseded != nil {
		interactive.message += " (superseded " + superseded.ResolutionID + ")"
	}
	interactive.isError = false
	interactive.state = stateList
	interactive.fail(interactive.reload())
	return interactive, nil
}

func (interactive model) submitRevoke() (tea.Model, tea.Cmd) {
	record := interactive.selectedRecord()
	if record == nil {
		interactive.state = stateList
		return interactive, nil
	}
	revokedBy := interactive.revokeInputs[revokeFieldRevokedBy].Value()
	reason := interactive.revokeInputs[revokeFieldReason].Value()
	// Preview first, then apply with the returned token. The token binds
	// the previewed record content, so a change from another process
	// between the two calls surfaces as a stale-preview error here
	// instead of silently revoking a record the user never saw.
	preview, err := interactive.store.Revoke(
		context.Background(),
		record.ResolutionID,
		revokedBy,
		reason,
		"",
		interactive.now(),
	)
	if interactive.fail(err) {
		return interactive, nil
	}
	applied, err := interactive.store.Revoke(
		context.Background(),
		record.ResolutionID,
		revokedBy,
		reason,
		preview.ApplyToken,
		interactive.now(),
	)
	if interactive.fail(err) {
		return interactive, nil
	}
	interactive.message = "revoked resolution " + applied.ResolutionID
	interactive.isError = false
	interactive.state = stateList
	interactive.fail(interactive.reload())
	return interactive, nil
}

// fail records an error message and reports whether err was non-nil.
func (interactive *model) fail(err error) bool {
	if err == nil {
		return false
	}
	interactive.message = err.Error()
	interactive.isError = true
	return true
}

func (interactive *model) clearMessage() {
	interactive.message = ""
	interactive.isError = false
}

// View implements tea.Model.
func (interactive model) View() string {
	var view string
	switch interactive.state {
	case stateDetail:
		view = interactive.viewDetail()
	case stateHistory:
		view = interactive.viewHistory()
	case stateApprove:
		view = interactive.viewForm(
			"Approve a resolution",
			approveFieldLabels[:],
			interactive.approveInputs[:],
		)
	case stateRevoke:
		view = interactive.viewRevoke()
	default:
		view = interactive.viewList()
	}
	if interactive.message != "" {
		style := statusStyle
		if interactive.isError {
			style = errorStyle
		}
		view += "\n" + style.Render(interactive.message) + "\n"
	}
	return view
}

func (interactive model) viewList() string {
	var builder strings.Builder
	builder.WriteString(titleStyle.Render("BOM Builder — approved resolutions"))
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render(fmt.Sprintf(
		"%s | active %d, superseded %d, revoked %d",
		interactive.status.Path,
		interactive.status.ActiveCount,
		interactive.status.SupersededCount,
		interactive.status.RevokedCount,
	)))
	builder.WriteString("\n\n")
	if len(interactive.records) == 0 {
		builder.WriteString(dimStyle.Render("no resolutions recorded"))
		builder.WriteString("\n")
	}
	for index, record := range interactive.records {
		line := fmt.Sprintf(
			"%-10s %s %s -> %s %s (%s)",
			record.Status,
			record.Manufacturer,
			record.PartNumber,
			record.Replacement.Manufacturer,
			record.Replacement.PartNumber,
			record.ApprovedBy,
		)
		switch {
		case index == interactive.cursor:
			line = selectedStyle.Render(line)
		case record.Status == resolutions.StatusRevoked:
			line = revokedStyle.Render(line)
		case record.Status != resolutions.StatusActive:
			line = dimStyle.Render(line)
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render(
		"enter detail · a approve · r revoke · h history · i " +
			interactive.inactiveToggleLabel() + " · q quit",
	))
	builder.WriteString("\n")
	return builder.String()
}

func (interactive model) inactiveToggleLabel() string {
	if interactive.includeInactive {
		return "hide inactive"
	}
	return "show inactive"
}

func (interactive model) viewDetail() string {
	record := interactive.selectedRecord()
	if record == nil {
		return dimStyle.Render("no resolution selected") + "\n"
	}
	var builder strings.Builder
	builder.WriteString(titleStyle.Render("Resolution " + record.ResolutionID))
	builder.WriteString("\n\n")
	rows := [][2]string{
		{"Status", record.Status},
		{"Original", record.Manufacturer + " " + record.PartNumber},
		{"Replacement", record.Replacement.Manufacturer + " " + record.Replacement.PartNumber},
	}
	if record.Replacement.Provider != "" {
		rows = append(rows, [2]string{
			"Provider",
			record.Replacement.Provider + " " + record.Replacement.ProviderSKU,
		})
	}
	rows = append(rows,
		[2]string{"Approved by", record.ApprovedBy},
		[2]string{"Approved at", record.ApprovedAt.Format(time.RFC3339)},
		[2]string{"Updated at", record.UpdatedAt.Format(time.RFC3339)},
	)
	if record.Note != "" {
		rows = append(rows, [2]string{"Note", record.Note})
	}
	for _, row := range rows {
		builder.WriteString(fmt.Sprintf("%-14s %s\n", row[0], row[1]))
	}
	if len(record.SourceDocuments) > 0 {
		builder.WriteString("\nEvidence documents:\n")
		for _, document := range record.SourceDocuments {
			builder.WriteString("  " + document.URL + "\n")
			builder.WriteString(dimStyle.Render("    sha256:"+document.SHA256) + "\n")
		}
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render("esc back"))
	builder.WriteString("\n")
	return builder.String()
}

func (interactive model) viewHistory() string {
	var builder strings.Builder
	builder.WriteString(titleStyle.Render("Audit history"))
	builder.WriteString("\n\n")
	if len(interactive.events) == 0 {
		builder.WriteString(dimStyle.Render("no events"))
		builder.WriteString("\n")
	}
	for _, event := range interactive.events {
		builder.WriteString(fmt.Sprintf(
			"%s  %-10s %s  %s %s",
			event.OccurredAt.Format(time.RFC3339),
			event.Action,
			event.ResolutionID,
			event.Manufacturer,
			event.PartNumber,
		))
		if event.Actor != "" {
			builder.WriteString("  by " + event.Actor)
		}
		builder.WriteString("\n")
		if event.Details != "" {
			builder.WriteString(dimStyle.Render("    "+event.Details) + "\n")
		}
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render("esc back"))
	builder.WriteString("\n")
	return builder.String()
}

func (interactive model) viewForm(
	title string,
	labels []string,
	inputs []textinput.Model,
) string {
	var builder strings.Builder
	builder.WriteString(titleStyle.Render(title))
	builder.WriteString("\n\n")
	for index, label := range labels {
		builder.WriteString(label)
		builder.WriteString("\n")
		builder.WriteString(inputs[index].View())
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render("tab next field · ctrl+s submit · esc cancel"))
	builder.WriteString("\n")
	return builder.String()
}

func (interactive model) viewRevoke() string {
	record := interactive.selectedRecord()
	if record == nil {
		return dimStyle.Render("no resolution selected") + "\n"
	}
	summary := titleStyle.Render("Revoke resolution "+record.ResolutionID) + "\n\n" +
		fmt.Sprintf(
			"%s %s -> %s %s (approved by %s)\n\n",
			record.Manufacturer,
			record.PartNumber,
			record.Replacement.Manufacturer,
			record.Replacement.PartNumber,
			record.ApprovedBy,
		)
	var builder strings.Builder
	builder.WriteString(summary)
	for index, label := range revokeFieldLabels {
		builder.WriteString(label)
		builder.WriteString("\n")
		builder.WriteString(interactive.revokeInputs[index].View())
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render("tab next field · ctrl+s revoke · esc cancel"))
	builder.WriteString("\n")
	return builder.String()
}
