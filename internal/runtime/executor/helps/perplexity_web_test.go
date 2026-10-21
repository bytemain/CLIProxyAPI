package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildPerplexityQueryToolTurns(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"sunny"}
	]}`
	q, err := BuildPerplexityQuery([]byte(body), 0)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	env := gjson.Parse(q)
	if !strings.Contains(env.Get("query").String(), "[tool result c1]: sunny") {
		t.Fatalf("tool result should become the current user turn: %s", q)
	}
	if !strings.Contains(env.Get("history.1.content").String(), "get_weather") {
		t.Fatalf("tool call should be kept in history: %s", q)
	}
}

func TestBuildPerplexityQueryLimits(t *testing.T) {
	if _, err := BuildPerplexityQuery([]byte(`{"messages":[{"role":"system","content":"x"}]}`), 0); err == nil {
		t.Fatal("expected error without user message")
	}
	long := `{"messages":[{"role":"user","content":"` + strings.Repeat("a", 50) + `"}]}`
	if _, err := BuildPerplexityQuery([]byte(long), 10); err == nil {
		t.Fatal("expected length error")
	}
}
