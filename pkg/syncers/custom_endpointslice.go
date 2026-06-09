package syncers

import (
	"errors"
	"fmt"

	"github.com/loft-sh/vcluster/pkg/mappings"
	"github.com/loft-sh/vcluster/pkg/patcher"
	"github.com/loft-sh/vcluster/pkg/syncer"
	"github.com/loft-sh/vcluster/pkg/syncer/synccontext"
	"github.com/loft-sh/vcluster/pkg/syncer/translator"
	synctypes "github.com/loft-sh/vcluster/pkg/syncer/types"
	"github.com/loft-sh/vcluster/pkg/util/translate"
	discoveryv1 "k8s.io/api/discovery/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	syncerName = "custom-endpointslice-syncer"
)

func NewCustomEndpointSliceSyncer(ctx *synccontext.RegisterContext) synctypes.Base {
	mapper, err := ctx.Mappings.ByGVK(mappings.EndpointSlices())
	if err != nil {
		klog.FromContext(ctx).Error(err, "unable to get mapper for EndpointSlice")
		return nil
	}

	return &customEndpointSliceSyncer{
		GenericTranslator: translator.NewGenericTranslator(ctx, "custom-endpointslice", &discoveryv1.EndpointSlice{}, mapper),
	}
}

type customEndpointSliceSyncer struct {
	synctypes.GenericTranslator
}

func (s *customEndpointSliceSyncer) Name() string {
	return syncerName
}

func (s *customEndpointSliceSyncer) Resource() client.Object {
	return &discoveryv1.EndpointSlice{}
}

var _ synctypes.OptionsProvider = &customEndpointSliceSyncer{}

func (s *customEndpointSliceSyncer) Options() *synctypes.Options {
	return &synctypes.Options{ObjectCaching: true}
}

var _ synctypes.Syncer = &customEndpointSliceSyncer{}

func (s *customEndpointSliceSyncer) Syncer() synctypes.Sync[client.Object] {
	return syncer.ToGenericSyncer(s)
}

var _ synctypes.ObjectExcluder = &customEndpointSliceSyncer{}

func (s *customEndpointSliceSyncer) ExcludeVirtual(vObj client.Object) bool {
	eps, ok := vObj.(*discoveryv1.EndpointSlice)
	if !ok {
		return true
	}
	return ExcludeVirtualEndpointSlice(eps)
}

func (s *customEndpointSliceSyncer) ExcludePhysical(pObj client.Object) bool {
	return ExcludePhysicalEndpointSlice(pObj)
}

func (s *customEndpointSliceSyncer) SyncToHost(ctx *synccontext.SyncContext, event *synccontext.SyncToHostEvent[*discoveryv1.EndpointSlice]) (ctrl.Result, error) {
	if event.HostOld != nil {
		return patcher.DeleteVirtualObject(ctx, event.Virtual, event.HostOld, "host object was deleted")
	}

	pObj := s.translate(ctx, event.Virtual)
	return patcher.CreateHostObject(ctx, event.Virtual, pObj, s.EventRecorder(), false)
}

func (s *customEndpointSliceSyncer) Sync(ctx *synccontext.SyncContext, event *synccontext.SyncEvent[*discoveryv1.EndpointSlice]) (_ ctrl.Result, retErr error) {
	patch, err := patcher.NewSyncerPatcher(ctx, event.Host, event.Virtual, nil)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("new syncer patcher: %w", err)
	}
	defer func() {
		if err := patch.Patch(ctx, event.Host, event.Virtual); err != nil {
			retErr = errors.Join(retErr, err)
		}
		if retErr != nil {
			s.EventRecorder().Eventf(event.Virtual, "Warning", "SyncError", "Error syncing custom EndpointSlice: %v", retErr)
		}
	}()

	desired := s.translate(ctx, event.Virtual)
	applyEndpointSliceSpec(event.Host, desired)
	if !equality.Semantic.DeepEqual(event.Host.GetLabels(), desired.GetLabels()) {
		event.Host.SetLabels(desired.GetLabels())
	}
	if !equality.Semantic.DeepEqual(event.Host.GetAnnotations(), desired.GetAnnotations()) {
		event.Host.SetAnnotations(desired.GetAnnotations())
	}

	return ctrl.Result{}, nil
}

func (s *customEndpointSliceSyncer) SyncToVirtual(ctx *synccontext.SyncContext, event *synccontext.SyncToVirtualEvent[*discoveryv1.EndpointSlice]) (ctrl.Result, error) {
	return patcher.DeleteHostObject(ctx, event.Host, event.VirtualOld, "virtual object was deleted")
}

func (s *customEndpointSliceSyncer) translate(ctx *synccontext.SyncContext, vObj *discoveryv1.EndpointSlice) *discoveryv1.EndpointSlice {
	pObj := translate.HostMetadata(
		vObj.DeepCopy(),
		s.VirtualToHost(ctx, types.NamespacedName{Name: vObj.GetName(), Namespace: vObj.GetNamespace()}, vObj),
	)

	serviceName := vObj.GetLabels()[translate.K8sServiceNameLabel]
	hostServiceName := serviceName
	if serviceName != "" {
		hostServiceName = translateServiceName(ctx, serviceName, vObj.GetNamespace())
	}

	TranslateEndpointSlice(pObj, hostServiceName)
	translateEndpointTargets(ctx, pObj)
	return pObj
}

func translateServiceName(ctx *synccontext.SyncContext, serviceName, namespace string) string {
	return mappings.VirtualToHost(ctx, serviceName, namespace, mappings.Services()).Name
}

func translateEndpointTargets(ctx *synccontext.SyncContext, endpointSlice *discoveryv1.EndpointSlice) {
	for i, endpoint := range endpointSlice.Endpoints {
		if endpoint.TargetRef == nil || endpoint.TargetRef.Kind != "Pod" {
			continue
		}

		namespace := endpoint.TargetRef.Namespace
		if namespace == "" {
			namespace = endpointSlice.GetAnnotations()[translate.NamespaceAnnotation]
		}
		if namespace == "" {
			namespace = endpointSlice.GetNamespace()
		}

		hostPod := mappings.VirtualToHost(ctx, endpoint.TargetRef.Name, namespace, mappings.Pods())
		endpointSlice.Endpoints[i].TargetRef.Name = hostPod.Name
		endpointSlice.Endpoints[i].TargetRef.Namespace = hostPod.Namespace
	}
}

func applyEndpointSliceSpec(host, desired *discoveryv1.EndpointSlice) {
	host.AddressType = desired.AddressType
	host.Ports = desired.Ports
	host.Endpoints = desired.Endpoints
}

func ExcludeVirtualEndpointSlice(eps *discoveryv1.EndpointSlice) bool {
	if eps == nil {
		return true
	}
	return eps.GetLabels()[discoveryv1.LabelManagedBy] == "endpointslice-controller.k8s.io"
}

func ExcludePhysicalEndpointSlice(obj client.Object) bool {
	if obj == nil {
		return true
	}
	// A typed-nil (*EndpointSlice)(nil) passed as client.Object is a non-nil
	// interface with a nil value; detect and exclude it.
	eps, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok || eps == nil {
		return true
	}
	annotations := eps.GetAnnotations()
	return annotations == nil || annotations[translate.NameAnnotation] == ""
}

func TranslateEndpointSlice(eps *discoveryv1.EndpointSlice, hostServiceName string) {
	if eps.Labels == nil {
		eps.Labels = map[string]string{}
	}
	if hostServiceName != "" {
		eps.Labels[translate.K8sServiceNameLabel] = hostServiceName
	}
}

func NewTestEndpointSlice(name, namespace, serviceName, managedBy string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1ObjectMeta(name, namespace, map[string]string{
			translate.K8sServiceNameLabel: serviceName,
			discoveryv1.LabelManagedBy:    managedBy,
		}, nil),
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses: []string{"10.42.0.10"},
			TargetRef: &corev1.ObjectReference{
				Kind:      "Pod",
				Name:      "podinfo-abc123",
				Namespace: namespace,
			},
		}},
	}
}
