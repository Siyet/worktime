package update

import "testing"

func TestNativePackagingIsExactAndFailClosed(t *testing.T) {
	tests := []struct {
		packaging string
		supported bool
	}{
		{packaging: "native", supported: true},
		{packaging: "docker", supported: false},
		{packaging: "dev", supported: false},
		{packaging: "", supported: false},
		{packaging: "Native", supported: false},
		{packaging: "unknown", supported: false},
	}
	for _, test := range tests {
		t.Run(test.packaging, func(t *testing.T) {
			supported, reason := nativePackagingSupport(test.packaging)
			if supported != test.supported {
				t.Fatalf("packaging %q supported=%v, expected %v", test.packaging, supported, test.supported)
			}
			if !supported && reason == "" {
				t.Fatalf("packaging %q failed closed without operator guidance", test.packaging)
			}
		})
	}
}
