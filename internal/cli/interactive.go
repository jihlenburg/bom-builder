// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/tui"
)

// runInteractive starts the full-screen terminal mode. It is the one
// deliberate exception to the JSON stdout protocol, and therefore refuses
// to start unless stdin and stdout are real terminals: an agent or pipeline
// that reaches this command by mistake gets a machine-readable error, never
// a hanging interface.
func runInteractive(args []string, stdin io.Reader, stdout io.Writer) int {
	args, path, pretty, err := consumeResolutionsCommandCommon(args, false)
	if err != nil {
		return emitError(stdout, "interactive", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "interactive", args, pretty)
	}
	if !isTerminal(stdin) || !isTerminal(stdout) {
		return emitError(
			stdout,
			"interactive",
			"INTERACTIVE_TTY_REQUIRED",
			"interactive mode needs a terminal on stdin and stdout; use the JSON commands in scripts",
			contract.ExitInput,
			pretty,
		)
	}
	if err := tui.Run(tui.Options{DatabasePath: path}); err != nil {
		return emitResolutionsError(stdout, "interactive", err, pretty)
	}
	return contract.ExitOK
}

func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	descriptor := file.Fd()
	return isatty.IsTerminal(descriptor) || isatty.IsCygwinTerminal(descriptor)
}
