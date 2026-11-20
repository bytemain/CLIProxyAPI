package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestExplicitEffortAliasAppearsInOpenAIModelList(test *testing.T) {
	models := []*ModelInfo{{ID: "gpt-6.1-terra", Object: "model", OwnedBy: "openai", ContextLength: 500000, MaxCompletionTokens: 128000}}
	base := config.OAuthModelAlias{Name: "gpt-6.1-terra", Alias: "gpt-6.1-terra-fast", Fork: true}
	variant := config.OAuthModelAlias{Name: "gpt-6.1-terra", Alias: "gpt-6.1-terra-fast(max)", Fork: true}
	cfg, err := config.ParseConfigBytes([]byte(`config-version: 8
oauth:
  model-alias:
    codex:
      - name: gpt-6.1-terra
        alias: gpt-6.1-terra-fast
        fork: true
      - name: gpt-6.1-terra
        alias: gpt-6.1-terra-fast(max)
        fork: true
`))
	if err != nil {
		test.Fatal(err)
	}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("review-explicit-effort-alias", "codex", applyOAuthModelAlias(cfg, "codex", "oauth", models))
	test.Cleanup(func() { registryRef.UnregisterClient("review-explicit-effort-alias") })
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "review-explicit-effort-alias", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}); err != nil {
		test.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)).OpenAIModels(ctx)
	if recorder.Code != http.StatusOK {
		test.Fatalf("status = %d", recorder.Code)
	}
	data := ojson.GetBytes(recorder.Body.Bytes(), "data")
	if !data.IsArray() {
		test.Fatal("model list is not an array")
	}
	seen := make(map[string]bool)
	for _, model := range data.Array() {
		id := model.Get("id").String()
		if id == base.Alias || id == variant.Alias {
			if model.Get("context_length").Int() != 500000 || model.Get("max_completion_tokens").Int() != 128000 {
				test.Fatalf("%s has unexpected metadata", id)
			}
			seen[id] = true
		}
	}
	if !seen[base.Alias] || !seen[variant.Alias] {
		test.Fatalf("expected both configured aliases, got %v", seen)
	}
}

func TestSuffixAliasOmitsLimitsWhenPayloadCanChangeModel(test *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{"codex": {
			{Name: "rule-upstream", Alias: "rule-public", Fork: true},
			{Name: "rule-upstream", Alias: "rule-public(max)", Fork: true},
		}},
		Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "*"}},
			Params: map[string]any{"model": "different-upstream"},
		}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	credential := &coreauth.Auth{ID: "review-rule-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	if _, err := manager.Register(context.Background(), credential); err != nil {
		test.Fatal(err)
	}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(credential.ID, credential.Provider, applyOAuthModelAlias(cfg, "codex", "oauth", []*ModelInfo{{ID: "rule-upstream", ContextLength: 500000, MaxCompletionTokens: 128000}}))
	test.Cleanup(func() { registryRef.UnregisterClient(credential.ID) })
	modified := helps.ApplyPayloadConfigWithRequest(cfg, "rule-upstream(max)", "codex", "openai", "", []byte(`{"model":"rule-upstream"}`), nil, "rule-public(max)", "/v1/chat/completions", nil)
	if got := ojson.GetBytes(modified, "model").String(); got != "different-upstream" {
		test.Fatalf("payload override did not change the model: %q", got)
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)).OpenAIModels(ctx)
	for _, model := range ojson.GetBytes(recorder.Body.Bytes(), "data").Array() {
		if model.Get("id").String() != "rule-public(max)" {
			continue
		}
		if model.Get("context_length").Exists() || model.Get("max_completion_tokens").Exists() {
			test.Fatalf("model-changing payload rule still advertised limits: %s", model.Raw)
		}
		return
	}
	test.Fatal("configured suffix alias missing")
}

func TestExplicitEffortAliasIncludesBaseOnlyRouteLimits(test *testing.T) {
	base := config.OAuthModelAlias{Name: "source", Alias: "review-base", Fork: true}
	variant := config.OAuthModelAlias{Name: "source", Alias: "review-base(max)", Fork: true}
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"codex": {base, variant}}}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("review-variant", "codex", applyOAuthModelAlias(cfg, "codex", "oauth", []*ModelInfo{{ID: "source", ContextLength: 500000, MaxCompletionTokens: 128000}}))
	registryRef.RegisterClient("review-base-only", "codex", []*ModelInfo{{ID: base.Alias, MetadataModelID: "small-source", ContextLength: 200000, MaxCompletionTokens: 32000}})
	test.Cleanup(func() {
		registryRef.UnregisterClient("review-variant")
		registryRef.UnregisterClient("review-base-only")
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	variantCredential := &coreauth.Auth{ID: "review-variant", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	baseCredential := &coreauth.Auth{ID: "review-base-only", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	coreauth.SetOAuthModelAliasesAttribute(baseCredential, []config.OAuthModelAlias{{Name: "small-source", Alias: base.Alias, Fork: true}})
	for _, credential := range []*coreauth.Auth{variantCredential, baseCredential} {
		if _, err := manager.Register(context.Background(), credential); err != nil {
			test.Fatal(err)
		}
	}
	read := func() ojson.Result {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		openAIHandler := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
		openAIHandler.OpenAIModels(ctx)
		if recorder.Code != http.StatusOK {
			test.Fatalf("status = %d", recorder.Code)
		}
		for _, model := range ojson.GetBytes(recorder.Body.Bytes(), "data").Array() {
			if model.Get("id").String() == variant.Alias {
				return model
			}
		}
		test.Fatal("variant is missing")
		return ojson.Result{}
	}
	if model := read(); model.Get("context_length").Int() != 200000 || model.Get("max_completion_tokens").Int() != 32000 {
		test.Fatalf("variant did not cover the base-only route: %s", model.Raw)
	}
	registryRef.RegisterClient("review-base-only", "codex", []*ModelInfo{{ID: base.Alias, MetadataModelID: "small-source", ContextLength: 200000}})
	if model := read(); model.Get("context_length").Int() != 200000 || model.Get("max_completion_tokens").Exists() {
		test.Fatalf("unknown base output must remain unknown: %s", model.Raw)
	}
}
