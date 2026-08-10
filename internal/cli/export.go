// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/bom"
	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/design"
)

// runExport dispatches export subcommands.
func runExport(args []string, stdin io.Reader, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "ec-bom" {
		return emitError(
			stdout,
			"export",
			"UNKNOWN_SUBCOMMAND",
			"supported subcommand: export ec-bom",
			contract.ExitInput,
			false,
		)
	}
	return runExportECBOM(args[1:], stdin, stdout)
}

// runExportECBOM renders one validated design as a Eurocircuits
// assembly BOM (semicolon CSV, CRLF) without contacting providers.
func runExportECBOM(args []string, stdin io.Reader, stdout io.Writer) int {
	const command = "export ec-bom"
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, command, "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	remaining, outputPath, hasOutput, err := consumeValueFlag(remaining, "--output")
	if err != nil {
		return emitError(stdout, command, "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	outputPath = strings.TrimSpace(outputPath)
	if !hasOutput || outputPath == "" {
		return emitError(
			stdout,
			command,
			"OUTPUT_REQUIRED",
			"--output <file> is required; existing files are never overwritten",
			contract.ExitInput,
			pretty,
		)
	}
	if len(remaining) == 0 {
		return emitError(
			stdout,
			command,
			"DESIGN_REQUIRED",
			"exactly one design source is required",
			contract.ExitInput,
			pretty,
		)
	}
	for _, source := range remaining {
		if strings.HasPrefix(source, "--") {
			return emitUnexpected(stdout, command, []string{source}, pretty)
		}
	}
	designs, err := design.LoadSources(remaining, stdin)
	if err != nil {
		return emitError(stdout, command, "INVALID_INPUT", err.Error(), contract.ExitInput, pretty)
	}
	if len(designs) != 1 {
		return emitError(
			stdout,
			command,
			"SINGLE_DESIGN_REQUIRED",
			"export ec-bom renders exactly one design per output file",
			contract.ExitInput,
			pretty,
		)
	}
	data, renderWarnings, err := bom.EurocircuitsCSV(designs[0])
	if err != nil {
		return emitError(stdout, command, "RENDER_FAILED", err.Error(), contract.ExitInternal, pretty)
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return emitError(
				stdout,
				command,
				"OUTPUT_EXISTS",
				"output file already exists; existing files are never overwritten",
				contract.ExitInput,
				pretty,
			)
		}
		return emitError(stdout, command, "OUTPUT_UNWRITABLE", err.Error(), contract.ExitInput, pretty)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(outputPath)
		return emitError(
			stdout,
			command,
			"OUTPUT_WRITE_FAILED",
			"could not write the eC-BOM file",
			contract.ExitInternal,
			pretty,
		)
	}
	absolutePath, err := filepath.Abs(outputPath)
	if err != nil {
		absolutePath = outputPath
	}
	digest := sha256.Sum256(data)
	envelope := contract.ExportEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "written",
		ExitCode:      contract.ExitOK,
		Command:       command,
		Version:       app.Version,
		Design:        designs[0].Design,
		Artifact: contract.ExportArtifact{
			OutputPath: absolutePath,
			Format:     "ec-bom-csv",
			SizeBytes:  int64(len(data)),
			SHA256:     hex.EncodeToString(digest[:]),
			LineCount:  len(designs[0].Parts),
		},
		Warnings: renderWarnings,
		Errors:   []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}
