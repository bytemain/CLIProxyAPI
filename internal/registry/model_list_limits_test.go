package registry

import "testing"

func TestGetModelListLimitsSingleClient(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "devin", []*ModelInfo{
		{ID: "m-full", ContextLength: 262144, MaxCompletionTokens: 64000},
		{ID: "m-none"},
		{ID: "m-context-only", ContextLength: 128000},
	})

	limits := r.GetModelListLimits()
	if got := limits["m-full"]; got != (ModelListLimits{ContextLength: 262144, MaxCompletionTokens: 64000}) {
		t.Fatalf("m-full limits = %+v", got)
	}
	if got := limits["m-none"]; got != (ModelListLimits{}) {
		t.Fatalf("m-none limits = %+v, want unknown", got)
	}
	if got := limits["m-context-only"]; got != (ModelListLimits{ContextLength: 128000}) {
		t.Fatalf("m-context-only limits = %+v", got)
	}
}

func TestGetModelListLimitsTakesSmallestAcrossClients(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-a", "codex", []*ModelInfo{{ID: "shared", ContextLength: 400000, MaxCompletionTokens: 128000}})
	r.RegisterClient("client-b", "antigravity", []*ModelInfo{{ID: "shared", ContextLength: 200000, MaxCompletionTokens: 64000}})
	r.RegisterClient("client-c", "codex", []*ModelInfo{{ID: "shared", ContextLength: 300000, MaxCompletionTokens: 100000}})

	if got := r.GetModelListLimits()["shared"]; got != (ModelListLimits{ContextLength: 200000, MaxCompletionTokens: 64000}) {
		t.Fatalf("shared limits = %+v, want smallest values", got)
	}
}

func TestGetModelListLimitsUnknownOnAnyClientIsUnknown(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-a", "codex", []*ModelInfo{{ID: "shared", ContextLength: 400000, MaxCompletionTokens: 128000}})
	r.RegisterClient("client-b", "antigravity", []*ModelInfo{{ID: "shared", ContextLength: 200000}})

	if got := r.GetModelListLimits()["shared"]; got != (ModelListLimits{ContextLength: 200000}) {
		t.Fatalf("shared limits = %+v, want output unknown", got)
	}

	r.UnregisterClient("client-b")
	if got := r.GetModelListLimits()["shared"]; got != (ModelListLimits{ContextLength: 400000, MaxCompletionTokens: 128000}) {
		t.Fatalf("after unregister, shared limits = %+v", got)
	}
}

func TestGetModelListLimitsMaxContextLengthOverride(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{ID: "gpt", ContextLength: 400000, MaxContextLength: 272000, MaxCompletionTokens: 128000}})

	if got := r.GetModelListLimits()["gpt"]; got != (ModelListLimits{ContextLength: 272000, MaxCompletionTokens: 128000}) {
		t.Fatalf("gpt limits = %+v, want override context", got)
	}
}

func TestGetModelListLimitsDropsOutputAboveContext(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{ID: "odd", ContextLength: 8000, MaxCompletionTokens: 16000}})

	if got := r.GetModelListLimits()["odd"]; got != (ModelListLimits{ContextLength: 8000}) {
		t.Fatalf("odd limits = %+v, want output dropped", got)
	}
}

func TestGetModelListLimitsUsesGeminiOutputTokenLimit(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{ID: "gemini-2.5-pro", InputTokenLimit: 1048576, OutputTokenLimit: 65536}})

	if got := r.GetModelListLimits()["gemini-2.5-pro"]; got != (ModelListLimits{MaxCompletionTokens: 65536}) {
		t.Fatalf("gemini limits = %+v, want output only (input limit is not a context window)", got)
	}
}

func TestGetModelListLimitsMixedOutputFieldsAcrossClients(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-a", "gemini", []*ModelInfo{{ID: "mixed", OutputTokenLimit: 65536}})
	r.RegisterClient("client-b", "antigravity", []*ModelInfo{{ID: "mixed", MaxCompletionTokens: 32000}})
	r.RegisterClient("client-c", "antigravity", []*ModelInfo{{ID: "mixed-missing", MaxCompletionTokens: 32000}})
	r.RegisterClient("client-d", "gemini", []*ModelInfo{{ID: "mixed-missing", InputTokenLimit: 1048576}})

	limits := r.GetModelListLimits()
	if got := limits["mixed"].MaxCompletionTokens; got != 32000 {
		t.Fatalf("mixed output = %d, want 32000", got)
	}
	if got := limits["mixed-missing"].MaxCompletionTokens; got != 0 {
		t.Fatalf("mixed-missing output = %d, want unknown", got)
	}
}

func TestGetModelListLimitsCapsToAcceptedRange(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "antigravity", []*ModelInfo{
		{ID: "huge", ContextLength: 1048576, MaxCompletionTokens: 1048576},
		{ID: "giant-context", ContextLength: 20_000_000, MaxCompletionTokens: 128000},
	})

	limits := r.GetModelListLimits()
	if got := limits["huge"]; got != (ModelListLimits{ContextLength: 1048576, MaxCompletionTokens: 1_000_000}) {
		t.Fatalf("huge limits = %+v, want output capped at 1000000", got)
	}
	if got := limits["giant-context"]; got != (ModelListLimits{ContextLength: 10_000_000, MaxCompletionTokens: 128000}) {
		t.Fatalf("giant-context limits = %+v, want context capped at 10000000", got)
	}
}

func TestGetModelListLimitsConflictingOutputFieldsTakeSmaller(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "custom", []*ModelInfo{
		{ID: "max-larger", MaxCompletionTokens: 64000, OutputTokenLimit: 32000},
		{ID: "output-larger", MaxCompletionTokens: 32000, OutputTokenLimit: 64000},
	})

	limits := r.GetModelListLimits()
	for _, id := range []string{"max-larger", "output-larger"} {
		if got := limits[id].MaxCompletionTokens; got != 32000 {
			t.Fatalf("%s output = %d, want smaller declared value 32000", id, got)
		}
	}
}
