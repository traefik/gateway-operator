package provisioner

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestResourceName(t *testing.T) {
	tests := []struct {
		name       string
		gwName     string
		wantPrefix string
	}{
		{name: "short name", gwName: "gw", wantPrefix: "traefik-app-gw-"},
		{name: "dots replaced", gwName: "gw.example.com", wantPrefix: "traefik-app-gw-example-com-"},
		{name: "long name truncated", gwName: strings.Repeat("a.", 126) + "a", wantPrefix: "traefik-app-" + strings.Repeat("a-", 21)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResourceName(&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: tt.gwName}})
			if errs := validation.IsDNS1035Label(got); len(errs) > 0 {
				t.Fatalf("invalid service name %q: %v", got, errs)
			}
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Fatalf("got %q, want prefix %q", got, tt.wantPrefix)
			}
		})
	}
}

func TestLabelName(t *testing.T) {
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
			got := LabelName(tt.value)
			if errs := validation.IsValidLabelValue(got); len(errs) > 0 {
				t.Fatalf("invalid label value %q: %v", got, errs)
			}
			if !strings.HasPrefix(got, tt.wantPrefix) || len(got) != tt.wantLen {
				t.Fatalf("got %q, want prefix %q and length %d", got, tt.wantPrefix, tt.wantLen)
			}
		})
	}

	if LabelName(long) == LabelName(long+"b") {
		t.Fatal("expected distinct long values to produce distinct label values")
	}
}
