package pluginhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type observerMutationProbe struct {
	requestCalls   atomic.Int32
	responseCalls  atomic.Int32
	streamCalls    atomic.Int32
	unsafeMetadata atomic.Bool
}

func (probe *observerMutationProbe) mutateRequest(request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	probe.requestCalls.Add(1)
	if len(request.Body) > 0 {
		request.Body[0] = 'X'
	}
	request.Headers.Set("X-Test", "changed")
	if nested, ok := request.Metadata["nested"].(map[string]any); ok {
		nested["value"] = "changed"
	}
	if _, exists := request.Metadata["callback"]; exists {
		probe.unsafeMetadata.Store(true)
	}
	return pluginapi.RequestInterceptResponse{Body: []byte("replacement"), Headers: http.Header{"X-Test": {"replacement"}}, ClearHeaders: []string{"X-Test"}, Path: "/changed", Terminate: true, StatusCode: 200}, nil
}

func (probe *observerMutationProbe) InterceptRequestBeforeAuth(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return probe.mutateRequest(request)
}

func (probe *observerMutationProbe) InterceptRequestAfterAuth(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return probe.mutateRequest(request)
}

func (probe *observerMutationProbe) InterceptResponse(_ context.Context, response pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	probe.responseCalls.Add(1)
	if len(response.Body) > 0 {
		response.Body[0] = 'X'
	}
	response.ResponseHeaders.Set("X-Test", "changed")
	return pluginapi.ResponseInterceptResponse{Body: []byte("replacement"), Headers: http.Header{"X-Test": {"replacement"}}}, nil
}

func (probe *observerMutationProbe) InterceptStreamChunk(_ context.Context, response pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	probe.streamCalls.Add(1)
	if len(response.Body) > 0 {
		response.Body[0] = 'X'
	}
	response.ResponseHeaders.Set("X-Test", "changed")
	return pluginapi.StreamChunkInterceptResponse{Body: []byte("replacement"), Headers: http.Header{"X-Test": {"replacement"}}, DropChunk: true}, nil
}

func TestObserverModeIsolatesInputsAndDiscardsInterceptorResults(test *testing.T) {
	probe := &observerMutationProbe{}
	plugin := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: probe, ResponseInterceptor: probe, StreamChunkInterceptor: probe}}
	host := newHostWithRecords(capabilityRecord{id: "observer", observeOnly: true, plugin: plugin})
	body := []byte("original")
	headers := http.Header{"X-Test": {"original"}}
	nested := map[string]any{"value": "original"}
	metadata := map[string]any{"nested": nested, "callback": func() {}}
	request := pluginapi.RequestInterceptRequest{Body: body, Headers: headers, Metadata: metadata}
	for _, result := range []pluginapi.RequestInterceptResponse{host.InterceptRequestBeforeAuth(context.Background(), request), host.InterceptRequestAfterAuth(context.Background(), request)} {
		if len(result.Body) != 0 || result.Path != "" || result.Terminate || result.Headers.Get("X-Test") != "original" {
			test.Fatalf("observer changed request: %+v", result)
		}
	}
	if string(body) != "original" || headers.Get("X-Test") != "original" || nested["value"] != "original" || probe.unsafeMetadata.Load() || probe.requestCalls.Load() != 2 {
		test.Fatal("observer received shared input or was not invoked")
	}
	response := host.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{Body: body, ResponseHeaders: headers, Metadata: metadata})
	if string(response.Body) != "original" || response.Headers.Get("X-Test") != "original" || probe.responseCalls.Load() != 1 {
		test.Fatalf("observer changed response: %+v", response)
	}
	stream := host.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{Body: body, ResponseHeaders: headers, Metadata: metadata})
	if string(stream.Body) != "original" || stream.Headers.Get("X-Test") != "original" || stream.DropChunk || probe.streamCalls.Load() != 1 {
		test.Fatalf("observer changed stream: %+v", stream)
	}
}

func TestObserverModeIsolatesContextValues(test *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginContext.Request.Header.Set("X-Test", "original")
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	requestContext, cancel := base.GetContextWithCancel(nil, ginContext, context.Background())
	defer cancel()
	var callbackContext context.Context
	interceptor := requestInterceptorFunc(func(ctx context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		callbackContext = ctx
		if shared, ok := ctx.Value("gin").(*gin.Context); ok && shared != nil {
			shared.Request.Header.Set("X-Test", "changed")
		}
		return pluginapi.RequestInterceptResponse{}, nil
	})
	completion := make(chan bool, 1)
	lifecycle := requestLifecyclePluginFunc(func(ctx context.Context, event pluginapi.RequestCompletion) {
		_, leaked := ctx.Value("gin").(*gin.Context)
		if nested, ok := event.Metadata["nested"].(map[string]any); ok {
			nested["value"] = "changed"
		}
		if callback, ok := event.Metadata["callback"].(func()); ok {
			callback()
		}
		completion <- leaked
	})
	plugin := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: interceptor, RequestLifecyclePlugin: lifecycle}}
	host := newHostWithRecords(capabilityRecord{id: "context-observer", observeOnly: true, plugin: plugin})
	host.InterceptRequestBeforeAuth(requestContext, pluginapi.RequestInterceptRequest{})
	if callbackContext == nil || callbackContext.Done() != requestContext.Done() {
		test.Fatal("observer did not receive the original cancellation signal")
	}
	nested := map[string]any{"value": "original"}
	var callbackCalled atomic.Bool
	host.CompleteRequest(requestContext, pluginapi.RequestCompletion{Metadata: map[string]any{"nested": nested, "callback": func() { callbackCalled.Store(true) }}})
	if <-completion {
		test.Error("observer lifecycle received the original Gin context")
	}
	if nested["value"] != "original" || callbackCalled.Load() {
		test.Error("observer lifecycle received shared or unsafe metadata")
	}
	if ginContext.Request.Header.Get("X-Test") != "original" {
		test.Error("observer modified the original Gin request through context")
	}
	cancel()
	if callbackContext.Err() != context.Canceled {
		test.Fatalf("observer cancellation = %v", callbackContext.Err())
	}
}

func TestObserverModeIsolatesResponseAndWebSocketContexts(test *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginContext.Request.Header.Set("X-Test", "original")
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	requestContext, cancel := base.GetContextWithCancel(nil, ginContext, context.Background())
	defer cancel()
	var calls atomic.Int32
	mutate := func(ctx context.Context) {
		calls.Add(1)
		if shared, ok := ctx.Value("gin").(*gin.Context); ok && shared != nil {
			shared.Request.Header.Set("X-Test", "changed")
		}
	}
	interceptor := responseInterceptorFunc{
		interceptResponse: func(ctx context.Context, response pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
			mutate(ctx)
			return pluginapi.ResponseInterceptResponse{}, nil
		},
		interceptStreamChunk: func(ctx context.Context, response pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
			mutate(ctx)
			return pluginapi.StreamChunkInterceptResponse{}, nil
		},
	}
	websocket := testWebSocketObserverFunc(func(ctx context.Context, event pluginapi.WebSocketResponseEvent) error {
		mutate(ctx)
		if callback, ok := event.Metadata["callback"].(func()); ok {
			callback()
		}
		return nil
	})
	plugin := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ResponseInterceptor: interceptor, StreamChunkInterceptor: interceptor, WebSocketResponseObserver: websocket}}
	host := newHostWithRecords(capabilityRecord{id: "response-observer", observeOnly: true, plugin: plugin})
	var callbackCalled atomic.Bool
	host.InterceptResponse(requestContext, pluginapi.ResponseInterceptRequest{})
	host.InterceptStreamChunk(requestContext, pluginapi.StreamChunkInterceptRequest{})
	host.ObserveWebSocketResponseEvent(requestContext, pluginapi.WebSocketResponseEvent{Metadata: map[string]any{"callback": func() { callbackCalled.Store(true) }}})
	if ginContext.Request.Header.Get("X-Test") != "original" || callbackCalled.Load() || calls.Load() != 3 {
		test.Fatal("observer callback received shared context or unsafe metadata")
	}
}

func TestObserverModeDiscardsBodyPatcherEdits(test *testing.T) {
	patcher := funcRequestBodyPatcher(func(_ context.Context, body []byte) (pluginapi.RequestBodyPatchResponse, error) {
		body[10] = 'X'
		return pluginapi.RequestBodyPatchResponse{Edits: []pluginapi.RequestBodyEdit{{Start: 10, End: 18, Replacement: "changed"}}}, nil
	})
	plugin := pluginapi.Plugin{SchemaVersion: pluginabi.SchemaVersionRequestInterceptorBodyPatch, Capabilities: pluginapi.Capabilities{RequestBodyPatcherBefore: patcher}}
	host := newHostWithRecords(capabilityRecord{id: "observer-patcher", observeOnly: true, plugin: plugin})
	body := []byte(`{"model":"original"}`)
	result := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: body})
	if string(body) != `{"model":"original"}` || len(result.Body) != 0 {
		test.Fatalf("observer patcher changed body: %q / %q", body, result.Body)
	}
}

func TestObserverModeDiscardsInterceptorEdits(test *testing.T) {
	for _, useAlias := range []bool{false, true} {
		interceptor := requestInterceptorFunc(func(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
			edit := pluginapi.RequestBodyEdit{Start: 10, End: 18, Replacement: "changed"}
			if useAlias {
				return pluginapi.RequestInterceptResponse{Edits: []pluginapi.RequestBodyEdit{edit}}, nil
			}
			return pluginapi.RequestInterceptResponse{BodyEdits: []pluginapi.RequestBodyEdit{edit}}, nil
		})
		host := newHostWithRecords(capabilityRecord{id: "observer-edits", observeOnly: true, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: interceptor}}})
		body := []byte(`{"model":"original"}`)
		result := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: body})
		if string(body) != `{"model":"original"}` || len(result.Body) != 0 {
			test.Fatalf("observer edits changed request: %q / %q", body, result.Body)
		}
	}
}

func TestObserverModeReloadDoesNotApplyOldMutations(test *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var callbackInvoked atomic.Bool
	interceptor := requestInterceptorFunc(func(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		close(entered)
		<-release
		if callback, ok := request.Metadata["callback"].(func()); ok {
			callback()
		}
		return pluginapi.RequestInterceptResponse{Body: []byte("changed")}, nil
	})
	plugin := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: interceptor}}
	oldRecord := capabilityRecord{id: "reload-observer", plugin: plugin}
	host := newHostWithRecords(oldRecord)
	result := make(chan pluginapi.RequestInterceptResponse, 1)
	go func() {
		request := pluginapi.RequestInterceptRequest{Body: []byte("original"), Metadata: map[string]any{"callback": func() { callbackInvoked.Store(true) }}}
		result <- host.InterceptRequestBeforeAuth(context.Background(), request)
	}()
	<-entered
	setHostSnapshotForTest(host, true, capabilityRecord{id: "reload-observer", observeOnly: true, plugin: plugin})
	close(release)
	if got := <-result; len(got.Body) != 0 {
		test.Fatalf("old mutation survived observer transition: %q", got.Body)
	}
	if !callbackInvoked.Load() {
		test.Fatal("old unconstrained callback did not exercise shared metadata")
	}
}

func TestObserverModeDefaultRetainsMutations(test *testing.T) {
	interceptor := requestInterceptorFunc(func(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return pluginapi.RequestInterceptResponse{Body: []byte("changed")}, nil
	})
	host := newHostWithRecords(capabilityRecord{id: "ordinary", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: interceptor}}})
	result := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("original")})
	if string(result.Body) != "changed" {
		test.Fatalf("ordinary interceptor result = %q", result.Body)
	}
}

func TestObserverModeReloadThroughRuntimeConfig(test *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{registerResult: validTestPlugin("observer"), reconfigureResult: validTestPlugin("observer")}
	interceptor := requestInterceptorFunc(func(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return pluginapi.RequestInterceptResponse{Body: []byte("changed")}, nil
	})
	plugin.registerResult.Capabilities.RequestInterceptor = interceptor
	plugin.reconfigureResult.Capabilities.RequestInterceptor = interceptor
	plugin.registerResult.Capabilities.UsagePlugin = nil
	plugin.reconfigureResult.Capabilities.UsagePlugin = nil
	loader.lookups["observer"] = newTestSymbolLookup(plugin)
	host := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(test, "observer"), Configs: enabledPluginConfigs("observer")}}
	test.Cleanup(func() { host.ApplyConfig(context.Background(), &config.Config{}) })
	host.ApplyConfig(context.Background(), cfg)
	if got := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("original")}); string(got.Body) != "changed" {
		test.Fatalf("default mode did not retain interceptor behavior: %q", got.Body)
	}
	updated := cfg.CloneForRuntime()
	updated.Plugins.ObserveOnlyInterceptors = []string{"observer"}
	host.ApplyConfig(context.Background(), updated)
	if got := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("original")}); len(got.Body) != 0 {
		test.Fatalf("configured observer changed request: %q", got.Body)
	}
	host.ApplyConfig(context.Background(), cfg)
	if got := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("original")}); string(got.Body) != "changed" {
		test.Fatalf("ordinary mode did not restore behavior: %q", got.Body)
	}
}

func TestObserverModeConfigIsExplicit(test *testing.T) {
	enabled := true
	cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, ObserveOnlyInterceptors: []string{" observer "}, Configs: map[string]config.PluginInstanceConfig{
		"observer": {Enabled: &enabled},
		"ordinary": {Enabled: &enabled},
	}}}
	runtimeConfig, err := runtimeConfigFromConfig(cfg)
	if err != nil {
		test.Fatal(err)
	}
	if !runtimeConfig.Items["observer"].ObserveOnly || runtimeConfig.Items["ordinary"].ObserveOnly {
		test.Fatalf("observer config = %+v", runtimeConfig.Items)
	}
}

func TestObserverModeConfigRoundTripsThroughYAML(test *testing.T) {
	raw := []byte("server:\n  port: 8317\nplugins:\n  enabled: true\n  observe-only-interceptors: [observer]\n  configs:\n    observer:\n      enabled: true\n    ordinary:\n      enabled: true\n")
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		test.Fatal(err)
	}
	configPath := filepath.Join(test.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, raw, 0600); errWrite != nil {
		test.Fatal(errWrite)
	}
	if errSave := config.SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		test.Fatal(errSave)
	}
	reloaded, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		test.Fatal(errLoad)
	}
	runtimeConfig, errRuntime := runtimeConfigFromConfig(reloaded.CloneForRuntime())
	if errRuntime != nil {
		test.Fatal(errRuntime)
	}
	if !runtimeConfig.Items["observer"].ObserveOnly || runtimeConfig.Items["ordinary"].ObserveOnly {
		test.Fatalf("round-trip observer config = %+v", runtimeConfig.Items)
	}
}
