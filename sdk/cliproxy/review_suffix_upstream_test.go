package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type reviewSuffixPluginRegistrar struct{}

func (reviewSuffixPluginRegistrar) RegisterModels(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	return pluginapi.ModelRegistrationResponse{Provider: "codex", Models: []pluginapi.ModelInfo{
		{ID: "review-plugin-up", ContextLength: 200000, MaxCompletionTokens: 32000},
	}}, nil
}

func TestSuffixListIncludesSelectablePluginUpstream(test *testing.T) {
	aliases := []config.OAuthModelAlias{
		{Name: "gpt-6.1-terra", Alias: "review-fast", Fork: true},
		{Name: "gpt-6.1-terra", Alias: "review-fast(max)", Fork: true},
	}
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"codex": aliases}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	host := pluginhost.New()
	host.RegisterPluginForTest(test.Name(), pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelRegistrar: reviewSuffixPluginRegistrar{}}})
	registryRef := registry.GetGlobalRegistry()
	host.RegisterModels(test.Context(), registryRef)
	host.RegisterExecutors(manager, registryRef)
	test.Cleanup(func() { registryRef.UnregisterClient("plugin:" + test.Name() + ":codex") })
	service := &Service{cfg: cfg, pluginHost: host}
	standard := &coreauth.Auth{ID: "review-suffix-native", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}}
	plugin := &coreauth.Auth{ID: "review-suffix-plugin", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}}
	coreauth.SetOAuthModelAliasesAttribute(plugin, []config.OAuthModelAlias{{Name: "review-plugin-up", Alias: "review-fast", Fork: true}})
	for _, credential := range []*coreauth.Auth{standard, plugin} {
		if _, err := manager.Register(context.Background(), credential); err != nil {
			test.Fatal(err)
		}
		service.registerModelsForAuth(test.Context(), credential)
		credentialID := credential.ID
		test.Cleanup(func() { registryRef.UnregisterClient(credentialID) })
	}
	if registryRef.ClientSupportsModel(plugin.ID, "review-fast") || !registryRef.ClientSupportsModel(plugin.ID, "review-plugin-up") {
		test.Fatal("plugin fixture must select through the separately registered upstream")
	}
	if got := manager.ResolveExecutionModel(plugin, "review-fast(max)"); got != "review-plugin-up(max)" {
		test.Fatalf("plugin upstream = %q", got)
	}
	read := func() ojson.Result {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)).OpenAIModels(ctx)
		if recorder.Code != http.StatusOK {
			test.Fatalf("status = %d", recorder.Code)
		}
		for _, model := range ojson.GetBytes(recorder.Body.Bytes(), "data").Array() {
			if model.Get("id").String() == "review-fast(max)" {
				return model
			}
		}
		test.Fatal("configured suffixed alias missing")
		return ojson.Result{}
	}
	if model := read(); model.Get("context_length").Int() != 200000 || model.Get("max_completion_tokens").Int() != 32000 {
		test.Fatalf("plugin limit omitted: %s", model.Raw)
	}
	registryRef.RegisterClient(plugin.ID, "codex", []*registry.ModelInfo{{ID: "review-plugin-up", ContextLength: 200000}})
	if model := read(); model.Get("context_length").Int() != 200000 || model.Get("max_completion_tokens").Exists() {
		test.Fatalf("unknown plugin output must be omitted: %s", model.Raw)
	}
}

func TestDuplicatePluginModelLimitsAreMerged(test *testing.T) {
	host := pluginhost.New()
	host.RegisterPluginForTest(test.Name(), pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelRegistrar: reviewSuffixPluginRegistrar{}}})
	registryRef := registry.GetGlobalRegistry()
	host.RegisterModels(test.Context(), registryRef)
	host.RegisterExecutors(coreauth.NewManager(nil, nil, nil), registryRef)
	test.Cleanup(func() { registryRef.UnregisterClient("plugin:" + test.Name() + ":codex") })
	service := &Service{pluginHost: host}
	models := service.appendPluginModels("codex", []*ModelInfo{{ID: "review-plugin-up", ContextLength: 500000, MaxCompletionTokens: 128000}}, nil)
	if len(models) != 1 {
		test.Fatalf("duplicate plugin models = %d", len(models))
	}
	if got, want := registry.DeclaredModelListLimits(models[0]), (registry.ModelListLimits{ContextLength: 200000, MaxCompletionTokens: 32000}); got != want {
		test.Fatalf("merged plugin limits = %+v, want %+v", got, want)
	}
}
