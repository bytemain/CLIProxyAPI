package registry

import "time"

// PerplexityWebModel maps a proxy-facing model ID to a perplexity.ai web
// model_preference. IDs carry a "pplx-" prefix so they never collide with the
// same upstream model served by another provider (e.g. codex gpt-5.5).
type PerplexityWebModel struct {
	ID         string
	Label      string
	Preference string
	// Thinking is the reasoning variant preference, if Perplexity offers one.
	Thinking string
	// Tier is the minimum subscription tier (free, pro, max).
	Tier string
}

// PerplexityWebModels lists the web (cookie) models. Preferences are taken from
// the perplexity.ai web client (/rest/models/config); update when Perplexity rotates model IDs.
var PerplexityWebModels = []PerplexityWebModel{
	{ID: "pplx-auto", Label: "Perplexity Best", Preference: "pplx_pro", Tier: "free"},
	{ID: "pplx-sonar", Label: "Sonar", Preference: "experimental", Tier: "pro"},
	{ID: "pplx-gpt-6.1-sol", Label: "GPT-6.1 Sol", Preference: "gpt6_1_sol", Thinking: "gpt6_1_sol_thinking", Tier: "max"},
	{ID: "pplx-gpt-6-sol", Label: "GPT-6 Sol", Preference: "gpt6_sol", Thinking: "gpt6_sol_thinking", Tier: "max"},
	{ID: "pplx-gpt-6-astra", Label: "GPT-6 Astra", Preference: "gpt6_astra", Thinking: "gpt6_astra_thinking", Tier: "pro"},
	{ID: "pplx-gpt-5.6-terra", Label: "GPT-5.6 Terra", Preference: "gpt56_terra", Thinking: "gpt56_terra_thinking", Tier: "pro"},
	{ID: "pplx-gpt-5.6-sol", Label: "GPT-5.6 Sol", Preference: "gpt56_sol", Thinking: "gpt56_sol_thinking", Tier: "max"},
	{ID: "pplx-gpt-5.5", Label: "GPT-5.5", Preference: "gpt55", Thinking: "gpt55_thinking", Tier: "pro"},
	{ID: "pplx-gpt-5-mini", Label: "GPT-5 Mini", Preference: "gpt5_mini", Tier: "pro"},
	{ID: "pplx-gemini-4-argon", Label: "Gemini 4 Argon", Preference: "gemini4argon", Thinking: "gemini4argonthinking", Tier: "pro"},
	{ID: "pplx-gemini-3.8-flash", Label: "Gemini 3.8 Flash", Preference: "gemini38flash", Thinking: "gemini38flashthinking", Tier: "pro"},
	{ID: "pplx-gemini-3.1-pro", Label: "Gemini 3.1 Pro", Preference: "gemini31pro_high", Tier: "pro"},
	{ID: "pplx-gemini-3.5-flash", Label: "Gemini 3.5 Flash", Preference: "gemini35flash", Tier: "pro"},
	{ID: "pplx-claude-opus-5.5", Label: "Claude Opus 5.5", Preference: "claude55opus", Thinking: "claude55opusthinking", Tier: "max"},
	{ID: "pplx-claude-fable-5.1", Label: "Claude Fable 5.1", Preference: "claudefable51", Thinking: "claudefable51thinking", Tier: "max"},
	{ID: "pplx-claude-sonnet-5.5", Label: "Claude Sonnet 5.5", Preference: "claude55sonnet", Thinking: "claude55sonnetthinking", Tier: "pro"},
	{ID: "pplx-claude-haiku-5.5", Label: "Claude Haiku 5.5", Preference: "claude55haiku", Thinking: "claude55haikuthinking", Tier: "pro"},
	{ID: "pplx-claude-sonnet-5", Label: "Claude Sonnet 5", Preference: "claude50sonnet", Thinking: "claude50sonnetthinking", Tier: "pro"},
	{ID: "pplx-claude-sonnet-4.6", Label: "Claude Sonnet 4.6", Preference: "claude46sonnet", Thinking: "claude46sonnetthinking", Tier: "pro"},
	{ID: "pplx-claude-opus-4.8", Label: "Claude Opus 4.8", Preference: "claude48opus", Thinking: "claude48opusthinking", Tier: "max"},
	{ID: "pplx-claude-opus-4.7", Label: "Claude Opus 4.7", Preference: "claude47opus", Thinking: "claude47opusthinking", Tier: "max"},
	{ID: "pplx-grok-4.7", Label: "Grok 4.7", Preference: "grok47", Thinking: "grok47thinking", Tier: "pro"},
	{ID: "pplx-grok-4.6", Label: "Grok 4.6", Preference: "grok46low", Thinking: "grok46medium", Tier: "pro"},
	{ID: "pplx-grok-4.5", Label: "Grok 4.5", Preference: "grok45low", Thinking: "grok45medium", Tier: "pro"},
	{ID: "pplx-kimi-k3", Label: "Kimi K3", Preference: "kimik3", Thinking: "kimik3thinking", Tier: "pro"},
	{ID: "pplx-kimi-k2.6", Label: "Kimi K2.6", Preference: "kimik26instant", Thinking: "kimik26thinking", Tier: "pro"},
	{ID: "pplx-glm-5.3", Label: "GLM-5.3", Preference: "glm_5_3_thinking", Tier: "pro"},
	{ID: "pplx-glm-5.2", Label: "GLM-5.2", Preference: "glm_5_2", Tier: "pro"},
	{ID: "pplx-nemotron-3.5-super", Label: "Nemotron 3.5 Super VL", Preference: "nv_nemotron_3_5_super", Tier: "pro"},
	{ID: "pplx-nemotron-3-ultra", Label: "Nemotron 3 Ultra", Preference: "nv_nemotron_3_ultra", Tier: "pro"},
}

// PerplexityThinkingSuffix marks the reasoning variant of a web model.
const PerplexityThinkingSuffix = "-thinking"

// LookupPerplexityWebModel resolves a model ID (optionally with the
// "-thinking" suffix) to its web preference.
func LookupPerplexityWebModel(id string) (PerplexityWebModel, bool, bool) {
	thinking := false
	base := id
	if len(id) > len(PerplexityThinkingSuffix) && id[len(id)-len(PerplexityThinkingSuffix):] == PerplexityThinkingSuffix {
		base = id[:len(id)-len(PerplexityThinkingSuffix)]
		thinking = true
	}
	for _, m := range PerplexityWebModels {
		if m.ID == base {
			if thinking && m.Thinking == "" {
				return PerplexityWebModel{}, false, false
			}
			return m, thinking, true
		}
	}
	return PerplexityWebModel{}, false, false
}

// PerplexityAPIModels are the official API (pplx- key) models.
var PerplexityAPIModels = []struct{ ID, Label string }{
	{"sonar", "Sonar"},
	{"sonar-pro", "Sonar Pro"},
	{"sonar-reasoning-pro", "Sonar Reasoning Pro"},
	{"sonar-deep-research", "Sonar Deep Research"},
}

func perplexityModelInfo(id, label string) *ModelInfo {
	return &ModelInfo{
		ID:                         id,
		Object:                     "model",
		Created:                    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix(),
		OwnedBy:                    "perplexity",
		Type:                       "perplexity",
		DisplayName:                label,
		ContextLength:              128000,
		MaxCompletionTokens:        16384,
		InputTokenLimit:            128000,
		OutputTokenLimit:           16384,
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	}
}

// GetPerplexityModels returns models for a Perplexity credential kind
// ("cookie" or "api_key").
func GetPerplexityModels(kind string) []*ModelInfo {
	if kind == "api_key" {
		out := make([]*ModelInfo, 0, len(PerplexityAPIModels))
		for _, m := range PerplexityAPIModels {
			out = append(out, perplexityModelInfo(m.ID, "Perplexity "+m.Label))
		}
		return out
	}
	out := make([]*ModelInfo, 0, len(PerplexityWebModels)*2)
	for _, m := range PerplexityWebModels {
		out = append(out, perplexityModelInfo(m.ID, "Perplexity "+m.Label))
		if m.Thinking != "" {
			out = append(out, perplexityModelInfo(m.ID+PerplexityThinkingSuffix, "Perplexity "+m.Label+" Thinking"))
		}
	}
	return out
}
