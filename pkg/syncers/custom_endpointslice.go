package syncers

import (
	"errors"
	"fmt"
	"sync"

	"github.com/loft-sh/vcluster/pkg/mappings"
	"github.com/loft-sh/vcluster/pkg/patcher"
	"github.com/loft-sh/vcluster/pkg/syncer"
	"github.com/loft-sh/vcluster/pkg/syncer/synccontext"
	"github.com/loft-sh/vcluster/pkg/syncer/translator"
	synctypes "github.com/loft-sh/vcluster/pkg/syncer/types"
	"github.com/loft-sh/vcluster/pkg/util/translate"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
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

	// sweepOnce makes sure the orphaned host EndpointSlice sweep runs at most
	// once per process. The first reconcile is used as the trigger because
	// reconciles only start after the informer caches have been synced.
	sweepOnce sync.Once
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

var _ synctypes.Starter = &customEndpointSliceSyncer{}

// ReconcileStart decides whether an EndpointSlice should be synced at all.
// Besides the static exclusions it refuses to mirror the vcluster API server's
// own slice, and it cleans up host copies that the plugin created earlier but
// that are no longer allowed.
func (s *customEndpointSliceSyncer) ReconcileStart(ctx *synccontext.SyncContext, req ctrl.Request) (bool, error) {
	s.sweepOnce.Do(func() {
		if err := s.sweepOrphanedHostSlices(ctx); err != nil {
			ctx.Log.Infof("custom endpointslice orphan sweep failed: %v", err)
		}
	})

	if req.Name == "" {
		return false, nil
	}

	vObj := &discoveryv1.EndpointSlice{}
	err := ctx.VirtualClient.Get(ctx, req.NamespacedName, vObj)
	if err != nil {
		if kerrors.IsNotFound(err) {
			// Let the normal SyncToVirtual path delete the host copy.
			return false, nil
		}
		return true, err
	}

	skip, reason, err := s.shouldSkip(ctx, vObj)
	if err != nil {
		return true, err
	}
	if !skip {
		return false, nil
	}

	if err := s.deleteHostCopy(ctx, req.NamespacedName); err != nil {
		return true, err
	}
	ctx.Log.Infof("skip syncing endpointslice %s/%s: %s", vObj.GetNamespace(), vObj.GetName(), reason)
	return true, nil
}

func (s *customEndpointSliceSyncer) ReconcileEnd() {}

// shouldSkip resolves the parent Service and applies the sync policy. It is
// the context-aware counterpart of shouldSkipVirtual.
func (s *customEndpointSliceSyncer) shouldSkip(ctx *synccontext.SyncContext, vObj *discoveryv1.EndpointSlice) (bool, string, error) {
	var svc *corev1.Service
	serviceName := vObj.GetLabels()[translate.K8sServiceNameLabel]
	if serviceName != "" {
		svc = &corev1.Service{}
		err := ctx.VirtualClient.Get(ctx, types.NamespacedName{Namespace: vObj.GetNamespace(), Name: serviceName}, svc)
		if err != nil {
			if !kerrors.IsNotFound(err) {
				return true, "", err
			}
			svc = nil
		}
	}

	skip, reason := shouldSkipVirtual(vObj, svc)
	if skip {
		return true, reason, nil
	}

	// Defense in depth: never write endpoints for the host Service that fronts
	// the vcluster API server. The EndpointSlice mapper translates
	// default/kubernetes to exactly that Service name.
	if isVClusterAPIService(ctx, vObj) {
		return true, "belongs to the vcluster API service", nil
	}

	return false, "", nil
}

// shouldSkipVirtual is the pure decision function behind shouldSkip. A nil
// parent Service means the Service was not found in the virtual cluster.
func shouldSkipVirtual(vObj *discoveryv1.EndpointSlice, parentService *corev1.Service) (bool, string) {
	if ExcludeVirtualEndpointSlice(vObj) {
		return true, "excluded by EndpointSlice policy"
	}
	if vObj.GetLabels()[translate.K8sServiceNameLabel] == "" {
		return true, fmt.Sprintf("missing %s label", translate.K8sServiceNameLabel)
	}
	if parentService == nil {
		return true, "parent service not found"
	}
	return false, ""
}

func isVClusterAPIService(ctx *synccontext.SyncContext, vObj *discoveryv1.EndpointSlice) bool {
	serviceName := vObj.GetLabels()[translate.K8sServiceNameLabel]
	if serviceName == "" {
		return false
	}
	return translateServiceName(ctx, serviceName, vObj.GetNamespace()) == translate.VClusterName
}

// deleteHostCopy removes the host EndpointSlice created by this plugin, if it
// exists. Host objects owned by other controllers (no vcluster name annotation)
// are never touched.
func (s *customEndpointSliceSyncer) deleteHostCopy(ctx *synccontext.SyncContext, vReq types.NamespacedName) error {
	pObj := &discoveryv1.EndpointSlice{}
	err := ctx.HostClient.Get(ctx, s.VirtualToHost(ctx, vReq, nil), pObj)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if pObj.GetAnnotations()[translate.NameAnnotation] == "" {
		return nil
	}

	ctx.Log.Infof("delete host endpointslice %s/%s because it should no longer be synced", pObj.GetNamespace(), pObj.GetName())
	if err := ctx.HostClient.Delete(ctx, pObj); err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}

// sweepOrphanedHostSlices removes host EndpointSlices that this plugin created
// but that no longer have a valid virtual source. Deletion events are only
// observed while the plugin is running, so without this sweep a host copy can
// outlive its virtual object (for example when the plugin was down during the
// delete, or when the virtual object was recreated with a new UID).
func (s *customEndpointSliceSyncer) sweepOrphanedHostSlices(ctx *synccontext.SyncContext) error {
	hostSlices := &discoveryv1.EndpointSliceList{}
	if err := ctx.HostClient.List(ctx, hostSlices, client.InNamespace(ctx.CurrentNamespace)); err != nil {
		return fmt.Errorf("list host endpoint slices: %w", err)
	}

	for i := range hostSlices.Items {
		pObj := &hostSlices.Items[i]
		annotations := pObj.GetAnnotations()
		vName := annotations[translate.NameAnnotation]
		vNamespace := annotations[translate.NamespaceAnnotation]
		if vName == "" || vNamespace == "" {
			continue
		}

		vObj := &discoveryv1.EndpointSlice{}
		err := ctx.VirtualClient.Get(ctx, types.NamespacedName{Namespace: vNamespace, Name: vName}, vObj)
		virtualExists := err == nil
		if err != nil && !kerrors.IsNotFound(err) {
			return fmt.Errorf("get virtual endpointslice %s/%s: %w", vNamespace, vName, err)
		}

		skipReason := ""
		if virtualExists {
			skip, reason, err := s.shouldSkip(ctx, vObj)
			if err != nil {
				return err
			}
			if skip {
				skipReason = reason
			}
		}

		stale, reason := staleHostSlice(annotations, virtualExists, skipReason)
		if !stale {
			continue
		}

		ctx.Log.Infof("sweep: delete host endpointslice %s/%s: %s", pObj.GetNamespace(), pObj.GetName(), reason)
		if err := ctx.HostClient.Delete(ctx, pObj); err != nil && !kerrors.IsNotFound(err) {
			return fmt.Errorf("delete stale host endpointslice %s/%s: %w", pObj.GetNamespace(), pObj.GetName(), err)
		}
	}

	return nil
}

// staleHostSlice is the pure decision function behind the orphan sweep.
func staleHostSlice(annotations map[string]string, virtualExists bool, skipReason string) (bool, string) {
	vName := annotations[translate.NameAnnotation]
	vNamespace := annotations[translate.NamespaceAnnotation]
	if vName == "" || vNamespace == "" {
		return false, ""
	}
	if !virtualExists {
		return true, fmt.Sprintf("virtual endpointslice %s/%s is gone", vNamespace, vName)
	}
	if skipReason != "" {
		return true, skipReason
	}
	return false, ""
}

func (s *customEndpointSliceSyncer) SyncToHost(ctx *synccontext.SyncContext, event *synccontext.SyncToHostEvent[*discoveryv1.EndpointSlice]) (ctrl.Result, error) {
	if event.HostOld != nil {
		return patcher.DeleteVirtualObject(ctx, event.Virtual, event.HostOld, "host object was deleted")
	}

	// Defense in depth: ReconcileStart already filters this, but never create a
	// host EndpointSlice for the Service that carries the vcluster API.
	if isVClusterAPIService(ctx, event.Virtual) {
		ctx.Log.Infof("refuse to sync endpointslice %s/%s: it belongs to the vcluster API service", event.Virtual.GetNamespace(), event.Virtual.GetName())
		return ctrl.Result{}, nil
	}

	pObj := s.translate(ctx, event.Virtual)
	return patcher.CreateHostObject(ctx, event.Virtual, pObj, s.EventRecorder(), false)
}

func (s *customEndpointSliceSyncer) Sync(ctx *synccontext.SyncContext, event *synccontext.SyncEvent[*discoveryv1.EndpointSlice]) (_ ctrl.Result, retErr error) {
	patch, err := patcher.NewSyncerPatcher(ctx, event.Host, event.Virtual)
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

	// The vcluster API server's own EndpointSlice. vcluster core creates it
	// (k8sdefaultendpoint provider) with only the kubernetes.io/service-name
	// label, and the built-in syncer explicitly skips it. Mirroring it would
	// create a second EndpointSlice for the host Service that fronts the
	// vcluster, which can make kube-proxy treat that Service as endpoint-less
	// and reject all traffic to the vcluster API.
	if eps.GetNamespace() == "default" && (eps.GetName() == "kubernetes" || eps.GetLabels()[translate.K8sServiceNameLabel] == "kubernetes") {
		return true
	}

	switch eps.GetLabels()[discoveryv1.LabelManagedBy] {
	case "endpointslice-controller.k8s.io":
		// Standard k8s endpointslice controller — managed by Service, not operators.
		return true
	case "endpointslicemirroring-controller.k8s.io":
		// Mirrors a legacy Endpoints object to an EndpointSlice. These originate
		// from either a headless service or a replicated-from-host service (e.g.
		// OpenBao). Syncing them back to the host causes a create→GC→delete loop
		// because the host-side EndpointSlice lives in a different namespace than
		// the translated host object, so the host GC deletes it immediately.
		return true
	}
	return false
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
