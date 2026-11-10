package registry

import "strings"

// ModelListLimits is the token limit pair advertised for one model ID in the
// OpenAI-compatible model list. A zero value means the limit is unknown and
// must be omitted from the response.
type ModelListLimits struct {
	ContextLength       int
	MaxCompletionTokens int
}

// Clients that read these limits commonly reject implausible values (the Raft
// gateway contract ignores a context window above 10,000,000 or an output
// limit above 1,000,000 tokens). Larger declared limits are capped, which
// still never advertises more than the model accepts.
const (
	maxListContextLength       = 10_000_000
	maxListMaxCompletionTokens = 1_000_000
)

// modelListLimits returns the limits one model entry declares for the model
// list. ListLimits wins when set; otherwise a configured max-context-length
// override replaces the catalog context length, and a Gemini-style
// OutputTokenLimit stands in for a missing MaxCompletionTokens.
// InputTokenLimit is not used: it bounds the input alone, not the context
// window shared by input and output.
func modelListLimits(info *ModelInfo) ModelListLimits {
	if info == nil {
		return ModelListLimits{}
	}
	if info.ListLimits != nil {
		return *info.ListLimits
	}
	limits := ModelListLimits{ContextLength: info.ContextLength, MaxCompletionTokens: info.MaxCompletionTokens}
	if limits.MaxCompletionTokens <= 0 {
		limits.MaxCompletionTokens = info.OutputTokenLimit
	}
	if info.MaxContextLength > 0 {
		limits.ContextLength = info.MaxContextLength
	}
	return limits
}

// MergeModelListLimits returns a copy of kept whose ListLimits also cover
// dropped. Call it wherever two entries with the same model ID collapse into
// one while both remain routable, so the model list cannot advertise more
// than the smallest upstream accepts. A limit that either entry does not
// declare becomes unknown.
func MergeModelListLimits(kept, dropped *ModelInfo) *ModelInfo {
	if kept == nil {
		return kept
	}
	merged := cloneModelInfo(kept)
	a, b := modelListLimits(kept), modelListLimits(dropped)
	merged.ListLimits = &ModelListLimits{
		ContextLength:       minKnownLimit(a.ContextLength, b.ContextLength),
		MaxCompletionTokens: minKnownLimit(a.MaxCompletionTokens, b.MaxCompletionTokens),
	}
	return merged
}

// WithListContextLength returns a copy of info whose list context length is
// contextLength. Use it when an override is applied after entries were merged.
func WithListContextLength(info *ModelInfo, contextLength int) *ModelInfo {
	if info == nil || info.ListLimits == nil {
		return info
	}
	updated := *info
	limits := *info.ListLimits
	limits.ContextLength = contextLength
	updated.ListLimits = &limits
	return &updated
}

// GetModelListLimits returns the advertised token limits for every registered
// model ID.
//
// A model ID can be served by several clients whose catalogs declare different
// limits, so each limit is the smallest value declared across all of them. If
// any client serving the ID does not declare a limit, that limit is reported
// as unknown rather than taken from the clients that do. Limits above the
// accepted range are capped, and the output limit is dropped when it exceeds
// the context length.
func (r *ModelRegistry) GetModelListLimits() map[string]ModelListLimits {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	acc := make(map[string]*ModelListLimits)

	for clientID, modelIDs := range r.clientModels {
		clientInfos := r.clientModelInfos[clientID]
		for _, modelID := range modelIDs {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			info := clientInfos[modelID]
			if info == nil {
				if reg := r.models[modelID]; reg != nil {
					info = reg.Info
				}
			}
			limits := modelListLimits(info)

			entry := acc[modelID]
			if entry == nil {
				acc[modelID] = &limits
				continue
			}
			entry.ContextLength = minKnownLimit(entry.ContextLength, limits.ContextLength)
			entry.MaxCompletionTokens = minKnownLimit(entry.MaxCompletionTokens, limits.MaxCompletionTokens)
		}
	}

	out := make(map[string]ModelListLimits, len(acc))
	for modelID, entry := range acc {
		limits := *entry
		if limits.ContextLength <= 0 {
			limits.ContextLength = 0
		}
		if limits.MaxCompletionTokens <= 0 {
			limits.MaxCompletionTokens = 0
		}
		limits.ContextLength = min(limits.ContextLength, maxListContextLength)
		limits.MaxCompletionTokens = min(limits.MaxCompletionTokens, maxListMaxCompletionTokens)
		if limits.ContextLength > 0 && limits.MaxCompletionTokens > limits.ContextLength {
			limits.MaxCompletionTokens = 0
		}
		out[modelID] = limits
	}
	return out
}

// minKnownLimit returns the smaller of two limits, or 0 (unknown) when either
// is not a positive value.
func minKnownLimit(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	if b < a {
		return b
	}
	return a
}
