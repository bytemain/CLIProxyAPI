package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestExplicitEffortAliasPreservesExistingRoute(test *testing.T) {
	base := internalconfig.OAuthModelAlias{Name: "gpt-6.1-terra", Alias: "gpt-6.1-terra-fast", Fork: true}
	variant := internalconfig.OAuthModelAlias{Name: "gpt-6.1-terra", Alias: "gpt-6.1-terra-fast(max)", Fork: true}
	auth := &Auth{ID: "synthetic", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"codex": {base}})
	before := manager.applyOAuthModelAlias(auth, "gpt-6.1-terra-fast(max)")
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"codex": {base, variant}})
	after := manager.applyOAuthModelAlias(auth, "gpt-6.1-terra-fast(max)")
	if before != "gpt-6.1-terra(max)" || after != before {
		test.Fatalf("suffix route changed: before=%q after=%q", before, after)
	}
}

func TestEffortRequestCanUseClientWithOnlyBaseRegistration(test *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("review-base-only-auth", "codex", []*registry.ModelInfo{{ID: "review-base"}})
	test.Cleanup(func() { registryRef.UnregisterClient("review-base-only-auth") })
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "review-base-only-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	if !manager.authSupportsRouteModel(registryRef, auth, "review-base(max)") {
		test.Fatal("a base-only client was not eligible for a suffixed request")
	}
}

func TestExplicitEffortAliasCanSelectADifferentUpstream(test *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"codex": {
		{Name: "upstream-base", Alias: "review-base", Fork: true},
		{Name: "upstream-variant", Alias: "review-base(max)", Fork: true},
	}})
	auth := &Auth{ID: "synthetic", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	if got := manager.applyOAuthModelAlias(auth, "review-base(max)"); got != "upstream-variant(max)" {
		test.Fatalf("exact variant route = %q, want upstream-variant(max)", got)
	}
}
