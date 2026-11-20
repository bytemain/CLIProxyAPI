package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestSuffixLimitsFailClosedOnUnverifiedTargets(test *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	credential := &Auth{ID: "suffix-unknown-target", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	registryRef.RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "public-model", ContextLength: 500000, MaxCompletionTokens: 128000}})
	test.Cleanup(func() { registryRef.UnregisterClient(credential.ID) })
	manager := NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"codex": {{Name: "missing-upstream", Alias: "public-model"}}})
	if _, err := manager.Register(context.Background(), credential); err != nil {
		test.Fatal(err)
	}
	initial := registry.ModelListLimits{ContextLength: 500000, MaxCompletionTokens: 128000}
	if got := manager.ConservativeSuffixModelLimits("public-model(max)", initial); got != (registry.ModelListLimits{}) {
		test.Fatalf("missing upstream limits = %+v", got)
	}
	registryRef.RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{
		{ID: "public-model", MetadataModelID: "missing-upstream", ContextLength: 200000},
	})
	if got, want := manager.ConservativeSuffixModelLimits("public-model(max)", initial), (registry.ModelListLimits{ContextLength: 200000}); got != want {
		test.Fatalf("partially known upstream limits = %+v, want %+v", got, want)
	}
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	if got := manager.ConservativeSuffixModelLimits("public-model(max)", initial); got != (registry.ModelListLimits{}) {
		test.Fatalf("Home limits = %+v", got)
	}
}

func TestSuffixLimitsFailClosedForAPIKeyRoutes(test *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	credential := &Auth{ID: "suffix-apikey", Provider: "codex", Attributes: map[string]string{"auth_kind": "apikey"}}
	registryRef.RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "apikey-model", ContextLength: 500000, MaxCompletionTokens: 128000}})
	test.Cleanup(func() { registryRef.UnregisterClient(credential.ID) })
	manager := NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), credential); err != nil {
		test.Fatal(err)
	}
	initial := registry.ModelListLimits{ContextLength: 500000, MaxCompletionTokens: 128000}
	if got := manager.ConservativeSuffixModelLimits("apikey-model(max)", initial); got != (registry.ModelListLimits{}) {
		test.Fatalf("API-key limits = %+v", got)
	}
}

func TestSuffixLimitsFollowAccountAliasAndCompletedReload(test *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"codex": {{Name: "review-wide", Alias: "review-public"}}})
	wide := &Auth{ID: "suffix-reload-wide", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	narrow := &Auth{ID: "suffix-reload-narrow", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	SetOAuthModelAliasesAttribute(narrow, []internalconfig.OAuthModelAlias{{Name: "review-small", Alias: "review-public(max)"}})
	for _, credential := range []*Auth{wide, narrow} {
		if _, err := manager.Register(context.Background(), credential); err != nil {
			test.Fatal(err)
		}
		credentialID := credential.ID
		test.Cleanup(func() { registryRef.UnregisterClient(credentialID) })
	}
	registryRef.RegisterClient(wide.ID, "codex", []*registry.ModelInfo{{ID: "review-public", MetadataModelID: "review-wide", ContextLength: 500000, MaxCompletionTokens: 120000}})
	registerSmall := func(contextLimit, outputLimit int) {
		registryRef.RegisterClient(narrow.ID, "codex", []*registry.ModelInfo{{ID: "review-small", ContextLength: contextLimit, MaxCompletionTokens: outputLimit}})
	}
	registerSmall(80000, 6000)
	if got := manager.ResolveExecutionModel(narrow, "review-public(max)"); got != "review-small(max)" {
		test.Fatalf("actual upstream = %q", got)
	}
	initial := registry.ModelListLimits{ContextLength: 500000, MaxCompletionTokens: 120000}
	check := func(want registry.ModelListLimits) {
		test.Helper()
		if got := manager.ConservativeSuffixModelLimits("review-public(max)", initial); got != want {
			test.Fatalf("limits = %+v, want %+v", got, want)
		}
	}
	check(registry.ModelListLimits{ContextLength: 80000, MaxCompletionTokens: 6000})
	registerSmall(40000, 0)
	check(registry.ModelListLimits{ContextLength: 40000})
	changed := narrow.Clone()
	SetOAuthModelAliasesAttribute(changed, []internalconfig.OAuthModelAlias{{Name: "review-missing", Alias: "review-public(max)"}})
	if _, err := manager.Update(context.Background(), changed); err != nil {
		test.Fatal(err)
	}
	if got := manager.ResolveExecutionModel(changed, "review-public(max)"); got != "review-missing(max)" {
		test.Fatalf("reloaded upstream = %q", got)
	}
	registryRef.RegisterClient(narrow.ID, "codex", []*registry.ModelInfo{{ID: "review-public", MetadataModelID: "review-small", ContextLength: 40000}})
	check(registry.ModelListLimits{})
	if _, err := manager.Update(context.Background(), narrow); err != nil {
		test.Fatal(err)
	}
	registerSmall(40000, 3000)
	check(registry.ModelListLimits{ContextLength: 40000, MaxCompletionTokens: 3000})
	apiKey := &Auth{ID: "suffix-reload-apikey", Provider: "codex", Attributes: map[string]string{"auth_kind": "apikey"}}
	if _, err := manager.Register(context.Background(), apiKey); err != nil {
		test.Fatal(err)
	}
	registryRef.RegisterClient(apiKey.ID, "codex", []*registry.ModelInfo{{ID: "review-public", ContextLength: 90000, MaxCompletionTokens: 8000}})
	test.Cleanup(func() { registryRef.UnregisterClient(apiKey.ID) })
	check(registry.ModelListLimits{})
}

func TestSuffixLimitsPayloadRuleBoundary(test *testing.T) {
	for _, scenario := range []struct {
		name    string
		payload internalconfig.PayloadConfig
		want    bool
	}{
		{name: "no rules", want: true},
		{name: "service tier override", payload: internalconfig.PayloadConfig{Override: []internalconfig.PayloadRule{{Params: map[string]any{"service_tier": "priority"}}}}, want: true},
		{name: "reasoning effort default", payload: internalconfig.PayloadConfig{Default: []internalconfig.PayloadRule{{Params: map[string]any{"reasoning.effort": "high"}}}}, want: true},
		{name: "model override", payload: internalconfig.PayloadConfig{Override: []internalconfig.PayloadRule{{Params: map[string]any{"model": "different"}}}}},
		{name: "raw model override", payload: internalconfig.PayloadConfig{OverrideRaw: []internalconfig.PayloadRule{{Params: map[string]any{"model": `"different"`}}}}},
		{name: "default model", payload: internalconfig.PayloadConfig{DefaultRaw: []internalconfig.PayloadRule{{Params: map[string]any{"model": `"different"`}}}}},
		{name: "token cap", payload: internalconfig.PayloadConfig{Override: []internalconfig.PayloadRule{{Params: map[string]any{"max_output_tokens": 8192}}}}},
		{name: "model filter", payload: internalconfig.PayloadConfig{Filter: []internalconfig.PayloadFilterRule{{Params: []string{"model"}}}}},
		{name: "unknown path", payload: internalconfig.PayloadConfig{Override: []internalconfig.PayloadRule{{Params: map[string]any{"model.*": "different"}}}}},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			if got := payloadRulesPreserveListLimits(scenario.payload); got != scenario.want {
				test.Fatalf("payload rules preserve limits = %t, want %t", got, scenario.want)
			}
		})
	}
}
