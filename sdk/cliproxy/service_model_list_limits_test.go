package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func registerForListLimits(t *testing.T, clientID string, models []*ModelInfo) map[string]registry.ModelListLimits {
	t.Helper()
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", models)
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	return modelRegistry.GetModelListLimits()
}

func TestModelListLimitsOpenAICompatibilityPool(t *testing.T) {
	for _, tt := range []struct {
		name        string
		secondLimit int
		want        int
	}{
		{name: "smallest across pool", secondLimit: 120000, want: 120000},
		{name: "unknown member makes it unknown", secondLimit: 0, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			compat := &config.OpenAICompatibility{
				Name: "pool-provider",
				Models: []config.OpenAICompatibilityModel{
					{Name: "upstream-a", Alias: "list-limits-pool", MaxContextLength: 400000},
					{Name: "upstream-b", Alias: "list-limits-pool", MaxContextLength: tt.secondLimit},
				},
			}
			limits := registerForListLimits(t, "list-limits-pool-client", buildOpenAICompatibilityConfigModels(compat))
			if got := limits["list-limits-pool"].ContextLength; got != tt.want {
				t.Fatalf("context_length = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestModelListLimitsOpenAICompatibilityPoolWithPrefix(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name: "pool-provider",
		Models: []config.OpenAICompatibilityModel{
			{Name: "upstream-a", Alias: "list-limits-prefixed", MaxContextLength: 400000},
			{Name: "upstream-b", Alias: "list-limits-prefixed", MaxContextLength: 120000},
		},
	}
	for _, force := range []bool{false, true} {
		models := applyModelPrefixes(buildOpenAICompatibilityConfigModels(compat), "team", force)
		limits := registerForListLimits(t, "list-limits-prefixed-client", models)
		if got := limits["team/list-limits-prefixed"].ContextLength; got != 120000 {
			t.Fatalf("force=%v: prefixed context_length = %d, want 120000", force, got)
		}
		if !force {
			if got := limits["list-limits-prefixed"].ContextLength; got != 120000 {
				t.Fatalf("unprefixed context_length = %d, want 120000", got)
			}
		}
		registry.GetGlobalRegistry().UnregisterClient("list-limits-prefixed-client")
	}
}

func TestModelListLimitsAPIKeyConfigModels(t *testing.T) {
	models := buildConfigModels([]config.ClaudeModel{
		{Name: "upstream-a", Alias: "list-limits-claude", MaxContextLength: 200000},
		{Name: "upstream-b", Alias: "list-limits-claude", MaxContextLength: 150000},
	}, "anthropic", "claude", "claude")
	if len(models) != 1 {
		t.Fatalf("expected duplicate alias to collapse to 1 model, got %d", len(models))
	}
	limits := registerForListLimits(t, "list-limits-claude-client", models)
	if got := limits["list-limits-claude"].ContextLength; got != 150000 {
		t.Fatalf("context_length = %d, want 150000", got)
	}
}

func TestModelListLimitsOAuthAliasPool(t *testing.T) {
	models := applyOAuthModelAliasEntries([]config.OAuthModelAlias{
		{Name: "list-limits-big", Alias: "list-limits-alias"},
		{Name: "list-limits-small", Alias: "list-limits-alias"},
	}, []*ModelInfo{
		{ID: "list-limits-big", ContextLength: 400000, MaxCompletionTokens: 128000},
		{ID: "list-limits-small", ContextLength: 200000, MaxCompletionTokens: 64000},
	})
	limits := registerForListLimits(t, "list-limits-alias-client", models)
	want := registry.ModelListLimits{ContextLength: 200000, MaxCompletionTokens: 64000}
	if got := limits["list-limits-alias"]; got != want {
		t.Fatalf("alias limits = %+v, want %+v", got, want)
	}
}

func TestModelListLimitsOAuthSettingAfterMerge(t *testing.T) {
	models := applyOAuthModelAliasEntries([]config.OAuthModelAlias{
		{Name: "list-limits-big", Alias: "list-limits-override"},
		{Name: "list-limits-small", Alias: "list-limits-override"},
	}, []*ModelInfo{
		{ID: "list-limits-big", ContextLength: 400000, MaxCompletionTokens: 128000},
		{ID: "list-limits-small", ContextLength: 200000, MaxCompletionTokens: 64000},
	})
	models = applyOAuthSettingEntries([]config.OAuthModelSetting{
		{Name: "list-limits-override", MaxContextLength: 180000},
	}, models)
	limits := registerForListLimits(t, "list-limits-override-client", models)
	want := registry.ModelListLimits{ContextLength: 180000, MaxCompletionTokens: 64000}
	if got := limits["list-limits-override"]; got != want {
		t.Fatalf("override limits = %+v, want %+v", got, want)
	}
}
