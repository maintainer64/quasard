// SPDX-License-Identifier: GPL-3.0-only
package buildinfo

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	for _, value := range []string{Version, Commit, Date} {
		if !strings.Contains(String(), value) {
			t.Fatalf("missing %q in %q", value, String())
		}
	}
}
