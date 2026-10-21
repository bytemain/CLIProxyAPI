package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	perplexityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/perplexity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// PerplexityAuthenticator imports a Perplexity web session cookie or an
// official pplx- API key. There is no public OAuth flow for perplexity.ai,
// so the user pastes the credential.
type PerplexityAuthenticator struct{}

// NewPerplexityAuthenticator constructs a Perplexity authenticator.
func NewPerplexityAuthenticator() Authenticator {
	return &PerplexityAuthenticator{}
}

// Provider returns the provider key.
func (PerplexityAuthenticator) Provider() string { return perplexityauth.Provider }

// RefreshLead schedules a session touch well before the NextAuth cookie
// expires. Perplexity issues ~30 day session cookies and rotates them on
// /api/auth/session, so touching once a day keeps the cookie alive.
// API-key credentials carry no expiry and are never scheduled.
func (PerplexityAuthenticator) RefreshLead() *time.Duration {
	d := 29 * 24 * time.Hour
	return &d
}

// Login reads the credential from metadata ("credential"/"cookie"/"api_key"),
// the PERPLEXITY_COOKIE / PERPLEXITY_API_KEY environment variables, or an
// interactive prompt, validates it, and returns the auth record.
func (a PerplexityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	raw := ""
	for _, key := range []string{"credential", "cookie", "api_key"} {
		if v := strings.TrimSpace(opts.Metadata[key]); v != "" {
			raw = v
			break
		}
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("PERPLEXITY_COOKIE"))
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("PERPLEXITY_API_KEY"))
	}
	if raw == "" {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("perplexity: no credential provided (set PERPLEXITY_COOKIE or PERPLEXITY_API_KEY)")
		}
		fmt.Println("Perplexity login accepts any of:")
		fmt.Println("  - the value of the " + perplexityauth.SessionCookieName + " cookie from perplexity.ai")
		fmt.Println("  - the full Cookie header copied from browser DevTools")
		fmt.Println("  - a cookie export JSON array (Cookie-Editor / EditThisCookie)")
		fmt.Println("  - an official API key (pplx-...)")
		value, err := opts.Prompt("Paste Perplexity cookie or API key: ")
		if err != nil {
			return nil, err
		}
		raw = strings.TrimSpace(value)
	}

	record, err := perplexityauth.Validate(ctx, cfg, raw)
	if err != nil {
		return nil, err
	}
	fmt.Println("Perplexity credential validated")
	return record, nil
}
