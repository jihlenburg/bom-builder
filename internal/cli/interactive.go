// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/sourcing"
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
	if err := tui.Run(tui.Options{
		DatabasePath: path,
		Lookup:       interactiveLookupRunner(),
	}); err != nil {
		return emitResolutionsError(stdout, "interactive", err, pretty)
	}
	return contract.ExitOK
}

// interactiveLookupRunner sources one demand with the same provider,
// cache, and selection semantics as the lookup command. Runtimes are
// constructed per call and torn down afterwards, so a lookup made hours
// into a session sees current configuration and holds no idle browser or
// token state between resolutions. The unnamed func type is assignable to
// both tui.LookupRunner and webui.LookupRunner.
func interactiveLookupRunner() func(
	context.Context,
	procurement.Demand,
	string,
) (procurement.SourcedPart, error) {
	return func(
		ctx context.Context,
		demand procurement.Demand,
		providers string,
	) (procurement.SourcedPart, error) {
		_, cacheConfig, err := consumeCacheFlags(nil)
		if err != nil {
			return procurement.SourcedPart{}, err
		}
		selected, err := resolveProviderSelection(
			providers,
			providers != "",
			cacheConfig.Policy,
		)
		if err != nil {
			return procurement.SourcedPart{}, err
		}
		runtimes, cacheSession, err := newProviderRuntimes(selected, cacheConfig)
		if err != nil {
			var setupError *providerRuntimeSetupError
			if errors.As(err, &setupError) {
				return procurement.SourcedPart{}, fmt.Errorf(
					"%s: %s",
					setupError.provider,
					setupError.cause.Error(),
				)
			}
			return procurement.SourcedPart{}, err
		}
		defer closeProviderRuntimeResources(runtimes, cacheSession)
		bindings := make([]sourcing.ProviderResolver, 0, len(runtimes))
		for _, runtime := range runtimes {
			bindings = append(bindings, sourcing.ProviderResolver{
				Name: runtime.name, Resolver: runtime.resolver,
			})
		}
		resolver, err := sourcing.NewMultiResolver(bindings)
		if err != nil {
			return procurement.SourcedPart{}, err
		}
		result := sourcing.Source(ctx, resolver, []procurement.Demand{demand}, 1)
		if len(result.Parts) != 1 {
			return procurement.SourcedPart{}, errors.New(
				"sourcing returned an unexpected part count",
			)
		}
		return result.Parts[0], nil
	}
}

func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	descriptor := file.Fd()
	return isatty.IsTerminal(descriptor) || isatty.IsCygwinTerminal(descriptor)
}
