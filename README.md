# vcluster Custom EndpointSlice Sync

A vcluster plugin that mirrors **operator-created EndpointSlices** from a virtual
cluster into the host cluster, so host `kube-proxy` can actually route traffic to
them.

## Why this plugin exists

vcluster ships a built-in EndpointSlice syncer. Its behaviour is:

- Services **without** a selector: the built-in syncer copies their
  EndpointSlices to the host.
- Services **with** a selector: the built-in syncer assumes Kubernetes owns the
  EndpointSlices and **skips them entirely**.
- `default/kubernetes` and other special Services: always skipped.

That is a problem for operators that create their own EndpointSlices. KubeElasti,
for example, keeps the Service selector and adds a slice that points at its
resolver proxy so a scaled-to-zero workload can wake up on the first request.
Because the Service has a selector, vcluster never copies that slice to the host.
The host cluster then has no endpoint for the Service and traffic never reaches
the resolver.

This plugin closes that gap: it registers a second EndpointSlice syncer that
copies the slices the built-in syncer ignores.

## What it does

- Watches `discovery.k8s.io/v1` EndpointSlices in the virtual cluster.
- Copies operator-created slices to the host cluster.
- Rewrites the `kubernetes.io/service-name` label to the translated host Service
  name, so host `kube-proxy` associates the slice with the right Service.
- Copies `addressType`, `ports` and `endpoints`, and translates Pod `targetRef`
  names to their host names.
- Deletes the host copy when the virtual slice is deleted.
- Sweeps orphaned host copies on startup (see *Ownership and lifecycle*).

## What it deliberately does not do

These guards prevent the plugin from creating duplicate or conflicting
EndpointSlices, which can make host `kube-proxy` treat a Service as having no
endpoints:

| Slice | Behaviour | Why |
| :--- | :--- | :--- |
| `endpointslice.kubernetes.io/managed-by: endpointslice-controller.k8s.io` | Skipped | The built-in Kubernetes controller owns it. |
| `endpointslice.kubernetes.io/managed-by: endpointslicemirroring-controller.k8s.io` | Skipped | Mirrors a legacy `Endpoints` object; syncing it causes a create/GC loop. |
| `default/kubernetes` (and any slice labelled `kubernetes.io/service-name: kubernetes` in `default`) | Skipped | This is vcluster's own API endpoint. |
| Any slice whose parent Service translates to the host Service that fronts the vcluster | Skipped | Writing endpoints for that Service can cut every pod in the vcluster off from the API server. |
| Slice with no `kubernetes.io/service-name` label | Skipped | It cannot be associated with a Service. |
| Slice whose parent Service does not exist | Skipped | There is nothing to route to. |

The "vcluster API Service" guard is intentionally service-based rather than
name-based: the EndpointSlice mapper translates the virtual `default/kubernetes`
Service to exactly the host Service name (`<vcluster-name>`) that exposes the
vcluster API.

## Ownership and lifecycle

Host objects created by the plugin are annotated with vcluster's standard
ownership metadata, including:

```text
vcluster.loft.sh/object-name:      <virtual EndpointSlice name>
vcluster.loft.sh/object-namespace: <virtual namespace>
vcluster.loft.sh/object-uid:       <virtual UID>
```

Only host objects carrying `vcluster.loft.sh/object-name` are considered owned by
the plugin. Host EndpointSlices created by Kubernetes or by other controllers are
never modified or deleted.

Lifecycle rules:

1. When a virtual slice is deleted, the plugin deletes the matching host slice.
2. If the host slice is deleted manually, vcluster's standard syncer semantics
   apply: the virtual object is deleted as well.
3. On startup the plugin performs a one-time sweep. Any host slice annotated as
   plugin-owned is checked against the virtual cluster and deleted when:
   - the virtual slice no longer exists, or
   - the virtual slice is now excluded by the rules above.

The sweep matters because deletion events are only observed while the plugin is
running. A host copy can otherwise outlive its virtual source when the plugin was
down during the delete, or when the virtual object was recreated with a new UID.

## Why the guards are strict

A single EndpointSlice that is wrong for the host Service can take down the whole
vcluster. `kube-proxy` merges all EndpointSlices that reference a Service. If one
of them reports the Service's endpoint as `ready: false` (for example a copy made
while the vcluster pod was still starting), some nodes compute "no ready
endpoints" and install a `REJECT --reject-with icmp-port-unreachable` rule.

For the Service that fronts the vcluster, that means every pod in the virtual
cluster on that node loses access to the Kubernetes API. Controllers such as
CloudNativePG then crash-loop with `dial tcp <cluster-ip>:443: connect:
connection refused`, even though the vcluster itself is healthy.

vcluster's own syncer comments on the same hazard: multiple slices pointing at
the same endpoints can result in wrong DNS and cluster networking.

## Configuration

```yaml
plugins:
  custom-endpointslice-sync:
    image: ghcr.io/malarinv/vcluster-custom-endpointslice-sync:latest
    imagePullPolicy: IfNotPresent
    rbac:
      clusterRole:
        extraRules:
          - apiGroups: ["discovery.k8s.io"]
            resources: ["endpointslices"]
            verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
          - apiGroups: [""]
            resources: ["services"]
            verbs: ["get", "list", "watch"]
```

The plugin needs:

- full EndpointSlice access in the vcluster namespace (create/update/delete the
  host copies, list them for the orphan sweep),
- read access to Services in the virtual cluster (resolve the parent Service).

## Image

Images are published to:

```text
ghcr.io/malarinv/vcluster-custom-endpointslice-sync
```

Tags:

- `latest` for `main`
- `sha-<shortsha>` for every build
- `vX.Y.Z` for release tags

## Development

```bash
go test ./...
go vet ./...
go build -o bin/plugin ./cmd/plugin
docker build -t ghcr.io/malarinv/vcluster-custom-endpointslice-sync:dev .
```

If the checkout lives inside a Go workspace (`go.work`) that does not include
this module, prefix the Go commands with `GOWORK=off`.

### Verifying a deployment

Inside the virtual cluster:

```bash
kubectl get endpointslice -n <namespace> \
  -l kubernetes.io/service-name=<service> \
  -o custom-columns='NAME:.metadata.name,MANAGED-BY:.metadata.labels.endpointslice\.kubernetes\.io/managed-by'
```

On the host, the operator-created slice should exist in the vcluster namespace
with a translated name, the translated `kubernetes.io/service-name` label and the
`vcluster.loft.sh/object-*` annotations:

```bash
kubectl -n <vcluster-namespace> get endpointslice \
  -l kubernetes.io/service-name=<translated-service-name> -o yaml
```

The Service that fronts the vcluster must never have a plugin-owned slice. If one
appears, check the plugin version: slices for `default/kubernetes` are excluded
since the fix for the duplicate-API-endpoint incident.
