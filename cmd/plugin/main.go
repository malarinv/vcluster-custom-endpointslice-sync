package main

import (
	"github.com/loft-sh/vcluster-sdk/plugin"
	"github.com/loft-sh/vcluster/pkg/mappings/resources"
	"github.com/malarinv/vcluster-custom-endpointslice-sync/pkg/syncers"
)

func main() {
	ctx := plugin.MustInitWithOptions(plugin.Options{
		RegisterMappings: []resources.BuildMapper{
			resources.CreateServiceMapper,
			resources.CreatePodsMapper,
			resources.CreateEndpointSlicesMapper,
		},
	})
	plugin.MustRegister(syncers.NewCustomEndpointSliceSyncer(ctx))
	plugin.MustStart()
}
