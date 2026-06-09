package main

import (
	"github.com/loft-sh/vcluster-sdk/plugin"
	"github.com/malarinv/vcluster-custom-endpointslice-sync/pkg/syncers"
)

func main() {
	ctx := plugin.MustInit()
	plugin.MustRegister(syncers.NewCustomEndpointSliceSyncer(ctx))
	plugin.MustStart()
}
