package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestExplicitEffortAliasKeepsBasePayloadRule(test *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gpt-6.1-terra-fast", Protocol: "codex"}},
		Params: map[string]any{"service_tier": "priority"},
	}}}}
	output := ApplyPayloadConfigWithRequest(cfg, "gpt-6.1-terra(max)", "codex", "openai", "", []byte(`{"model":"gpt-6.1-terra"}`), nil, "gpt-6.1-terra-fast(max)", "/v1/chat/completions", nil)
	if got := ojson.GetBytes(output, "service_tier").String(); got != "priority" {
		test.Fatalf("service_tier = %q, want priority", got)
	}
}
