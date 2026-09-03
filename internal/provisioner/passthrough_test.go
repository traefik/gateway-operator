package provisioner

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/gateway-operator/api/v1alpha1"
)

func listener(name string, port gwapiv1.PortNumber, proto gwapiv1.ProtocolType, host string) gwapiv1.Listener {
	l := gwapiv1.Listener{Name: gwapiv1.SectionName(name), Port: port, Protocol: proto}
	if host != "" {
		l.Hostname = new(gwapiv1.Hostname(host))
	}
	return l
}

func mkGateway(ns, name string, ls ...gwapiv1.Listener) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       gwapiv1.GatewaySpec{Listeners: ls},
	}
}

func hasArg(args []string, want string) bool {
	return slices.Contains(args, want)
}

func TestRender_Passthrough(t *testing.T) {
	proxy := &v1alpha1.TraefikProxy{
		Spec: v1alpha1.TraefikProxySpec{
			Autoscaling: &v1alpha1.AutoscalingConfig{
				MinReplicas:                    ptr.To[int32](2),
				MaxReplicas:                    5,
				TargetCPUUtilizationPercentage: ptr.To[int32](80),
			},
			PodDisruptionBudget: &v1alpha1.PodDisruptionBudgetConfig{
				MinAvailable: new(intstr.FromInt32(1)),
			},
			Logs:    &v1alpha1.LogsConfig{Level: "DEBUG"},
			Metrics: &v1alpha1.MetricsConfig{Prometheus: true},
			Tracing: &v1alpha1.TracingConfig{OTLPEndpoint: "http://otel:4318"},
		},
	}
	gw := mkGateway("app", "gw", listener("http", 80, gwapiv1.HTTPProtocolType, ""))

	set := Render(gw, proxy, "app")

	// Autoscaling: HPA present, Deployment replicas left to the HPA.
	if set.HPA == nil {
		t.Fatal("expected HPA")
	}
	if set.Deployment.Spec.Replicas != nil {
		t.Fatal("expected nil replicas when autoscaling is enabled")
	}
	if set.HPA.Spec.MaxReplicas != 5 || set.HPA.Spec.ScaleTargetRef.Name != set.Deployment.Name {
		t.Fatalf("unexpected HPA spec: %+v", set.HPA.Spec)
	}
	if len(set.HPA.Spec.Metrics) != 1 {
		t.Fatalf("expected one HPA metric, got %d", len(set.HPA.Spec.Metrics))
	}

	// PodDisruptionBudget present and selecting the data plane pods.
	if set.PodDisruptionBudget == nil {
		t.Fatal("expected PDB")
	}
	if set.PodDisruptionBudget.Spec.Selector == nil {
		t.Fatal("expected PDB selector")
	}

	args := set.Deployment.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{
		"--log.level=DEBUG",
		"--metrics.prometheus=true",
		"--metrics.prometheus.entryPoint=metrics",
		"--tracing.otlp=true",
		"--tracing.otlp.http.endpoint=http://otel:4318",
	} {
		if !hasArg(args, want) {
			t.Fatalf("missing arg %q in %v", want, args)
		}
	}

	// Metrics container port wired.
	var hasMetricsPort bool
	for _, p := range set.Deployment.Spec.Template.Spec.Containers[0].Ports {
		if p.Name == "metrics" && p.ContainerPort == 9100 {
			hasMetricsPort = true
		}
	}
	if !hasMetricsPort {
		t.Fatalf("expected metrics container port")
	}
}

func TestRender_NoPassthrough(t *testing.T) {
	gw := mkGateway("app", "gw", listener("http", 80, gwapiv1.HTTPProtocolType, ""))
	set := Render(gw, nil, "app")

	if set.HPA != nil || set.PodDisruptionBudget != nil {
		t.Fatal("did not expect HPA or PDB without configuration")
	}
	if set.Deployment.Spec.Replicas == nil {
		t.Fatal("expected a default replica count without autoscaling")
	}
	args := set.Deployment.Spec.Template.Spec.Containers[0].Args
	for _, unwanted := range []string{"--metrics.prometheus=true", "--tracing.otlp=true"} {
		if hasArg(args, unwanted) {
			t.Fatalf("did not expect arg %q", unwanted)
		}
	}
	if strings.Contains(strings.Join(args, " "), "--log.level") {
		t.Fatal("did not expect a log level arg")
	}
}
