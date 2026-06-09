package syncers

import (
	"testing"

	"github.com/loft-sh/vcluster/pkg/util/translate"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExcludeVirtualEndpointSlice(t *testing.T) {
	tests := []struct {
		name    string
		eps     *discoveryv1.EndpointSlice
		exclude bool
	}{
		{
			name:    "nil",
			eps:     nil,
			exclude: true,
		},
		{
			name:    "controller managed",
			eps:     NewTestEndpointSlice("podinfo-abc", "preview-system", "podinfo", "endpointslice-controller.k8s.io"),
			exclude: true,
		},
		{
			name:    "custom operator managed",
			eps:     NewTestEndpointSlice("elasti-podinfo", "preview-system", "podinfo", "elasti.truefoundry.com"),
			exclude: false,
		},
		{
			name:    "missing managed by label",
			eps:     NewTestEndpointSlice("manual", "preview-system", "podinfo", ""),
			exclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExcludeVirtualEndpointSlice(tt.eps); got != tt.exclude {
				t.Fatalf("ExcludeVirtualEndpointSlice() = %v, want %v", got, tt.exclude)
			}
		})
	}
}

func TestTranslateEndpointSliceServiceNameLabel(t *testing.T) {
	eps := NewTestEndpointSlice("elasti-podinfo", "preview-system", "podinfo", "custom-operator")

	TranslateEndpointSlice(eps, "podinfo-x-preview-system-x-whiteblossom-pr-13-env")

	if got := eps.Labels[translate.K8sServiceNameLabel]; got != "podinfo-x-preview-system-x-whiteblossom-pr-13-env" {
		t.Fatalf("service label = %q, want translated host service name", got)
	}
}

func TestTranslateEndpointSlicePreservesExistingLabels(t *testing.T) {
	eps := NewTestEndpointSlice("elasti-podinfo", "preview-system", "podinfo", "custom-operator")
	eps.Labels["app.kubernetes.io/name"] = "podinfo"

	TranslateEndpointSlice(eps, "podinfo-x-preview-system-x-whiteblossom-pr-13-env")

	if got := eps.Labels["app.kubernetes.io/name"]; got != "podinfo" {
		t.Fatalf("existing label = %q, want preserved", got)
	}
}

func TestExcludePhysicalEndpointSlice(t *testing.T) {
	tests := []struct {
		name    string
		obj     *discoveryv1.EndpointSlice
		exclude bool
	}{
		{
			name:    "nil",
			obj:     nil,
			exclude: true,
		},
		{
			name: "unowned host object",
			obj: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "whiteblossom-pr-13-env"},
			},
			exclude: true,
		},
		{
			name: "owned host object",
			obj: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "elasti-podinfo-x-preview-system-x-whiteblossom-pr-13-env",
					Namespace: "whiteblossom-pr-13-env",
					Annotations: map[string]string{
						translate.NameAnnotation: "elasti-podinfo",
					},
				},
			},
			exclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExcludePhysicalEndpointSlice(tt.obj); got != tt.exclude {
				t.Fatalf("ExcludePhysicalEndpointSlice() = %v, want %v", got, tt.exclude)
			}
		})
	}
}
