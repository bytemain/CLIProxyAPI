package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestOpenAIModelsIncludesKnownTokenLimits(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("openai-models-limits-a", "devin", []*registry.ModelInfo{
		{ID: "limits-test-full", Object: "model", OwnedBy: "devin", ContextLength: 262144, MaxCompletionTokens: 64000},
		{ID: "limits-test-none", Object: "model", OwnedBy: "devin"},
		{ID: "limits-test-shared", Object: "model", OwnedBy: "devin", ContextLength: 400000, MaxCompletionTokens: 128000},
	})
	modelRegistry.RegisterClient("openai-models-limits-b", "codex", []*registry.ModelInfo{
		{ID: "limits-test-shared", Object: "model", OwnedBy: "codex", ContextLength: 272000},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("openai-models-limits-a")
		modelRegistry.UnregisterClient("openai-models-limits-b")
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
	handler.OpenAIModels(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byID := make(map[string]map[string]any)
	for _, model := range body.Data {
		id, _ := model["id"].(string)
		byID[id] = model
	}

	full := byID["limits-test-full"]
	if full["context_length"] != float64(262144) || full["max_completion_tokens"] != float64(64000) {
		t.Fatalf("limits-test-full = %v", full)
	}

	none := byID["limits-test-none"]
	if none == nil {
		t.Fatal("limits-test-none missing from list")
	}
	if _, ok := none["context_length"]; ok {
		t.Fatalf("limits-test-none should omit context_length: %v", none)
	}
	if _, ok := none["max_completion_tokens"]; ok {
		t.Fatalf("limits-test-none should omit max_completion_tokens: %v", none)
	}

	shared := byID["limits-test-shared"]
	if shared["context_length"] != float64(272000) {
		t.Fatalf("limits-test-shared context_length = %v, want smallest 272000", shared["context_length"])
	}
	if _, ok := shared["max_completion_tokens"]; ok {
		t.Fatalf("limits-test-shared should omit max_completion_tokens when one client lacks it: %v", shared)
	}

	for _, key := range []string{"max_context_length", "display_name", "supported_parameters"} {
		if _, ok := full[key]; ok {
			t.Fatalf("unexpected field %q in model list entry: %v", key, full)
		}
	}
}
