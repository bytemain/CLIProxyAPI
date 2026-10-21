// Package perplexity implements credential handling for Perplexity.
//
// Two credential kinds are supported:
//
//   - "cookie": a logged-in perplexity.ai web session. The proxy talks to the
//     same SSE endpoint the web app uses (/rest/sse/perplexity_ask), so any
//     model available to the account's Pro/Max subscription can be used.
//   - "api_key": an official Perplexity API key (pplx-...), forwarded to the
//     OpenAI-compatible https://api.perplexity.ai/chat/completions endpoint.
package perplexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// Provider is the provider key used in auth files and routing.
	Provider = "perplexity"

	// WebBaseURL is the perplexity.ai web origin.
	WebBaseURL = "https://www.perplexity.ai"
	// SessionURL returns the NextAuth session for the cookie owner.
	SessionURL = WebBaseURL + "/api/auth/session"
	// AskURL is the streaming ask endpoint used by the web app.
	AskURL = WebBaseURL + "/rest/sse/perplexity_ask"
	// APIBaseURL is the official OpenAI-compatible API base URL.
	APIBaseURL = "https://api.perplexity.ai"

	// SessionCookieName is the cookie that carries the logged-in session.
	SessionCookieName = "__Secure-next-auth.session-token"

	// UserAgent matches the Chrome TLS fingerprint used for perplexity.ai.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

	// KindCookie marks a web-session credential.
	KindCookie = "cookie"
	// KindAPIKey marks an official API key credential.
	KindAPIKey = "api_key"
)

// Credential is a parsed user-supplied Perplexity credential.
type Credential struct {
	Kind    string
	Cookies map[string]string
	APIKey  string
}

var apiKeyPattern = regexp.MustCompile(`^pplx-[A-Za-z0-9_\-]{16,}$`)

// ParseCredential accepts any of the following and normalises it:
//
//   - an official API key: "pplx-xxxxxxxx"
//   - a bare session token value (the __Secure-next-auth.session-token cookie)
//   - a full Cookie header: "a=b; __Secure-next-auth.session-token=...; c=d"
//   - a JSON object {"name":"value",...}
//   - a JSON cookie export array [{"name":"...","value":"..."}] (EditThisCookie, Cookie-Editor)
func ParseCredential(raw string) (*Credential, error) {
	input := strings.TrimSpace(raw)
	input = strings.TrimPrefix(input, "Cookie:")
	input = strings.TrimPrefix(input, "cookie:")
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("perplexity: credential is empty")
	}
	if strings.HasPrefix(strings.ToLower(input), "bearer ") {
		input = strings.TrimSpace(input[len("bearer "):])
	}
	if apiKeyPattern.MatchString(input) {
		return &Credential{Kind: KindAPIKey, APIKey: input}, nil
	}

	cookies := map[string]string{}
	switch {
	case strings.HasPrefix(input, "["):
		var arr []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(input), &arr); err != nil {
			return nil, fmt.Errorf("perplexity: invalid cookie export JSON: %w", err)
		}
		for _, c := range arr {
			if name := strings.TrimSpace(c.Name); name != "" {
				cookies[name] = strings.TrimSpace(c.Value)
			}
		}
	case strings.HasPrefix(input, "{"):
		var obj map[string]any
		if err := json.Unmarshal([]byte(input), &obj); err != nil {
			return nil, fmt.Errorf("perplexity: invalid cookie JSON: %w", err)
		}
		// Allow {"cookie": "..."} / {"api_key": "..."} wrappers too.
		for _, key := range []string{"api_key", "cookie", "cookies", "session_token"} {
			if s, ok := obj[key].(string); ok && strings.TrimSpace(s) != "" {
				if key == "session_token" {
					return &Credential{Kind: KindCookie, Cookies: map[string]string{SessionCookieName: strings.TrimSpace(s)}}, nil
				}
				return ParseCredential(s)
			}
		}
		for k, v := range obj {
			if s, ok := v.(string); ok {
				cookies[strings.TrimSpace(k)] = strings.TrimSpace(s)
			}
		}
	case strings.Contains(input, "="):
		for _, part := range strings.Split(input, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			if name = strings.TrimSpace(name); name != "" {
				cookies[name] = strings.TrimSpace(value)
			}
		}
	default:
		cookies[SessionCookieName] = input
	}

	if strings.TrimSpace(cookies[SessionCookieName]) == "" {
		return nil, fmt.Errorf("perplexity: cookie %s not found; paste the session token, the full Cookie header, or a pplx- API key", SessionCookieName)
	}
	return &Credential{Kind: KindCookie, Cookies: cookies}, nil
}

// CookieHeader renders a deterministic Cookie header value.
func CookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	names := make([]string, 0, len(cookies))
	for name := range cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+cookies[name])
	}
	return strings.Join(parts, "; ")
}

// CookiesFromMetadata extracts the cookie jar stored in an auth record.
func CookiesFromMetadata(meta map[string]any) map[string]string {
	out := map[string]string{}
	if meta == nil {
		return out
	}
	switch v := meta["cookies"].(type) {
	case map[string]any:
		for k, val := range v {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
	case map[string]string:
		for k, val := range v {
			out[k] = val
		}
	case string:
		if parsed, err := ParseCredential(v); err == nil && parsed.Kind == KindCookie {
			out = parsed.Cookies
		}
	}
	// Hand-written auth files may only carry "cookie" or "session_token".
	if len(out) == 0 {
		if s, ok := meta["cookie"].(string); ok {
			if parsed, err := ParseCredential(s); err == nil && parsed.Kind == KindCookie {
				out = parsed.Cookies
			}
		}
	}
	if len(out) == 0 {
		if s, ok := meta["session_token"].(string); ok && strings.TrimSpace(s) != "" {
			out[SessionCookieName] = strings.TrimSpace(s)
		}
	}
	return out
}

// APIKeyFromAuth returns the official API key for api_key credentials.
func APIKeyFromAuth(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["api_key"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// SessionUser is the subset of the NextAuth session payload we care about.
type SessionUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// SessionInfo is the NextAuth session response.
type SessionInfo struct {
	User    *SessionUser `json:"user"`
	Expires string       `json:"expires"`
}

// CheckSession validates a cookie jar against /api/auth/session. The returned
// cookie jar includes any rotated cookies sent back via Set-Cookie.
func CheckSession(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, cookies map[string]string) (*SessionInfo, map[string]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SessionURL, nil)
	if err != nil {
		return nil, nil, err
	}
	ApplyWebHeaders(req, cookies)
	req.Header.Set("Accept", "application/json")

	client := helps.NewUtlsHTTPClient(ctx, cfg, auth, 30*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("perplexity: session request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("perplexity: close session response body error: %v", errClose)
		}
	}()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, nil, &HTTPError{Code: resp.StatusCode, Body: truncate(string(body), 300)}
	}
	var info SessionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, nil, fmt.Errorf("perplexity: unexpected session response (Cloudflare challenge?): %s", truncate(string(body), 200))
	}
	if info.User == nil {
		return nil, nil, &HTTPError{Code: http.StatusUnauthorized, Body: "perplexity session is not authenticated (cookie expired or invalid)"}
	}
	return &info, MergeSetCookies(cookies, resp), nil
}

// HTTPError is a non-2xx response from perplexity.ai.
type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("perplexity: HTTP %d: %s", e.Code, e.Body)
}

// StatusCode satisfies the status-code interface used by the conductor.
func (e *HTTPError) StatusCode() int { return e.Code }

// MergeSetCookies returns a copy of cookies updated with Set-Cookie values.
func MergeSetCookies(cookies map[string]string, resp *http.Response) map[string]string {
	merged := make(map[string]string, len(cookies))
	for k, v := range cookies {
		merged[k] = v
	}
	if resp == nil {
		return merged
	}
	for _, c := range resp.Cookies() {
		if c == nil || c.Name == "" {
			continue
		}
		if c.MaxAge < 0 || c.Value == "" {
			// Never drop the session token on a stray clear; let the next
			// request fail loudly instead.
			if c.Name != SessionCookieName {
				delete(merged, c.Name)
			}
			continue
		}
		merged[c.Name] = c.Value
	}
	return merged
}

// ApplyWebHeaders sets browser-like headers for perplexity.ai requests.
func ApplyWebHeaders(req *http.Request, cookies map[string]string) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", WebBaseURL)
	req.Header.Set("Referer", WebBaseURL+"/")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="140", "Not=A?Brand";v="24", "Google Chrome";v="140"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if header := CookieHeader(cookies); header != "" {
		req.Header.Set("Cookie", header)
	}
}

// CredentialFileName builds a stable auth file name.
func CredentialFileName(kind, identity, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	short := hex.EncodeToString(sum[:])[:8]
	identity = sanitize(identity)
	if identity == "" {
		return fmt.Sprintf("perplexity-%s-%s.json", kind, short)
	}
	return fmt.Sprintf("perplexity-%s-%s.json", identity, short)
}

// BuildAuth converts a validated credential into an auth record.
func BuildAuth(cred *Credential, info *SessionInfo, cookies map[string]string) *coreauth.Auth {
	now := time.Now().Format(time.RFC3339)
	metadata := map[string]any{
		"type":         Provider,
		"auth_kind":    cred.Kind,
		"last_refresh": now,
	}
	attrs := map[string]string{"auth_kind": cred.Kind}
	var fileName, label string
	if cred.Kind == KindAPIKey {
		metadata["api_key"] = cred.APIKey
		metadata["base_url"] = APIBaseURL
		attrs["api_key"] = cred.APIKey
		attrs["base_url"] = APIBaseURL
		fileName = CredentialFileName("apikey", "", cred.APIKey)
		label = "Perplexity API (" + maskKey(cred.APIKey) + ")"
	} else {
		if cookies == nil {
			cookies = cred.Cookies
		}
		metadata["cookies"] = cookies
		email := ""
		if info != nil {
			if info.Expires != "" {
				metadata["expired"] = info.Expires
			}
			if info.User != nil {
				email = strings.TrimSpace(info.User.Email)
				metadata["email"] = email
				metadata["name"] = info.User.Name
				if info.User.ID != "" {
					metadata["user_id"] = info.User.ID
				}
			}
		}
		if email != "" {
			attrs["email"] = email
		}
		fileName = CredentialFileName("web", email, cred.Cookies[SessionCookieName])
		label = email
		if label == "" {
			label = "Perplexity Web"
		}
	}
	return &coreauth.Auth{
		ID:         fileName,
		Provider:   Provider,
		FileName:   fileName,
		Label:      label,
		Metadata:   metadata,
		Attributes: attrs,
	}
}

// Validate parses raw input and, for cookies, checks the live session.
func Validate(ctx context.Context, cfg *config.Config, raw string) (*coreauth.Auth, error) {
	cred, err := ParseCredential(raw)
	if err != nil {
		return nil, err
	}
	if cred.Kind == KindAPIKey {
		return BuildAuth(cred, nil, nil), nil
	}
	info, cookies, err := CheckSession(ctx, cfg, nil, cred.Cookies)
	if err != nil {
		return nil, err
	}
	return BuildAuth(cred, info, cookies), nil
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == '@':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "._-")
}

func maskKey(k string) string {
	if len(k) <= 12 {
		return "pplx-***"
	}
	return k[:8] + "..." + k[len(k)-4:]
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
