package buildinfo

import "testing"

func TestCurrentReturnsLinkedValues(t *testing.T) {
	original := Current()
	defer func() {
		Version = original.Version
		Revision = original.Revision
		BuiltAt = original.BuiltAt
		Packaging = original.Packaging
	}()
	Version = "v1.2.3"
	Revision = "0123456789abcdef"
	BuiltAt = "2026-08-30T12:00:00Z"
	Packaging = "native"

	want := Info{
		Version:   "v1.2.3",
		Revision:  "0123456789abcdef",
		BuiltAt:   "2026-08-30T12:00:00Z",
		Packaging: "native",
	}
	if got := Current(); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}
