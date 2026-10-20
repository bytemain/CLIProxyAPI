package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	perplexityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/perplexity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	perplexityWebAPIVersion = "2.18"
	perplexityMaxQueryChars = 96000
)

// PerplexityExecutor serves Perplexity credentials.
//
// Cookie credentials talk to the perplexity.ai web SSE endpoint, so every
// model in the account's Pro/Max subscription is available. API-key
// credentials (pplx-...) are forwarded to the official OpenAI-compatible API.
type PerplexityExecutor struct {
	cfg    *config.Config
	compat *OpenAICompatExecutor
}

// NewPerplexityExecutor creates a Perplexity executor.
func NewPerplexityExecutor(cfg *config.Config) *PerplexityExecutor {
	return &PerplexityExecutor{cfg: cfg, compat: NewOpenAICompatExecutor("perplexity", cfg)}
}

// Identifier returns the provider key.
func (e *PerplexityExecutor) Identifier() string { return perplexityauth.Provider }

// PrepareRequest injects Perplexity credentials into an outgoing request.
func (e *PerplexityExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if key := perplexityauth.APIKeyFromAuth(auth); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else if auth != nil {
		perplexityauth.ApplyWebHeaders(req, perplexityauth.CookiesFromMetadata(auth.Metadata))
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes an arbitrary request with Perplexity credentials.
func (e *PerplexityExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("perplexity executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// CountTokens estimates tokens locally on the OpenAI-translated request.
func (e *PerplexityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.compat.CountTokens(ctx, auth, req, opts)
}

// Refresh touches the web session so Perplexity rotates the session cookie.
func (e *PerplexityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "perplexity executor: auth is nil"}
	}
	if perplexityauth.APIKeyFromAuth(auth) != "" {
		return auth, nil
	}
	cookies := perplexityauth.CookiesFromMetadata(auth.Metadata)
	if cookies[perplexityauth.SessionCookieName] == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "perplexity executor: missing session cookie"}
	}
	info, merged, err := perplexityauth.CheckSession(ctx, e.cfg, auth, cookies)
	if err != nil {
		return nil, err
	}
	// Never mutate the live auth: concurrent requests read its metadata.
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	updated.Metadata["cookies"] = merged
	updated.Metadata["type"] = perplexityauth.Provider
	updated.Metadata["auth_kind"] = perplexityauth.KindCookie
	delete(updated.Metadata, "cookie")
	delete(updated.Metadata, "session_token")
	if info.Expires != "" {
		updated.Metadata["expired"] = info.Expires
	}
	if info.User != nil && info.User.Email != "" {
		updated.Metadata["email"] = info.User.Email
	}
	now := time.Now()
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339)
	updated.LastRefreshedAt = now
	return updated, nil
}

// Execute performs a non-streaming request.
func (e *PerplexityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if key := perplexityauth.APIKeyFromAuth(auth); key != "" {
		return e.compat.executeWithCredentials(ctx, auth, req, opts, perplexityauth.APIBaseURL, key, nil)
	}
	prepared, err := e.prepareWeb(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetUpstreamModel(prepared.preference)

	var answer, reasoning strings.Builder
	_, tail, err := e.runWeb(ctx, auth, prepared, func(ev helps.PerplexityEvent) error {
		answer.WriteString(ev.Content)
		reasoning.WriteString(ev.Reasoning)
		return nil
	})
	if err != nil {
		return resp, err
	}
	answer.WriteString(tail)

	message := map[string]any{"role": "assistant", "content": answer.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	usageMap := prepared.usage(answer.String() + reasoning.String())
	completion := map[string]any{
		"id":      prepared.id,
		"object":  "chat.completion",
		"created": prepared.created,
		"model":   req.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}},
		"usage":   usageMap,
	}
	data, _ := json.Marshal(completion)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, data, &param)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out}, nil
}

// ExecuteStream performs a streaming request.
func (e *PerplexityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if key := perplexityauth.APIKeyFromAuth(auth); key != "" {
		return e.compat.executeStreamWithCredentials(ctx, auth, req, opts, perplexityauth.APIBaseURL, key, nil)
	}
	prepared, err := e.prepareWeb(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetUpstreamModel(prepared.preference)

	// Open the upstream stream synchronously so auth/HTTP errors surface as
	// regular errors (enabling credential rotation) instead of stream chunks.
	httpResp, err := e.openWeb(ctx, auth, prepared)
	if err != nil {
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("perplexity executor: close response body error: %v", errClose)
			}
		}()
		var param any
		original := helps.ApplyPatchOriginalRequest(req, opts)
		send := func(line []byte) bool {
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			for _, chunk := range sdktranslator.TranslateStream(ctx, prepared.to, prepared.responseFormat, req.Model, original, prepared.translated, line, &param) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		var produced strings.Builder
		first := true
		emit := func(ev helps.PerplexityEvent) error {
			delta := map[string]any{}
			if first {
				delta["role"] = "assistant"
				first = false
			}
			if ev.Reasoning != "" {
				delta["reasoning_content"] = ev.Reasoning
			}
			if ev.Content != "" {
				delta["content"] = ev.Content
			}
			produced.WriteString(ev.Reasoning)
			produced.WriteString(ev.Content)
			if !send(prepared.chunk(delta, nil, nil)) {
				return ctx.Err()
			}
			return nil
		}
		_, tail, errStream := e.consumeWeb(ctx, httpResp, prepared, emit)
		if errStream != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errStream)
			reporter.PublishFailure(ctx, errStream)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errStream}:
			case <-ctx.Done():
			}
			return
		}
		if tail != "" {
			if errEmit := emit(helps.PerplexityEvent{Content: tail}); errEmit != nil {
				return
			}
		}
		stop := "stop"
		usageMap := prepared.usage(produced.String())
		finalChunk := prepared.chunk(map[string]any{}, &stop, usageMap)
		if !send(finalChunk) {
			return
		}
		usageJSON, _ := json.Marshal(map[string]any{"usage": usageMap})
		reporter.Publish(ctx, helps.ParseOpenAIUsage(usageJSON))
		send([]byte("data: [DONE]"))
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

type perplexityPrepared struct {
	baseModel      string
	preference     string
	mode           string
	query          string
	payload        []byte
	translated     []byte
	to             sdktranslator.Format
	responseFormat sdktranslator.Format
	id             string
	created        int64
	model          string
	citations      bool
	promptTokens   int
}

func (p *perplexityPrepared) chunk(delta map[string]any, finish *string, usage map[string]any) []byte {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	body := map[string]any{
		"id":      p.id,
		"object":  "chat.completion.chunk",
		"created": p.created,
		"model":   p.model,
		"choices": []any{choice},
	}
	if usage != nil {
		body["usage"] = usage
	}
	data, _ := json.Marshal(body)
	return append([]byte("data: "), data...)
}

func (p *perplexityPrepared) usage(completion string) map[string]any {
	completionTokens := helps.EstimateTokens(p.baseModel, completion)
	return map[string]any{
		"prompt_tokens":     p.promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      p.promptTokens + completionTokens,
	}
}

func (e *PerplexityExecutor) prepareWeb(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*perplexityPrepared, error) {
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	translated, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream, false)
	if err != nil {
		return nil, err
	}

	model, wantThinking, ok := registry.LookupPerplexityWebModel(baseModel)
	if !ok {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("perplexity: unknown model %q", baseModel)}
	}
	if !wantThinking && model.Thinking != "" {
		suffix := thinking.ParseSuffix(req.Model)
		effort := strings.ToLower(gjson.GetBytes(translated, "reasoning_effort").String())
		if (effort != "" && effort != "none" && effort != "minimal") || (suffix.HasSuffix && suffix.RawSuffix != "none") {
			wantThinking = true
		}
	}
	preference := model.Preference
	if wantThinking {
		preference = model.Thinking
	}
	mode := "copilot"
	if preference == "pplx_pro" {
		mode = "concise"
	}

	query, err := helps.BuildPerplexityQuery(translated, perplexityMaxQueryChars)
	if err != nil {
		return nil, err
	}

	meta := map[string]any{}
	if auth != nil && auth.Metadata != nil {
		meta = auth.Metadata
	}
	sources := []string{"web"}
	if raw, okSources := meta["sources"].([]any); okSources {
		sources = sources[:0]
		for _, s := range raw {
			if str, okStr := s.(string); okStr && str != "" {
				sources = append(sources, str)
			}
		}
	}
	language := helps.PerplexityMetaString(meta, "language", "en-US")
	timezone := helps.PerplexityMetaString(meta, "timezone", "UTC")
	// Incognito by default: a shared credential must not write proxied
	// queries into the owner's library or feed account memory and
	// personalisation back into other users' answers.
	incognito := true
	if v, okIncognito := meta["incognito"].(bool); okIncognito {
		incognito = v
	}
	// The Sources footer is opt-in; API/agent clients mostly treat it as noise.
	citations := false
	if v, okCitations := meta["citations"].(bool); okCitations {
		citations = v
	}

	payload := map[string]any{
		"query_str": query,
		"params": map[string]any{
			"attachments":                     []any{},
			"frontend_context_uuid":           uuid.NewString(),
			"frontend_uuid":                   uuid.NewString(),
			"is_incognito":                    incognito,
			"language":                        language,
			"last_backend_uuid":               nil,
			"mode":                            mode,
			"model_preference":                preference,
			"source":                          "default",
			"sources":                         sources,
			"search_focus":                    "internet",
			"search_recency_filter":           nil,
			"timezone":                        timezone,
			"visitor_id":                      uuid.NewString(),
			"prompt_source":                   "user",
			"query_source":                    "home",
			"browser_history_summary":         []any{},
			"is_related_query":                false,
			"is_sponsored":                    false,
			"is_nav_suggestions_disabled":     false,
			"use_schematized_api":             true,
			"send_back_text_in_streaming_api": false,
			"supported_block_use_cases": []string{
				"answer_modes", "media_items", "knowledge_cards", "inline_entity_cards",
				"place_widgets", "finance_widgets", "sports_widgets", "shopping_widgets",
				"search_result_widgets",
			},
			"client_coordinates": nil,
			"version":            helps.PerplexityMetaString(meta, "api_version", perplexityWebAPIVersion),
		},
	}
	body, _ := json.Marshal(payload)
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, perplexityauth.Provider, from.String(), "", body, bytes.Clone(originalPayload), helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)

	return &perplexityPrepared{
		baseModel:      baseModel,
		preference:     preference,
		mode:           mode,
		query:          query,
		payload:        body,
		translated:     translated,
		to:             to,
		responseFormat: cliproxyexecutor.ResponseFormatOrSource(opts),
		id:             "chatcmpl-" + helps.PerplexityRandomHex(12),
		created:        time.Now().Unix(),
		model:          req.Model,
		citations:      citations,
		promptTokens:   helps.EstimateTokens(baseModel, query),
	}, nil
}

func (e *PerplexityExecutor) runWeb(ctx context.Context, auth *cliproxyauth.Auth, p *perplexityPrepared, emit func(helps.PerplexityEvent) error) (*helps.PerplexityStreamResult, string, error) {
	httpResp, err := e.openWeb(ctx, auth, p)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("perplexity executor: close response body error: %v", errClose)
		}
	}()
	return e.consumeWeb(ctx, httpResp, p, emit)
}

// consumeWeb decodes the upstream stream and appends the citation footer.
func (e *PerplexityExecutor) consumeWeb(ctx context.Context, httpResp *http.Response, p *perplexityPrepared, emit func(helps.PerplexityEvent) error) (*helps.PerplexityStreamResult, string, error) {
	result, err := helps.ConsumePerplexityStream(ctx, httpResp.Body, p.preference, emit)
	if err != nil {
		return nil, "", err
	}
	tail := ""
	if p.citations {
		tail = helps.PerplexityCitations(result.WebResults, 10)
	}
	return result, tail, nil
}

func (e *PerplexityExecutor) openWeb(ctx context.Context, auth *cliproxyauth.Auth, p *perplexityPrepared) (*http.Response, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "perplexity: auth is nil"}
	}
	cookies := perplexityauth.CookiesFromMetadata(auth.Metadata)
	if cookies[perplexityauth.SessionCookieName] == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "perplexity: auth file has no session cookie"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, perplexityauth.AskURL, bytes.NewReader(p.payload))
	if err != nil {
		return nil, err
	}
	perplexityauth.ApplyWebHeaders(httpReq, cookies)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)

	authType, authValue := auth.AccountInfo()
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       perplexityauth.AskURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      p.payload,
		Provider:  e.Identifier(),
		AuthID:    auth.ID,
		AuthLabel: auth.Label,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpResp, err := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64<<10))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("perplexity executor: close response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		msg := strings.TrimSpace(string(b))
		if httpResp.StatusCode == http.StatusForbidden && strings.Contains(msg, "cf-") {
			msg = "perplexity: blocked by Cloudflare (try adding cf_clearance to the cookie jar or use a different proxy)"
		}
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: msg}
	}
	return httpResp, nil
}
