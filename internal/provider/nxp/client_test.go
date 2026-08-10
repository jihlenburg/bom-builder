// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package nxp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBrowser drives a real cdpProcess over pipes with a scripted handler, so
// Client behavior — disable policy, recovery, event ordering — is tested
// against the exact transport code paths without launching a browser.
type fakeBrowser struct {
	t       *testing.T
	process *cdpProcess
	handle  func(browser *fakeBrowser, method string, id int, params json.RawMessage)

	navigations atomic.Int32
	dead        atomic.Bool
}

func newFakeBrowser(
	t *testing.T,
	handle func(browser *fakeBrowser, method string, id int, params json.RawMessage),
) (*cdpProcess, *fakeBrowser) {
	t.Helper()
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &cdpProcess{
		toBrowser: commandWrite,
		messages:  make(chan cdpRead, 1024),
		sessionID: "fake-session",
	}
	browser := &fakeBrowser{t: t, process: process, handle: handle}
	go func() {
		defer commandRead.Close()
		scanner := bufio.NewScanner(commandRead)
		scanner.Buffer(make([]byte, 64*1024), maxCDPMessageBytes)
		scanner.Split(splitNullTerminated)
		for scanner.Scan() {
			var message cdpMessage
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				return
			}
			if message.Method == "Page.navigate" {
				browser.navigations.Add(1)
			}
			handle(browser, message.Method, message.ID, message.Params)
		}
	}()
	t.Cleanup(process.Close)
	return process, browser
}

// send parses one raw CDP JSON message and delivers it to the client.
func (browser *fakeBrowser) send(raw string) {
	if browser.dead.Load() {
		return
	}
	var message cdpMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		browser.t.Errorf("invalid fake CDP message %s: %v", raw, err)
		return
	}
	browser.process.messages <- cdpRead{message: message}
}

func (browser *fakeBrowser) reply(id int, result string) {
	browser.send(fmt.Sprintf(`{"id":%d,"result":%s}`, id, result))
}

func (browser *fakeBrowser) replyError(id int, message string) {
	browser.send(fmt.Sprintf(`{"id":%d,"error":{"code":-32000,"message":%q}}`, id, message))
}

func (browser *fakeBrowser) event(method, params string) {
	browser.send(fmt.Sprintf(`{"method":%q,"params":%s}`, method, params))
}

// die simulates the browser process crashing: the message channel closes the
// way readLoop closes it when the pipe from the browser ends.
func (browser *fakeBrowser) die() {
	if browser.dead.CompareAndSwap(false, true) {
		close(browser.process.messages)
	}
}

func fakeClient(process *cdpProcess, timeout time.Duration) *Client {
	return &Client{
		browserPath:   "/nonexistent-browser-for-tests",
		currency:      "USD",
		searchBaseURL: defaultSearchBaseURL,
		partBaseURL:   defaultPartBaseURL,
		timeout:       timeout,
		process:       process,
	}
}

const fakeResponseReceived = `{"requestId":"req-1","response":` +
	`{"url":"https://www.nxp.com/webapp-rest/api/search/getAsset/allResults/x","status":200}}`

func fakeStoreBodyResult(t *testing.T, storeJSON string) string {
	t.Helper()
	quoted, err := json.Marshal(storeJSON)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"body":%s,"base64Encoded":false}`, quoted)
}

const validStorePayload = `{"results":[{"metaData":{` +
	`"part_id":"ABC123","Order":["Buy Direct"],"stepPrice":["1::x::0.123456"]}}]}`

func TestSearchTransientTimeoutDoesNotDisableClient(t *testing.T) {
	t.Parallel()
	// One slow or dropped page must fail only that lookup. Permanently
	// disabling the client on a transient timeout silently kills direct
	// NXP pricing for every remaining BOM line in the run.
	process, _ := newFakeBrowser(t, func(browser *fakeBrowser, method string, id int, _ json.RawMessage) {
		// Answer commands but never emit the search response event.
		browser.reply(id, `{}`)
	})
	client := fakeClient(process, 300*time.Millisecond)
	if _, err := client.Search(context.Background(), "ABC123"); err == nil {
		t.Fatal("Search unexpectedly succeeded without a response event")
	}
	if err := client.disabledError(); err != nil {
		t.Fatalf("transient timeout disabled the client: %v", err)
	}
}

func TestSearchSchemaDriftDisablesClientForTheRun(t *testing.T) {
	t.Parallel()
	// A response that no longer matches the expected store payload is
	// evidence the site changed; the client must fail closed for the rest
	// of the run — for PartDetail too — without issuing more navigations.
	process, browser := newFakeBrowser(t, func(browser *fakeBrowser, method string, id int, _ json.RawMessage) {
		switch method {
		case "Page.navigate":
			browser.reply(id, `{}`)
			browser.event("Network.responseReceived", fakeResponseReceived)
			browser.event("Network.loadingFinished", `{"requestId":"req-1"}`)
		case "Network.getResponseBody":
			browser.reply(id, fakeStoreBodyResult(browser.t, `{}`))
		default:
			browser.reply(id, `{}`)
		}
	})
	client := fakeClient(process, time.Second)
	if _, err := client.Search(context.Background(), "ABC123"); err == nil {
		t.Fatal("Search unexpectedly accepted a drifted payload")
	}
	if err := client.disabledError(); err == nil {
		t.Fatal("schema drift did not disable the client")
	}
	if _, err := client.Search(context.Background(), "ABC123"); err == nil {
		t.Fatal("second Search unexpectedly succeeded while disabled")
	}
	if _, err := client.PartDetail(context.Background(), "ABC123", "ABC123"); err == nil {
		t.Fatal("PartDetail unexpectedly succeeded while disabled")
	}
	if count := browser.navigations.Load(); count != 1 {
		t.Fatalf("disabled client issued %d navigations, want 1", count)
	}
}

func TestSearchDropsDeadBrowserProcessForRelaunch(t *testing.T) {
	t.Parallel()
	// When the browser dies mid-operation the stored process is useless;
	// keeping it bricks the client for its lifetime. Dropping it lets the
	// next lookup launch a fresh browser.
	process, _ := newFakeBrowser(t, func(browser *fakeBrowser, method string, _ int, _ json.RawMessage) {
		browser.die()
	})
	client := fakeClient(process, time.Second)
	if _, err := client.Search(context.Background(), "ABC123"); err == nil {
		t.Fatal("Search unexpectedly succeeded on a dead browser")
	}
	if err := client.disabledError(); err != nil {
		t.Fatalf("browser death disabled the client: %v", err)
	}
	client.mu.Lock()
	stale := client.process
	client.mu.Unlock()
	if stale != nil {
		t.Fatal("dead browser process was kept; next lookup cannot relaunch")
	}
}

func TestSearchWaitsForLoadingFinishedBeforeBodyFetch(t *testing.T) {
	t.Parallel()
	// CDP only guarantees the response body after Network.loadingFinished;
	// fetching on responseReceived races Chrome and yields "No data found
	// for resource" on slower responses.
	var loadingFinished atomic.Bool
	process, _ := newFakeBrowser(t, func(browser *fakeBrowser, method string, id int, _ json.RawMessage) {
		switch method {
		case "Page.navigate":
			browser.reply(id, `{}`)
			browser.event("Network.responseReceived", fakeResponseReceived)
			go func() {
				time.Sleep(50 * time.Millisecond)
				loadingFinished.Store(true)
				browser.event("Network.loadingFinished", `{"requestId":"req-1"}`)
			}()
		case "Network.getResponseBody":
			if !loadingFinished.Load() {
				browser.replyError(id, "No data found for resource with given identifier")
				return
			}
			browser.reply(id, fakeStoreBodyResult(browser.t, validStorePayload))
		default:
			browser.reply(id, `{}`)
		}
	})
	client := fakeClient(process, 2*time.Second)
	result, err := client.Search(context.Background(), "ABC123")
	if err != nil {
		t.Fatalf("Search raced the body fetch: %v", err)
	}
	if result == nil || result.PartID != "ABC123" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
