package store

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The PWA has to enforce the same limits this package validates: a row the client
// accepts and the server refuses is not a validation message to the user, it is a 400
// on push and a row quarantined on that one device. The numbers therefore live twice,
// and this test is what keeps the copies honest.
func TestClientLimitsMatchServer(t *testing.T) {
	source, err := os.ReadFile("../../web/src/lib/limits.ts")
	if err != nil {
		t.Fatalf("read client limits: %v", err)
	}

	expected := map[string]int{
		"maxNameLength":   maxNameLength,
		"maxTextLength":   maxTextLength,
		"maxTagsPerEntry": maxTagsPerEntry,
		"maxTagLength":    maxTagLength,
	}
	for name, want := range expected {
		pattern := regexp.MustCompile(`export const ` + name + ` = (\d+);`)
		match := pattern.FindSubmatch(source)
		if match == nil {
			t.Errorf("%s is not exported from web/src/lib/limits.ts", name)
			continue
		}
		got, err := strconv.Atoi(string(match[1]))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s: client has %d, server has %d - the client would produce rows the server refuses",
				name, got, want)
		}
	}
}
