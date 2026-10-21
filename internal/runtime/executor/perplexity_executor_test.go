package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const perplexityTestSSE = "event: message\r\n" +
	`data: {"display_model":"claude46sonnet","blocks":[{"intended_usage":"pro_search_steps","plan_block":{"steps":[{"step_type":"SEARCH_WEB","search_web_content":{"queries":[{"query":"go 1.26 release"}]}}]}}]}` + "\r\n\r\n" +
	"event: message\n" +
	`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"IN_PROGRESS","chunks":["Hello"]}},{"intended_usage":"ask_text_0_markdown","markdown_block":{"progress":"IN_PROGRESS","chunks":["Hello"]}}]}` + "\n\n" +
	"event: message\n" +
	`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"IN_PROGRESS","chunks":[", world"]}}]}` + "\n\n" +
	"event: message\n" +
	`data: {"web_results":[{"name":"Go","url":"https://go.dev"}],"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"DONE","chunks":["Hello, world [1]."]}}]}` + "\n\n" +
	"event: end_of_stream\ndata: {}\n\n"

func perplexityTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "perplexity-test.json",
		Provider: "perplexity",
		Metadata: map[string]any{
			"type":    "perplexity",
			"cookies": map[string]any{"__Secure-next-auth.session-token": "tok"},
		},
	}
}

func perplexityTestContext(t *testing.T, captured *[]byte) context.Context {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://www.perplexity.ai/rest/sse/perplexity_ask" {
			t.Fatalf("unexpected url %s", req.URL)
		}
		if !strings.Contains(req.Header.Get("Cookie"), "__Secure-next-auth.session-token=tok") {
			t.Fatalf("missing session cookie: %q", req.Header.Get("Cookie"))
		}
		*captured, _ = io.ReadAll(req.Body)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(perplexityTestSSE)), Request: req}, nil
	})
	return context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
}

func TestPerplexityExecuteOpenAI(t *testing.T) {
	var sent []byte
	ctx := perplexityTestContext(t, &sent)
	exec := NewPerplexityExecutor(&config.Config{})
	payload := `{"model":"pplx-claude-sonnet-4.6-thinking","messages":[{"role":"system","content":"Be brief"},{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":[{"type":"text","text":"latest go?"}]}]}`
	auth := perplexityTestAuth()
	auth.Metadata["citations"] = true
	resp, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "pplx-claude-sonnet-4.6-thinking", Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !gjson.GetBytes(sent, "params.is_incognito").Bool() {
		t.Fatalf("queries must be incognito by default: %s", sent)
	}
	if got := gjson.GetBytes(sent, "params.model_preference").String(); got != "claude46sonnetthinking" {
		t.Fatalf("model_preference = %q", got)
	}
	query := gjson.Parse(gjson.GetBytes(sent, "query_str").String())
	if query.Get("query").String() != "latest go?" || query.Get("instructions.0").String() != "Be brief" || query.Get("history.#").Int() != 2 {
		t.Fatalf("unexpected query envelope: %s", query.Raw)
	}
	content := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	if !strings.HasPrefix(content, "Hello, world [1].") || !strings.Contains(content, "[1] Go - https://go.dev") {
		t.Fatalf("unexpected content: %q", content)
	}
	if !strings.Contains(gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String(), "Searching: go 1.26 release") {
		t.Fatalf("missing reasoning: %s", resp.Payload)
	}
}

func TestPerplexityExecuteStreamClaude(t *testing.T) {
	var sent []byte
	ctx := perplexityTestContext(t, &sent)
	exec := NewPerplexityExecutor(&config.Config{})
	auth := perplexityTestAuth()
	auth.Metadata["incognito"] = false
	payload := `{"model":"pplx-auto","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "pplx-auto", Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var all strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk err: %v", chunk.Err)
		}
		all.Write(chunk.Payload)
	}
	out := all.String()
	if gjson.GetBytes(sent, "params.is_incognito").Bool() {
		t.Fatalf("incognito metadata override ignored: %s", sent)
	}
	if strings.Contains(out, "Sources:") {
		t.Fatalf("sources footer must be off by default:\n%s", out)
	}
	if gjson.GetBytes(sent, "query_str").String() != "hi" || gjson.GetBytes(sent, "params.mode").String() != "concise" {
		t.Fatalf("unexpected upstream payload: %s", sent)
	}
	for _, want := range []string{"message_start", "Hello", ", world", " [1].", "message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, `"text":"Hello"`) != 1 {
		t.Fatalf("mirrored answer block duplicated:\n%s", out)
	}
}

func TestPerplexityUnknownModel(t *testing.T) {
	exec := NewPerplexityExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), perplexityTestAuth(), cliproxyexecutor.Request{Model: "pplx-nope", Payload: []byte(`{"messages":[{"role":"user","content":"x"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("expected unknown model error, got %v", err)
	}
}

func TestPerplexityUpstreamUnauthorized(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"detail":"unauthorized"}`)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	exec := NewPerplexityExecutor(&config.Config{})
	_, err := exec.ExecuteStream(ctx, perplexityTestAuth(), cliproxyexecutor.Request{Model: "pplx-auto", Payload: []byte(`{"messages":[{"role":"user","content":"x"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	se, ok := err.(interface{ StatusCode() int })
	if !ok || se.StatusCode() != 401 {
		t.Fatalf("expected 401 status error, got %v", err)
	}
}

func TestPerplexityExecuteStreamResponses(t *testing.T) {
	var sent []byte
	ctx := perplexityTestContext(t, &sent)
	exec := NewPerplexityExecutor(&config.Config{})
	payload := `{"model":"pplx-gpt-5.5","stream":true,"reasoning":{"effort":"high"},"instructions":"sys","input":[{"role":"user","content":[{"type":"input_text","text":"q"}]}]}`
	result, err := exec.ExecuteStream(ctx, perplexityTestAuth(), cliproxyexecutor.Request{Model: "pplx-gpt-5.5", Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var all strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk err: %v", chunk.Err)
		}
		all.Write(chunk.Payload)
	}
	if got := gjson.GetBytes(sent, "params.model_preference").String(); got != "gpt55_thinking" {
		t.Fatalf("reasoning effort should select thinking variant, got %q", got)
	}
	if !strings.Contains(all.String(), "response.completed") || !strings.Contains(all.String(), "Hello") {
		t.Fatalf("unexpected responses stream:\n%s", all.String())
	}
}

func TestPerplexityExecuteClaudeNonStreamThinkingFirst(t *testing.T) {
	var sent []byte
	ctx := perplexityTestContext(t, &sent)
	exec := NewPerplexityExecutor(&config.Config{})
	payload := `{"model":"pplx-gpt-5.5-thinking","max_tokens":100,"messages":[{"role":"user","content":"q"}]}`
	resp, err := exec.Execute(ctx, perplexityTestAuth(), cliproxyexecutor.Request{Model: "pplx-gpt-5.5-thinking", Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gjson.GetBytes(resp.Payload, "content.0.type").String() != "thinking" || gjson.GetBytes(resp.Payload, "content.1.type").String() != "text" {
		t.Fatalf("thinking block must precede text: %s", resp.Payload)
	}
}

func TestPerplexityRefreshDoesNotMutateLiveAuth(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Add("Set-Cookie", "__Secure-next-auth.session-token=rotated; Path=/; HttpOnly")
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(`{"user":{"email":"a@b.c"},"expires":"2026-12-01T00:00:00Z"}`)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := perplexityTestAuth()
	updated, err := NewPerplexityExecutor(&config.Config{}).Refresh(ctx, auth)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if updated == auth {
		t.Fatal("refresh must return a clone")
	}
	if got := auth.Metadata["cookies"].(map[string]any)["__Secure-next-auth.session-token"]; got != "tok" {
		t.Fatalf("live auth mutated: %v", got)
	}
	if got := updated.Metadata["cookies"].(map[string]string)["__Secure-next-auth.session-token"]; got != "rotated" {
		t.Fatalf("rotated cookie not persisted: %v", got)
	}
	if _, ok := auth.Metadata["expired"]; ok {
		t.Fatal("live auth metadata gained keys")
	}
}

func TestPerplexityOpenWebNilAuth(t *testing.T) {
	_, err := NewPerplexityExecutor(&config.Config{}).Execute(context.Background(), nil, cliproxyexecutor.Request{Model: "pplx-auto", Payload: []byte(`{"messages":[{"role":"user","content":"x"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatal("expected error for nil auth")
	}
}
