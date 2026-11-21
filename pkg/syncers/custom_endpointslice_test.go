package syncers

import (
	"testing"

	"github.com/loft-sh/vcluster/pkg/util/translate"
	corev1 "k8s.io/api/core/v1"
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
		{
			name:    "vcluster api endpointslice",
			eps:     NewTestEndpointSlice("kubernetes", "default", "kubernetes", ""),
			exclude: true,
		},
		{
			name:    "vcluster api endpointslice with generated name",
			eps:     NewTestEndpointSlice("kubernetes-abcde", "default", "kubernetes", ""),
			exclude: true,
		},
		{
			name:    "kubernetes service outside default namespace",
			eps:     NewTestEndpointSlice("kubernetes", "other", "kubernetes", "custom-operator"),
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

func TestShouldSkipVirtual(t *testing.T) {
	selectorService := &corev1.Service{
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "podinfo"}},
	}
	selectorlessService := &corev1.Service{
		Spec: corev1.ServiceSpec{Selector: nil},
	}

	missingLabel := NewTestEndpointSlice("manual", "preview-system", "podinfo", "custom-operator")
	delete(missingLabel.Labels, translate.K8sServiceNameLabel)

	tests := []struct {
		name string
		eps  *discoveryv1.EndpointSlice
		svc  *corev1.Service
		skip bool
	}{
		{
			// KubeElasti keeps the Service selector and adds its own slice for
			// resolver/proxy routing. The built-in syncer skips selector
			// Services, so the plugin must sync this one.
			name: "operator slice for selector service",
			eps:  NewTestEndpointSlice("elasti-podinfo", "preview-system", "podinfo", "elasti.truefoundry.com"),
			svc:  selectorService,
			skip: false,
		},
		{
			name: "operator slice for selector-less service",
			eps:  NewTestEndpointSlice("elasti-podinfo", "preview-system", "podinfo", "elasti.truefoundry.com"),
			svc:  selectorlessService,
			skip: false,
		},
		{
			name: "vcluster api endpointslice",
			eps:  NewTestEndpointSlice("kubernetes", "default", "kubernetes", ""),
			svc:  selectorlessService,
			skip: true,
		},
		{
			name: "controller managed",
			eps:  NewTestEndpointSlice("podinfo-abc", "preview-system", "podinfo", "endpointslice-controller.k8s.io"),
			svc:  selectorlessService,
			skip: true,
		},
		{
			name: "parent service not found",
			eps:  NewTestEndpointSlice("elasti-podinfo", "preview-system", "podinfo", "elasti.truefoundry.com"),
			svc:  nil,
			skip: true,
		},
		{
			name: "missing service name label",
			eps:  missingLabel,
			svc:  selectorlessService,
			skip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skip, _ := shouldSkipVirtual(tt.eps, tt.svc)
			if skip != tt.skip {
				t.Fatalf("shouldSkipVirtual() = %v, want %v", skip, tt.skip)
			}
		})
	}
}

func TestStaleHostSlice(t *testing.T) {
	owned := map[string]string{
		translate.NameAnnotation:      "elasti-podinfo",
		translate.NamespaceAnnotation: "preview-system",
	}

	tests := []struct {
		name          string
		annotations   map[string]string
		virtualExists bool
		skipReason    string
		stale         bool
	}{
		{
			name:        "unowned host slice",
			annotations: nil,
			stale:       false,
		},
		{
			name:        "missing namespace annotation",
			annotations: map[string]string{translate.NameAnnotation: "elasti-podinfo"},
			stale:       false,
		},
		{
			name:          "virtual source is gone",
			annotations:   owned,
			virtualExists: false,
			stale:         true,
		},
		{
			name:          "virtual source exists and is synced",
			annotations:   owned,
			virtualExists: true,
			stale:         false,
		},
		{
			name:          "virtual source exists but is now excluded",
			annotations:   owned,
			virtualExists: true,
			skipReason:    "parent service has a selector",
			stale:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stale, _ := staleHostSlice(tt.annotations, tt.virtualExists, tt.skipReason)
			if stale != tt.stale {
				t.Fatalf("staleHostSlice() = %v, want %v", stale, tt.stale)
			}
		})
	}
}
