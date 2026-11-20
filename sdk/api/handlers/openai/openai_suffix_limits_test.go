package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type suffixRequestInterceptorHost struct{}

func (suffixRequestInterceptorHost) HasRequestInterceptors() bool { return true }

func (suffixRequestInterceptorHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (suffixRequestInterceptorHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (suffixRequestInterceptorHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func (suffixRequestInterceptorHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

func TestSuffixLimitOmittedWithoutRouteEvidence(test *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("suffix-unknown", "codex", []*registry.ModelInfo{{ID: "unknown(max)", ContextLength: 500000, MaxCompletionTokens: 128000}})
	test.Cleanup(func() { registryRef.UnregisterClient("suffix-unknown") })
	for _, base := range []*handlers.BaseAPIHandler{
		handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil),
		handlers.NewBaseAPIHandlers(&config.SDKConfig{}, coreauth.NewManager(nil, nil, nil)),
	} {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		NewOpenAIAPIHandler(base).OpenAIModels(ctx)
		if recorder.Code != http.StatusOK {
			test.Fatalf("status = %d", recorder.Code)
		}
		found := false
		for _, model := range ojson.GetBytes(recorder.Body.Bytes(), "data").Array() {
			if model.Get("id").String() != "unknown(max)" {
				continue
			}
			found = true
			if model.Get("context_length").Exists() || model.Get("max_completion_tokens").Exists() {
				test.Fatalf("unproven suffix limits leaked: %s", model.Raw)
			}
		}
		if !found {
			test.Fatal("model missing")
		}
	}
}

func TestSuffixLimitOmittedWithPluginRoutingOrInterceptor(test *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("suffix-router", "codex", []*registry.ModelInfo{
		{ID: "router-model", ContextLength: 200000, MaxCompletionTokens: 32000},
		{ID: "router-model(max)", ContextLength: 200000, MaxCompletionTokens: 32000},
	})
	test.Cleanup(func() { registryRef.UnregisterClient("suffix-router") })
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "suffix-router", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}); err != nil {
		test.Fatal(err)
	}
	for _, configure := range []func(*handlers.BaseAPIHandler){
		func(base *handlers.BaseAPIHandler) { base.SetModelRouterHost(&responsesPluginRouteHost{}) },
		func(base *handlers.BaseAPIHandler) { base.SetPluginHost(suffixRequestInterceptorHost{}) },
	} {
		base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
		configure(base)
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		NewOpenAIAPIHandler(base).OpenAIModels(ctx)
		found := false
		for _, model := range ojson.GetBytes(recorder.Body.Bytes(), "data").Array() {
			if model.Get("id").String() != "router-model(max)" {
				continue
			}
			found = true
			if model.Get("context_length").Exists() || model.Get("max_completion_tokens").Exists() {
				test.Fatalf("plugin-affected limits leaked: %s", model.Raw)
			}
		}
		if !found {
			test.Fatal("model missing")
		}
	}
}
