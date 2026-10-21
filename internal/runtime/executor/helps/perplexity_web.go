package helps

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// PerplexityStatusError is an HTTP-status-bearing error for Perplexity requests.
type PerplexityStatusError struct {
	Code int
	Msg  string
}

func (e *PerplexityStatusError) Error() string { return e.Msg }

// StatusCode returns the HTTP status associated with the error.
func (e *PerplexityStatusError) StatusCode() int { return e.Code }

// PerplexityEvent is one decoded increment of a perplexity_ask stream.
type PerplexityEvent struct {
	Content   string
	Reasoning string
}

// PerplexityStreamResult summarises a completed perplexity_ask stream.
type PerplexityStreamResult struct {
	Answer       string
	DisplayModel string
	WebResults   []gjson.Result
}

// PerplexityCitations renders up to max web results as a "Sources" footer.
func PerplexityCitations(results []gjson.Result, max int) string {
	if len(results) == 0 {
		return ""
	}
	var tail strings.Builder
	tail.WriteString("\n\nSources:\n")
	for i, r := range results {
		if max > 0 && i >= max {
			break
		}
		fmt.Fprintf(&tail, "[%d] %s - %s\n", i+1, r.Get("name").String(), r.Get("url").String())
	}
	return strings.TrimRight(tail.String(), "\n")
}

// EstimateTokens counts tokens with the model tokenizer, falling back to a
// 4-chars-per-token heuristic.
func EstimateTokens(model, text string) int {
	if text == "" {
		return 0
	}
	if enc, err := TokenizerForModel(model); err == nil {
		if n, errCount := enc.Count(text); errCount == nil {
			return n
		}
	}
	return len(text) / 4
}

func PerplexityMetaString(meta map[string]any, key, fallback string) string {
	if v, ok := meta[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func PerplexityRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// BuildPerplexityQuery flattens an OpenAI chat request into the single query
// string perplexity.ai accepts. Multi-turn context and system prompts are sent
// as a JSON envelope {"query","instructions","history"}, which the web models
// follow reliably.
func BuildPerplexityQuery(body []byte, maxChars int) (string, error) {
	type turn struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var instructions []string
	var history []turn
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		role := msg.Get("role").String()
		text := perplexityContentText(msg.Get("content"))
		switch role {
		case "system", "developer":
			if strings.TrimSpace(text) != "" {
				instructions = append(instructions, text)
			}
			continue
		case "tool":
			role = "user"
			text = fmt.Sprintf("[tool result %s]: %s", msg.Get("tool_call_id").String(), text)
		case "assistant":
			for _, call := range msg.Get("tool_calls").Array() {
				text += fmt.Sprintf("\n[called tool %s(%s)]", call.Get("function.name").String(), call.Get("function.arguments").String())
			}
		default:
			role = "user"
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if n := len(history); n > 0 && history[n-1].Role == role {
			history[n-1].Content += "\n\n" + text
			continue
		}
		history = append(history, turn{Role: role, Content: text})
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return "", &PerplexityStatusError{Code: http.StatusBadRequest, Msg: "perplexity: a non-empty final user message is required"}
	}
	current := history[len(history)-1].Content
	history = history[:len(history)-1]

	query := current
	if len(instructions) > 0 || len(history) > 0 {
		envelope := map[string]any{"query": current}
		if len(instructions) > 0 {
			envelope["instructions"] = instructions
		}
		if len(history) > 0 {
			envelope["history"] = history
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(envelope)
		query = strings.TrimSpace(buf.String())
	}
	if maxChars > 0 && len(query) > maxChars {
		return "", &PerplexityStatusError{Code: http.StatusBadRequest, Msg: fmt.Sprintf("perplexity: conversation exceeds the %d-character limit", maxChars)}
	}
	return query, nil
}

func perplexityContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text", "":
			if t := part.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		case "image_url", "input_image":
			parts = append(parts, "[image omitted]")
		}
	}
	return strings.Join(parts, "\n")
}

// ConsumePerplexityStream decodes the perplexity_ask SSE stream. Answer text arrives in
// "ask_text*" markdown blocks either as incremental chunks or as a DONE
// snapshot; search progress arrives in plan/pro_search_steps/web_results
// blocks and is surfaced as reasoning.
func ConsumePerplexityStream(ctx context.Context, body io.Reader, preference string, emit func(PerplexityEvent) error) (*PerplexityStreamResult, error) {
	reader := bufio.NewReaderSize(body, 64<<10)
	result := &PerplexityStreamResult{}
	var (
		event       = "message"
		dataLines   []string
		answer      string
		answerUsage string
		seen        = map[string]bool{}
		complete    bool
	)

	handle := func(ev string, data string) error {
		switch ev {
		case "end_of_stream":
			complete = true
			return nil
		case "error":
			return &PerplexityStatusError{Code: http.StatusBadGateway, Msg: "perplexity stream error: " + perplexityTruncate(data, 500)}
		case "message":
		default:
			return nil
		}
		if strings.TrimSpace(data) == "" {
			return nil
		}
		chunk := gjson.Parse(data)
		if !chunk.IsObject() {
			return &PerplexityStatusError{Code: http.StatusBadGateway, Msg: "perplexity: malformed SSE message"}
		}
		if errMsg := chunk.Get("error"); errMsg.Exists() && errMsg.String() != "" {
			return &PerplexityStatusError{Code: http.StatusBadGateway, Msg: "perplexity stream error: " + perplexityTruncate(errMsg.String(), 500)}
		}
		if dm := chunk.Get("display_model").String(); dm != "" {
			if result.DisplayModel == "" && dm != preference {
				log.Debugf("perplexity: requested %s, answered by %s", preference, dm)
			}
			result.DisplayModel = dm
		}
		if wr := chunk.Get("web_results"); wr.IsArray() {
			result.WebResults = wr.Array()
		}

		var reasoning strings.Builder
		addReasoning := func(key, line string) {
			if key == "" || seen[key] {
				return
			}
			seen[key] = true
			reasoning.WriteString(line)
			reasoning.WriteString("\n")
		}
		var answerBlocks []gjson.Result
		for _, block := range chunk.Get("blocks").Array() {
			usage := block.Get("intended_usage").String()
			switch usage {
			case "pro_search_steps":
				for _, step := range block.Get("plan_block.steps").Array() {
					switch step.Get("step_type").String() {
					case "SEARCH_WEB":
						for _, q := range step.Get("search_web_content.queries").Array() {
							if s := q.Get("query").String(); s != "" {
								addReasoning("q:"+s, "Searching: "+s)
							}
						}
					case "READ_RESULTS":
						for i, u := range step.Get("read_results_content.urls").Array() {
							if i >= 3 {
								break
							}
							addReasoning("r:"+u.String(), "Reading: "+u.String())
						}
					}
				}
			case "plan":
				for _, goal := range block.Get("plan_block.goals").Array() {
					if d := goal.Get("description").String(); d != "" {
						addReasoning("g:"+d, d)
					}
				}
			case "web_results":
				results := block.Get("web_result_block.web_results").Array()
				if len(results) > 0 {
					result.WebResults = results
				}
			}
			if strings.HasPrefix(usage, "ask_text") && block.Get("markdown_block").Exists() {
				answerBlocks = append(answerBlocks, block)
			}
		}

		ev2 := PerplexityEvent{Reasoning: reasoning.String()}
		if len(answerBlocks) > 0 {
			// The same answer may be mirrored in ask_text and
			// ask_text_0_markdown; lock onto one to avoid duplicates.
			var selected *gjson.Result
			for i := range answerBlocks {
				if answerUsage != "" && answerBlocks[i].Get("intended_usage").String() == answerUsage {
					selected = &answerBlocks[i]
					break
				}
			}
			if selected == nil && answerUsage == "" {
				selected = &answerBlocks[0]
				for i := range answerBlocks {
					if answerBlocks[i].Get("intended_usage").String() == "ask_text" {
						selected = &answerBlocks[i]
						break
					}
				}
				answerUsage = selected.Get("intended_usage").String()
			}
			if selected != nil {
				mb := selected.Get("markdown_block")
				var text strings.Builder
				for _, c := range mb.Get("chunks").Array() {
					text.WriteString(c.String())
				}
				if mb.Get("progress").String() == "DONE" {
					final := text.String()
					if strings.HasPrefix(final, answer) {
						ev2.Content = final[len(answer):]
						answer = final
					}
				} else if text.Len() > 0 {
					ev2.Content = text.String()
					answer += ev2.Content
				}
			}
		}
		if ev2.Content == "" && ev2.Reasoning == "" {
			return nil
		}
		return emit(ev2)
	}

	for {
		line, errRead := reader.ReadString('\n')
		if len(line) > 0 || errRead == nil {
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if len(dataLines) > 0 || event != "message" {
					if err := handle(event, strings.Join(dataLines, "\n")); err != nil {
						return nil, err
					}
				}
				event, dataLines = "message", nil
				if complete {
					break
				}
			} else if !strings.HasPrefix(line, ":") {
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					event = value
				case "data":
					dataLines = append(dataLines, value)
				}
			}
		}
		if errRead != nil {
			if errors.Is(errRead, io.EOF) {
				if len(dataLines) > 0 {
					if err := handle(event, strings.Join(dataLines, "\n")); err != nil {
						return nil, err
					}
				}
				break
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("perplexity: read stream: %w", errRead)
		}
	}
	if strings.TrimSpace(answer) == "" {
		return nil, &PerplexityStatusError{Code: http.StatusBadGateway, Msg: "perplexity returned an empty answer"}
	}
	result.Answer = answer
	return result, nil
}

func perplexityTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
