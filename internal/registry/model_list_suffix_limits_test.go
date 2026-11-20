package registry

import "testing"

func TestRegisteredEffortVariantLimitsCoverBaseRoutes(test *testing.T) {
	registry := newTestModelRegistry()
	registry.RegisterClient("base", "codex", []*ModelInfo{
		{ID: "Example-Fast", ContextLength: 200000, MaxCompletionTokens: 32000},
		{ID: "unknown", ContextLength: 100000},
		{ID: "claude[1m]", ContextLength: 1000000, MaxCompletionTokens: 64000},
		{ID: "standalone(max)", ContextLength: 160000, MaxCompletionTokens: 16000},
		{ID: "versioned", ContextLength: 200000, MaxCompletionTokens: 32000},
		{ID: "versioned(2024)", ContextLength: 500000, MaxCompletionTokens: 128000},
	})
	registry.RegisterClient("variant", "codex", []*ModelInfo{
		{ID: "example-fast(max)", ContextLength: 500000, MaxCompletionTokens: 128000},
		{ID: "unknown(max)", ContextLength: 400000, MaxCompletionTokens: 64000},
		{ID: "claude[1m](max)", ContextLength: 1000000, MaxCompletionTokens: 128000},
	})
	limits := registry.GetModelListLimits()
	for id, want := range map[string]ModelListLimits{
		"Example-Fast":      {ContextLength: 200000, MaxCompletionTokens: 32000},
		"example-fast(max)": {ContextLength: 200000, MaxCompletionTokens: 32000},
		"unknown(max)":      {ContextLength: 100000},
		"claude[1m](max)":   {ContextLength: 1000000, MaxCompletionTokens: 64000},
		"standalone(max)":   {ContextLength: 160000, MaxCompletionTokens: 16000},
		"versioned(2024)":   {ContextLength: 200000, MaxCompletionTokens: 32000},
	} {
		if got := limits[id]; got != want {
			test.Errorf("%s = %+v, want %+v", id, got, want)
		}
	}
}

func TestRegisteredEffortVariantLimitsCoverNestedBaseRoutes(test *testing.T) {
	registry := newTestModelRegistry()
	registry.RegisterClient("nested", "codex", []*ModelInfo{
		{ID: "base", ContextLength: 100000, MaxCompletionTokens: 10000},
		{ID: "base(high)", ContextLength: 200000, MaxCompletionTokens: 20000},
		{ID: "base(high)(max)", ContextLength: 300000, MaxCompletionTokens: 30000},
	})
	for iteration := 0; iteration < 20; iteration++ {
		if got, want := registry.GetModelListLimits()["base(high)(max)"], (ModelListLimits{ContextLength: 100000, MaxCompletionTokens: 10000}); got != want {
			test.Fatalf("nested limits = %+v, want %+v", got, want)
		}
	}
}

func TestRegisteredVariantLimitsAlsoConstrainCaseAndBase(test *testing.T) {
	registry := newTestModelRegistry()
	registry.RegisterClient("one", "codex", []*ModelInfo{
		{ID: "Shared", ContextLength: 500000, MaxCompletionTokens: 128000},
		{ID: "shared(16384)", ContextLength: 200000, MaxCompletionTokens: 32000},
		{ID: "shared[1m]", ContextLength: 1000000, MaxCompletionTokens: 64000},
	})
	for _, id := range []string{"Shared", "shared(16384)"} {
		if got, want := registry.GetModelListLimits()[id], (ModelListLimits{ContextLength: 200000, MaxCompletionTokens: 32000}); got != want {
			test.Fatalf("%s = %+v, want %+v", id, got, want)
		}
	}
	if got, want := registry.GetModelListLimits()["shared[1m]"], (ModelListLimits{ContextLength: 1000000, MaxCompletionTokens: 64000}); got != want {
		test.Fatalf("distinct bracket variant = %+v, want %+v", got, want)
	}
}
