package provisioner

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestResourceName(t *testing.T) {
	long := strings.Repeat("a", 253)

	tests := []struct {
		name string
		ns   string
		gw   string
		want string
	}{
		{name: "short name", ns: "app", gw: "gw", want: "traefik-app-gw-" + hashSuffix("app/gw")},
		{name: "long name truncated", ns: "app", gw: long, want: "traefik-app-" + strings.Repeat("a", 42) + "-" + hashSuffix("app/"+long)},
		{name: "dots replaced", ns: "app", gw: "my.gw", want: "traefik-app-my-gw-" + hashSuffix("app/my.gw")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResourceName(mkGateway(tt.ns, tt.gw))
			if errs := validation.IsDNS1035Label(got); len(errs) > 0 {
				t.Fatalf("invalid name %q: %v", got, errs)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}

	// Names sharing the truncated prefix, or the same base across a
	// namespace/name split, must stay distinct.
	if ResourceName(mkGateway("app", long)) == ResourceName(mkGateway("app", long+"b")) {
		t.Fatal("expected distinct long names to produce distinct resource names")
	}
	if ResourceName(mkGateway("a-b", "c")) == ResourceName(mkGateway("a", "b-c")) {
		t.Fatal("expected namespace/name splits to produce distinct resource names")
	}
	if ResourceName(mkGateway("app", "my.gw")) == ResourceName(mkGateway("app", "my-gw")) {
		t.Fatal("expected dotted and dashed names to produce distinct resource names")
	}
}

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
