// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/webui"
)

// runServe starts the local web interface. Stdout still carries exactly
// one JSON document — the startup envelope with the tokenized URL — and
// the process then serves until interrupted; diagnostics go to stderr.
func runServe(args []string, stdout, stderr io.Writer) int {
	args, path, pretty, err := consumeResolutionsCommandCommon(args, false)
	if err != nil {
		return emitError(stdout, "serve", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, listenAddress, hasListen, err := consumeValueFlag(args, "--listen")
	if err != nil {
		return emitError(stdout, "serve", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "serve", args, pretty)
	}
	if !hasListen {
		listenAddress = "127.0.0.1:0"
	}
	if err := validateLoopbackListen(listenAddress); err != nil {
		return emitError(
			stdout,
			"serve",
			"LISTEN_NOT_LOOPBACK",
			err.Error(),
			contract.ExitInput,
			pretty,
		)
	}
	server, err := webui.New(webui.Options{
		DatabasePath: path,
		Lookup:       interactiveLookupRunner(),
	})
	if err != nil {
		return emitResolutionsError(stdout, "serve", err, pretty)
	}
	defer server.Close()
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return emitError(stdout, "serve", "LISTEN_FAILED", err.Error(), contract.ExitInternal, pretty)
	}
	url := fmt.Sprintf("http://%s/?token=%s", listener.Addr(), server.Token())
	envelope := contract.ServeEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "serving",
		ExitCode:      contract.ExitOK,
		Command:       "serve",
		Version:       app.Version,
		URL:           url,
		Address:       listener.Addr().String(),
		Database:      server.DatabasePath(),
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	emitJSON(stdout, envelope, pretty)
	fmt.Fprintf(stderr, "bom-builder web interface: %s\n", url)
	fmt.Fprintln(stderr, "The URL contains this session's access token; do not share it.")
	fmt.Fprintln(stderr, "Press Ctrl+C to stop.")

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveFailed := make(chan error, 1)
	go func() {
		if serveErr := httpServer.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			serveFailed <- serveErr
		}
	}()
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	defer signal.Stop(interrupted)
	select {
	case <-interrupted:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		fmt.Fprintln(stderr, "web interface stopped")
		return contract.ExitOK
	case serveErr := <-serveFailed:
		fmt.Fprintf(stderr, "web interface failed: %s\n", serveErr)
		return contract.ExitInternal
	}
}

// validateLoopbackListen rejects any listen address that would expose the
// interface beyond this machine. The token is a second line of defense,
// not a reason to serve the store to a network.
func validateLoopbackListen(address string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("listen address must be host:port on a loopback interface")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	parsed := net.ParseIP(strings.Trim(host, "[]"))
	if parsed == nil || !parsed.IsLoopback() {
		return fmt.Errorf(
			"listen address %q is not a loopback interface; the web interface is local-only",
			address,
		)
	}
	return nil
}
