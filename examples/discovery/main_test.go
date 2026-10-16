// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT
package main

import "testing"

func TestOfflineDiscovery(t *testing.T) {
	if err := run(); err != nil {
		t.Fatal(err)
	}
}
