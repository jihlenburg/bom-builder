// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// LookupRunner executes one demand against the configured providers and
// returns the sourced part with every normalized candidate offer. The CLI
// injects it so the model layer never constructs provider clients itself
// and tests can substitute a deterministic fake.
type LookupRunner func(
	ctx context.Context,
	demand procurement.Demand,
	providers string,
) (procurement.SourcedPart, error)

// lookupResultMsg delivers one asynchronous lookup outcome. seq guards
// against results from an abandoned lookup arriving late.
type lookupResultMsg struct {
	seq  int
	part procurement.SourcedPart
	err  error
}

const lookupDeadline = 2 * time.Minute

// lookup form field order; the labels below must stay in sync.
const (
	lookupFieldPartNumber = iota
	lookupFieldManufacturer
	lookupFieldQuantity
	lookupFieldProviders
	lookupFieldCount
)

var lookupFieldLabels = [lookupFieldCount]string{
	"Part number",
	"Manufacturer",
	"Quantity",
	"Providers (auto or comma-separated)",
}

func (interactive *model) resetLookupForm() {
	for index := range interactive.lookupInputs {
		interactive.lookupInputs[index].SetValue("")
		interactive.lookupInputs[index].Blur()
	}
	interactive.lookupInputs[lookupFieldQuantity].SetValue("1")
	interactive.lookupInputs[lookupFieldProviders].SetValue("auto")
	interactive.focusIndex = 0
	interactive.lookupInputs[0].Focus()
}

func (interactive model) updateLookupForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		interactive.state = stateList
		return interactive, nil
	case "ctrl+s":
		return interactive.submitLookup()
	case "enter":
		if interactive.focusIndex == lookupFieldCount-1 {
			return interactive.submitLookup()
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
	interactive.lookupInputs[interactive.focusIndex], command =
		interactive.lookupInputs[interactive.focusIndex].Update(key)
	return interactive, command
}

func (interactive model) submitLookup() (tea.Model, tea.Cmd) {
	partNumber := strings.TrimSpace(interactive.lookupInputs[lookupFieldPartNumber].Value())
	manufacturer := strings.TrimSpace(interactive.lookupInputs[lookupFieldManufacturer].Value())
	quantityText := strings.TrimSpace(interactive.lookupInputs[lookupFieldQuantity].Value())
	providers := strings.TrimSpace(interactive.lookupInputs[lookupFieldProviders].Value())
	if partNumber == "" || manufacturer == "" {
		interactive.fail(fmt.Errorf("part number and manufacturer are required"))
		return interactive, nil
	}
	quantity, err := strconv.Atoi(quantityText)
	if err != nil || quantity < 1 {
		interactive.fail(fmt.Errorf("quantity must be a positive integer"))
		return interactive, nil
	}
	if providers == "" {
		providers = "auto"
	}
	demand := procurement.Demand{
		PartNumber:       partNumber,
		Manufacturer:     manufacturer,
		QuantityPerUnit:  quantity,
		RequiredQuantity: quantity,
	}
	interactive.clearMessage()
	interactive.lookupSeq++
	interactive.lookupDemand = demand
	interactive.state = stateLookupWait
	sequence := interactive.lookupSeq
	runner := interactive.lookup
	return interactive, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), lookupDeadline)
		defer cancel()
		part, err := runner(ctx, demand, providers)
		return lookupResultMsg{seq: sequence, part: part, err: err}
	}
}

func (interactive model) updateLookupWait(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		// Abandon the lookup: bumping the sequence makes the late result
		// message a no-op when it eventually arrives.
		interactive.lookupSeq++
		interactive.state = stateLookupForm
	}
	return interactive, nil
}

func (interactive model) applyLookupResult(result lookupResultMsg) (tea.Model, tea.Cmd) {
	if result.seq != interactive.lookupSeq || interactive.state != stateLookupWait {
		return interactive, nil
	}
	if result.err != nil {
		interactive.fail(result.err)
		interactive.state = stateLookupForm
		return interactive, nil
	}
	interactive.candidates = result.part
	interactive.candidateCursor = 0
	interactive.state = stateCandidates
	return interactive, nil
}

func (interactive model) updateCandidates(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		interactive.state = stateList
	case "up", "k":
		if interactive.candidateCursor > 0 {
			interactive.candidateCursor--
		}
	case "down", "j":
		if interactive.candidateCursor < len(interactive.candidates.Offers)-1 {
			interactive.candidateCursor++
		}
	case "enter":
		if interactive.candidateCursor < len(interactive.candidates.Offers) {
			interactive.prefillApproveFromOffer(
				interactive.candidates.Demand,
				interactive.candidates.Offers[interactive.candidateCursor],
			)
			interactive.state = stateApprove
		}
	}
	return interactive, nil
}

// prefillApproveFromOffer seeds the approve form with the chosen candidate.
// The person approving still reviews every field and must type their own
// name: choosing a row never clears engineering review by itself.
func (interactive *model) prefillApproveFromOffer(
	demand procurement.Demand,
	offer procurement.Offer,
) {
	interactive.clearMessage()
	for index := range interactive.approveInputs {
		interactive.approveInputs[index].SetValue("")
		interactive.approveInputs[index].Blur()
	}
	replacementManufacturer := strings.TrimSpace(offer.Manufacturer)
	if replacementManufacturer == "" {
		replacementManufacturer = demand.Manufacturer
	}
	replacementPart := strings.TrimSpace(offer.ManufacturerPartNumber)
	if replacementPart == "" {
		replacementPart = demand.PartNumber
	}
	interactive.approveInputs[fieldManufacturer].SetValue(demand.Manufacturer)
	interactive.approveInputs[fieldPartNumber].SetValue(demand.PartNumber)
	interactive.approveInputs[fieldReplacementManufacturer].SetValue(replacementManufacturer)
	interactive.approveInputs[fieldReplacementPartNumber].SetValue(replacementPart)
	interactive.approveInputs[fieldProvider].SetValue(offer.Provider)
	interactive.approveInputs[fieldProviderSKU].SetValue(offer.DistributorPartNumber)
	interactive.approveInputs[fieldNote].SetValue(offerEvidenceNote(offer))
	interactive.focusIndex = fieldApprovedBy
	interactive.approveInputs[fieldApprovedBy].Focus()
}

// offerEvidenceNote summarizes the provider evidence a choice was based on.
func offerEvidenceNote(offer procurement.Offer) string {
	parts := []string{"resolver: " + offer.Provider + " match " + offer.MatchMethod}
	if offer.AvailableQuantity != nil {
		parts = append(parts, fmt.Sprintf("stock %d", *offer.AvailableQuantity))
	}
	if plan := offerPlan(offer); plan != nil {
		parts = append(parts, fmt.Sprintf(
			"unit %s %s",
			plan.UnitPrice.String(),
			plan.Currency,
		))
	}
	if offer.ReviewRequired {
		parts = append(parts, "review-required match")
	}
	return strings.Join(parts, ", ")
}

func offerPlan(offer procurement.Offer) *procurement.PurchasePlan {
	if offer.SelectedPlan != nil {
		return offer.SelectedPlan
	}
	return offer.CandidatePlan
}

func (interactive model) viewLookupForm() string {
	var builder strings.Builder
	builder.WriteString(titleStyle.Render("Resolve a part"))
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render(
		"Source one part, review the candidates, and record the human decision.",
	))
	builder.WriteString("\n\n")
	for index, label := range lookupFieldLabels {
		builder.WriteString(label)
		builder.WriteString("\n")
		builder.WriteString(interactive.lookupInputs[index].View())
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render("tab next field · ctrl+s look up · esc cancel"))
	builder.WriteString("\n")
	return builder.String()
}

func (interactive model) viewLookupWait() string {
	demand := interactive.lookupDemand
	return titleStyle.Render("Looking up "+demand.PartNumber) + "\n\n" +
		fmt.Sprintf(
			"%s %s, quantity %d — contacting providers…\n\n",
			demand.Manufacturer,
			demand.PartNumber,
			demand.RequiredQuantity,
		) +
		dimStyle.Render("esc abandon") + "\n"
}

func (interactive model) viewCandidates() string {
	part := interactive.candidates
	var builder strings.Builder
	builder.WriteString(titleStyle.Render(
		"Candidates for " + part.Demand.Manufacturer + " " + part.Demand.PartNumber,
	))
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render(fmt.Sprintf(
		"status %s · required %d",
		part.Status,
		part.Demand.RequiredQuantity,
	)))
	builder.WriteString("\n\n")
	if part.IssueMessage != "" {
		builder.WriteString(part.IssueCode + ": " + part.IssueMessage)
		builder.WriteString("\n\n")
	}
	if len(part.Offers) == 0 {
		builder.WriteString(dimStyle.Render("no candidate offers returned"))
		builder.WriteString("\n")
	}
	for index, offer := range part.Offers {
		line := offerLine(offer)
		if index == interactive.candidateCursor {
			line = selectedStyle.Render(line)
		} else if offer.ReviewRequired {
			line = dimStyle.Render(line)
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(dimStyle.Render(
		"enter approve this candidate · esc back",
	))
	builder.WriteString("\n")
	return builder.String()
}

func offerLine(offer procurement.Offer) string {
	stock := "stock ?"
	if offer.AvailableQuantity != nil {
		stock = fmt.Sprintf("stock %d", *offer.AvailableQuantity)
	}
	price := "price ?"
	if plan := offerPlan(offer); plan != nil {
		price = plan.UnitPrice.String() + " " + plan.Currency
	}
	marker := "safe"
	if offer.ReviewRequired {
		marker = "review"
	}
	return fmt.Sprintf(
		"%-9s %-8s %s %s · %s · %s · %s",
		offer.Provider,
		marker,
		offer.ManufacturerPartNumber,
		offer.DistributorPartNumber,
		offer.MatchMethod,
		stock,
		price,
	)
}
