// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package nxp

import (
	"bufio"
	"bytes"
	"testing"
)

func TestCDPPipeSplitUsesNullTerminators(t *testing.T) {
	t.Parallel()
	scanner := bufio.NewScanner(bytes.NewReader([]byte("{\"id\":1}\x00{\"id\":2}\x00")))
	scanner.Split(splitNullTerminated)
	var messages []string
	for scanner.Scan() {
		messages = append(messages, scanner.Text())
	}
	if len(messages) != 2 ||
		messages[0] != `{"id":1}` ||
		messages[1] != `{"id":2}` {
		t.Fatalf("messages = %#v", messages)
	}
}
