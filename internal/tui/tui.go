// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package tui implements BOM Builder's interactive terminal mode. The first
// slice is a resolutions manager: browsing, approving, and revoking
// human-approved part resolutions against the audited store.
//
// The interactive mode is the one deliberate exception to the machine-first
// stdout protocol: it renders a full-screen terminal interface for a human
// and refuses to start when stdout is not a terminal, so agents and scripts
// can never confuse it with a JSON command.
//
// Dependency note: the interface is built on github.com/charmbracelet
// bubbletea/bubbles/lipgloss — the de-facto standard pure-Go TUI stack. It
// needs no cgo, supports Windows terminals, and keeps the model layer a pure
// state machine (Update/View are side-effect-free functions), which makes
// the whole interface unit-testable without a terminal.
package tui

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

// Options configures one interactive session.
type Options struct {
	// DatabasePath locates the resolutions SQLite database. The session
	// opens it read-write: interactive mode exists to record decisions.
	DatabasePath string
}

// Run opens the resolutions store and blocks inside the terminal interface
// until the user quits. It must only be called with a real terminal on
// stdin and stdout; the CLI enforces that before calling.
func Run(options Options) error {
	if options.DatabasePath == "" {
		return errors.New("resolutions database path is required")
	}
	store, err := resolutions.Open(options.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	model, err := newModel(store)
	if err != nil {
		return err
	}
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err = program.Run()
	return err
}
