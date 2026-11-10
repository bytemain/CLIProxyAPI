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
