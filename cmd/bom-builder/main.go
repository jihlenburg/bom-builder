// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Command bom-builder exposes the native machine-first BOM Builder CLI.
package main

import (
	"os"

	"github.com/jihlenburg/bom-builder/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
