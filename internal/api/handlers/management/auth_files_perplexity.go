package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	perplexityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/perplexity"
)

// ImportPerplexityCredential stores a Perplexity web session cookie or a
// pplx- API key.
//
//	POST /v8/oauth/import?provider=perplexity
//	{"credential": "<session token | Cookie header | cookie JSON | pplx-key>"}
//
// "cookie" and "api_key" are accepted as aliases of "credential", either as
// JSON fields or form values. Cookie credentials are validated against
// perplexity.ai before being saved.
func (h *Handler) ImportPerplexityCredential(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	if h.cfg.AuthDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth directory not configured"})
		return
	}

	raw := ""
	if strings.Contains(c.ContentType(), "json") {
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "message": err.Error()})
			return
		}
		for _, key := range []string{"credential", "cookie", "api_key", "session_token"} {
			if v, ok := body[key].(string); ok && strings.TrimSpace(v) != "" {
				raw = v
				break
			}
		}
	} else {
		for _, key := range []string{"credential", "cookie", "api_key", "session_token"} {
			if v := strings.TrimSpace(c.PostForm(key)); v != "" {
				raw = v
				break
			}
		}
	}
	if strings.TrimSpace(raw) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credential required"})
		return
	}

	ctx := context.Background()
	if reqCtx := c.Request.Context(); reqCtx != nil {
		ctx = reqCtx
	}
	record, err := perplexityauth.Validate(ctx, h.cfg, raw)
	if err != nil {
		status := http.StatusBadRequest
		if se, ok := err.(interface{ StatusCode() int }); ok && se.StatusCode() >= 500 {
			status = http.StatusBadGateway
		}
		c.JSON(status, gin.H{"error": "invalid_credential", "message": err.Error()})
		return
	}
	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed", "message": err.Error()})
		return
	}
	kind, _ := record.Metadata["auth_kind"].(string)
	email, _ := record.Metadata["email"].(string)
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"auth-file": savedPath,
		"kind":      kind,
		"email":     email,
		"label":     record.Label,
	})
}
