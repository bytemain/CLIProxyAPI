package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestObserverCancelledRPCRemainsUnconstrainedUntilInnerFinishes(test *testing.T) {
	for _, phase := range []string{"request-before", "request-after", "patch-before", "patch-after", "response", "stream"} {
		test.Run(phase, func(test *testing.T) {
			inner := &blockingGuardPluginClient{started: make(chan struct{}), release: make(chan struct{})}
			guarded := newGuardedPluginClient(inner)
			released := false
			defer func() {
				if !released {
					close(inner.release)
				}
				guarded.Shutdown()
			}()
			adapter := &rpcPluginAdapter{id: "cancelled-observer", client: guarded}
			plugin := pluginapi.Plugin{SchemaVersion: pluginabi.SchemaVersionRequestInterceptorBodyPatch}
			switch phase {
			case "request-before", "request-after":
				plugin.Capabilities.RequestInterceptor = adapter
			case "patch-before":
				plugin.Capabilities.RequestBodyPatcherBefore = rpcRequestBodyPatcher{rpcPluginAdapter: adapter, method: pluginabi.MethodRequestPatchBodyBefore}
			case "patch-after":
				plugin.Capabilities.RequestBodyPatcherAfter = rpcRequestBodyPatcher{rpcPluginAdapter: adapter, method: pluginabi.MethodRequestPatchBodyAfter}
			case "response":
				plugin.Capabilities.ResponseInterceptor = adapter
			case "stream":
				plugin.Capabilities.StreamChunkInterceptor = adapter
			}
			host := newHostWithRecords(capabilityRecord{id: "cancelled-observer", plugin: plugin})
			adapter.host = host
			requestContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan struct{})
			go func() {
				switch phase {
				case "request-before", "patch-before":
					host.InterceptRequestBeforeAuth(requestContext, pluginapi.RequestInterceptRequest{Body: []byte("{}")})
				case "request-after", "patch-after":
					host.InterceptRequestAfterAuth(requestContext, pluginapi.RequestInterceptRequest{Body: []byte("{}")})
				case "response":
					host.InterceptResponse(requestContext, pluginapi.ResponseInterceptRequest{})
				case "stream":
					host.InterceptStreamChunk(requestContext, pluginapi.StreamChunkInterceptRequest{})
				}
				close(returned)
			}()
			<-inner.started
			cancel()
			<-returned
			setHostSnapshotForTest(host, true, capabilityRecord{id: "cancelled-observer", observeOnly: true, plugin: plugin})
			if !host.HasUnconstrainedInterceptors() {
				test.Fatal("cancelled outer call released the guard before the inner plugin call finished")
			}
			close(inner.release)
			released = true
			guarded.Shutdown()
			if host.HasUnconstrainedInterceptors() {
				test.Fatal("guard remained active after the inner plugin call finished")
			}
		})
	}
}
