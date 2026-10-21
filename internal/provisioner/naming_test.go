package provisioner

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestLabelValue(t *testing.T) {
	long := strings.Repeat("a", 253)

	tests := []struct {
		name       string
		value      string
		wantPrefix string
		wantLen    int
	}{
		{name: "short value unchanged", value: "gw", wantPrefix: "gw", wantLen: 2},
		{name: "63 characters unchanged", value: strings.Repeat("a", 63), wantPrefix: strings.Repeat("a", 63), wantLen: 63},
		{name: "long value truncated with hash", value: long, wantPrefix: strings.Repeat("a", 54) + "-", wantLen: 63},
		{name: "trailing separators trimmed", value: strings.Repeat("a", 50) + "----" + long, wantPrefix: strings.Repeat("a", 50) + "-", wantLen: 59},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LabelValue(tt.value)
			if errs := validation.IsValidLabelValue(got); len(errs) > 0 {
				t.Fatalf("invalid label value %q: %v", got, errs)
			}
			if !strings.HasPrefix(got, tt.wantPrefix) || len(got) != tt.wantLen {
				t.Fatalf("got %q, want prefix %q and length %d", got, tt.wantPrefix, tt.wantLen)
			}
		})
	}

	if LabelValue(long) == LabelValue(long+"b") {
		t.Fatal("expected distinct long values to produce distinct label values")
	}
}
