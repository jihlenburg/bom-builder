// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package nxp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

const maxCDPMessageBytes = 16 * 1024 * 1024

// errBrowserGone marks transport failures that mean the browser process or
// its pipe is dead. Callers use it to discard the process so the next lookup
// can launch a fresh browser instead of failing forever.
var errBrowserGone = errors.New("browser connection lost")

type cdpMessage struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type cdpRead struct {
	message cdpMessage
	err     error
}

// cdpProcess is a single-consumer transport: nextID, pending, and the
// messages channel are unsynchronized by design, so at most one CDP
// operation may be in flight at a time. Client serializes Search and
// PartDetail under its operation mutex to uphold this.
type cdpProcess struct {
	command   *exec.Cmd
	toBrowser *os.File
	messages  chan cdpRead
	tempDir   string

	writeMu   sync.Mutex
	nextID    int
	sessionID string
	pending   []cdpMessage
	closeOnce sync.Once
}

func launchCDP(
	ctx context.Context,
	browserPath string,
) (*cdpProcess, error) {
	tempDir, err := os.MkdirTemp("", "bom-builder-nxp-chrome-")
	if err != nil {
		return nil, errors.New("could not create NXP browser profile")
	}
	childRead, parentWrite, err := os.Pipe()
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, errors.New("could not create NXP browser command pipe")
	}
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		childRead.Close()
		parentWrite.Close()
		os.RemoveAll(tempDir)
		return nil, errors.New("could not create NXP browser response pipe")
	}
	command := exec.Command(
		browserPath,
		"--headless=new",
		"--remote-debugging-pipe",
		"--user-data-dir="+tempDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--metrics-recording-only",
		"about:blank",
	)
	command.ExtraFiles = []*os.File{childRead, childWrite}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		childRead.Close()
		parentWrite.Close()
		parentRead.Close()
		childWrite.Close()
		os.RemoveAll(tempDir)
		return nil, errors.New("could not launch the NXP browser")
	}
	childRead.Close()
	childWrite.Close()
	process := &cdpProcess{
		command:   command,
		toBrowser: parentWrite,
		messages:  make(chan cdpRead, 1024),
		tempDir:   tempDir,
	}
	go process.readLoop(parentRead)

	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := process.callBrowser(ctx, "Target.createTarget", map[string]any{
		"url": "about:blank",
	}, &created); err != nil {
		process.Close()
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := process.callBrowser(ctx, "Target.attachToTarget", map[string]any{
		"targetId": created.TargetID,
		"flatten":  true,
	}, &attached); err != nil {
		process.Close()
		return nil, err
	}
	if attached.SessionID == "" {
		process.Close()
		return nil, errors.New("browser did not create an NXP page session")
	}
	process.sessionID = attached.SessionID
	return process, nil
}

func (process *cdpProcess) readLoop(fromBrowser *os.File) {
	defer fromBrowser.Close()
	scanner := bufio.NewScanner(fromBrowser)
	scanner.Buffer(make([]byte, 64*1024), maxCDPMessageBytes)
	scanner.Split(splitNullTerminated)
	for scanner.Scan() {
		var message cdpMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			process.messages <- cdpRead{err: errors.New("browser returned invalid CDP JSON")}
			return
		}
		process.messages <- cdpRead{message: message}
	}
	if err := scanner.Err(); err != nil {
		process.messages <- cdpRead{err: errors.New("browser CDP response exceeded limits")}
	} else {
		process.messages <- cdpRead{err: io.EOF}
	}
	close(process.messages)
}

func splitNullTerminated(data []byte, atEOF bool) (int, []byte, error) {
	for index, value := range data {
		if value == 0 {
			if index == 0 {
				return 1, nil, nil
			}
			return index + 1, data[:index], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (process *cdpProcess) callBrowser(
	ctx context.Context,
	method string,
	params any,
	destination any,
) error {
	return process.call(ctx, method, params, "", destination)
}

func (process *cdpProcess) callPage(
	ctx context.Context,
	method string,
	params any,
	destination any,
) error {
	return process.call(ctx, method, params, process.sessionID, destination)
}

func (process *cdpProcess) call(
	ctx context.Context,
	method string,
	params any,
	sessionID string,
	destination any,
) error {
	process.nextID++
	id := process.nextID
	request := map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}
	if sessionID != "" {
		request["sessionId"] = sessionID
	}
	data, err := json.Marshal(request)
	if err != nil {
		return errors.New("could not encode browser command")
	}
	data = append(data, 0)
	process.writeMu.Lock()
	_, err = process.toBrowser.Write(data)
	process.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: could not send browser command", errBrowserGone)
	}
	for {
		message, err := process.receive(ctx)
		if err != nil {
			return err
		}
		if message.ID != id {
			if message.Method != "" {
				process.pending = append(process.pending, message)
			}
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("browser command %s failed: %s", method, message.Error.Message)
		}
		if destination != nil && len(message.Result) > 0 {
			if err := json.Unmarshal(message.Result, destination); err != nil {
				return errors.New("browser command returned an invalid result")
			}
		}
		return nil
	}
}

func (process *cdpProcess) waitEvent(
	ctx context.Context,
	method string,
	predicate func(json.RawMessage) bool,
) (json.RawMessage, error) {
	for index := 0; index < len(process.pending); index++ {
		message := process.pending[index]
		if message.Method == method && (predicate == nil || predicate(message.Params)) {
			process.pending = append(process.pending[:index], process.pending[index+1:]...)
			return message.Params, nil
		}
	}
	for {
		message, err := process.receive(ctx)
		if err != nil {
			return nil, err
		}
		if message.Method == method && (predicate == nil || predicate(message.Params)) {
			return message.Params, nil
		}
		if message.Method != "" {
			process.pending = append(process.pending, message)
		}
	}
}

func (process *cdpProcess) receive(ctx context.Context) (cdpMessage, error) {
	select {
	case <-ctx.Done():
		return cdpMessage{}, ctx.Err()
	case received, open := <-process.messages:
		if !open {
			return cdpMessage{}, fmt.Errorf("%w: browser CDP connection closed", errBrowserGone)
		}
		if received.err != nil {
			// Every readLoop-fed error (invalid JSON, oversized
			// message, pipe EOF) precedes the channel closing: the
			// transport is unusable either way.
			return cdpMessage{}, fmt.Errorf("%w: %s", errBrowserGone, received.err)
		}
		return received.message, nil
	}
}

func (process *cdpProcess) clearEvents() {
	process.pending = nil
}

func (process *cdpProcess) Close() {
	process.closeOnce.Do(func() {
		if process.toBrowser != nil {
			process.toBrowser.Close()
		}
		if process.command != nil && process.command.Process != nil {
			process.command.Process.Kill()
			process.command.Wait()
		}
		if process.tempDir != "" {
			os.RemoveAll(process.tempDir)
		}
	})
}
