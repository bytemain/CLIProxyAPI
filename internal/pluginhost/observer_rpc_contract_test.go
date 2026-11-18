package pluginhost

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type observerRPCProbeClient struct {
	call func(context.Context, string, []byte) ([]byte, error)
}

func (client *observerRPCProbeClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	return client.call(ctx, method, request)
}

func (*observerRPCProbeClient) Shutdown() {}

func TestObserverRPCCallbackContextHidesParentValues(test *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), key, "synthetic-parent-value"))
	defer cancel()
	var host *Host
	var called atomic.Bool
	inner := &observerRPCProbeClient{call: func(ctx context.Context, method string, request []byte) ([]byte, error) {
		called.Store(true)
		callbackID := ojson.GetBytes(request, "host_callback_id").String()
		if callbackID == "" {
			test.Error("RPC callback ID missing")
		}
		restored := host.resolveCallbackContext(callbackID, context.Background())
		if ctx.Value(key) != nil || restored.Value(key) != nil {
			test.Error("observer RPC restored parent context values")
		}
		if ctx.Done() != parent.Done() || restored.Done() != parent.Done() {
			test.Error("observer RPC lost the cancellation signal")
		}
		return []byte(`{"ok":true,"result":{}}`), nil
	}}
	guarded := newGuardedPluginClient(inner)
	defer guarded.Shutdown()
	adapter := &rpcPluginAdapter{id: "observer-context-rpc", client: guarded, instance: guarded.callbackInstance()}
	plugin := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: adapter}}
	host = newHostWithRecords(capabilityRecord{id: adapter.id, observeOnly: true, plugin: plugin})
	adapter.host = host
	host.InterceptRequestBeforeAuth(parent, pluginapi.RequestInterceptRequest{})
	if !called.Load() {
		test.Fatal("synthetic RPC was not called")
	}
}

func TestObserverDetachedRPCCallbacksRemainGuarded(test *testing.T) {
	for _, phase := range []string{"websocket", "lifecycle"} {
		test.Run(phase, func(test *testing.T) {
			entered := make(chan bool, 1)
			release := make(chan struct{})
			inner := &observerRPCProbeClient{call: func(ctx context.Context, method string, request []byte) ([]byte, error) {
				entered <- ctx.Done() == nil
				<-release
				return []byte(`{"ok":true,"result":{}}`), nil
			}}
			guarded := newGuardedPluginClient(inner)
			var releaseOnce sync.Once
			defer func() {
				releaseOnce.Do(func() { close(release) })
				guarded.Shutdown()
			}()
			adapter := &rpcPluginAdapter{id: "observer-detached-rpc", client: guarded, instance: guarded.callbackInstance()}
			plugin := pluginapi.Plugin{}
			if phase == "websocket" {
				plugin.Capabilities.WebSocketResponseObserver = adapter
			} else {
				plugin.Capabilities.RequestLifecyclePlugin = adapter
			}
			host := newHostWithRecords(capabilityRecord{id: adapter.id, plugin: plugin})
			adapter.host = host
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan struct{})
			go func() {
				if phase == "websocket" {
					host.ObserveWebSocketResponseEvent(parent, pluginapi.WebSocketResponseEvent{})
				} else {
					host.CompleteRequest(parent, pluginapi.RequestCompletion{})
				}
				close(returned)
			}()
			if detached := <-entered; !detached {
				test.Fatal("public observer callback did not detach parent cancellation")
			}
			cancel()
			guarded.mu.Lock()
			workers := guarded.calls
			guarded.mu.Unlock()
			if workers != 1 {
				test.Fatalf("underlying workers = %d, want 1", workers)
			}
			if phase == "websocket" {
				select {
				case <-returned:
					test.Fatal("detached WebSocket callback ended before the worker")
				default:
				}
			}
			setHostSnapshotForTest(host, true, capabilityRecord{id: adapter.id, observeOnly: true, plugin: plugin})
			releaseOnce.Do(func() { close(release) })
			guarded.Shutdown()
			<-returned
			guarded.mu.Lock()
			workers = guarded.calls
			guarded.mu.Unlock()
			if workers != 0 {
				test.Fatalf("underlying workers = %d after shutdown, want 0", workers)
			}
		})
	}
}
