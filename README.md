# vcluster Custom EndpointSlice Sync

This vcluster plugin syncs custom, operator-created `EndpointSlice` objects from a virtual cluster to the host cluster.

vcluster already syncs Kubernetes controller-managed EndpointSlices. It skips slices for Services with selectors because the built-in syncer assumes Kubernetes owns them. Operators such as KubeElasti can create their own EndpointSlices for resolver/proxy routing, and those slices need to exist in the host cluster so host kube-proxy can route traffic.

## Behavior

- Watches `discovery.k8s.io/v1 EndpointSlice`.
- Skips slices managed by `endpointslice-controller.k8s.io`.
- Syncs custom/operator-created slices to the host cluster.
- Rewrites `kubernetes.io/service-name` to the translated host Service name.
- Copies `addressType`, `ports`, and `endpoints`.
- Translates Pod `targetRef` names when present.
- Deletes the host copy when the virtual slice is deleted.

## Image

Images are published to:

```text
ghcr.io/malarinv/vcluster-custom-endpointslice-sync
```

Tags:

- `latest` for `main`
- `sha-<shortsha>` for every build
- `vX.Y.Z` for release tags

## vcluster values

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

## Development

```bash
go test ./...
go vet ./...
go build -o bin/plugin ./cmd/plugin
docker build -t ghcr.io/malarinv/vcluster-custom-endpointslice-sync:dev .
```
