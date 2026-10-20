package perplexity

import "testing"

func TestParseCredential(t *testing.T) {
	cases := []struct {
		name, in, kind, token string
	}{
		{"api key", "pplx-abcdefghijklmnopqrstuvwxyz123456", KindAPIKey, ""},
		{"bare token", "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..abc", KindCookie, "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..abc"},
		{"cookie header", "Cookie: pplx.visitor-id=1; __Secure-next-auth.session-token=tok=x; cf_clearance=cf", KindCookie, "tok=x"},
		{"json map", `{"__Secure-next-auth.session-token":"tok2","cf_clearance":"c"}`, KindCookie, "tok2"},
		{"json export", `[{"name":"__Secure-next-auth.session-token","value":"tok3"},{"name":"a","value":"b"}]`, KindCookie, "tok3"},
		{"wrapper", `{"cookie":"__Secure-next-auth.session-token=tok4"}`, KindCookie, "tok4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cred, err := ParseCredential(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cred.Kind != tc.kind {
				t.Fatalf("kind = %s", cred.Kind)
			}
			if tc.token != "" && cred.Cookies[SessionCookieName] != tc.token {
				t.Fatalf("token = %q", cred.Cookies[SessionCookieName])
			}
		})
	}
	if _, err := ParseCredential("a=b; c=d"); err == nil {
		t.Fatal("expected error for cookie header without session token")
	}
}

func TestCookiesFromMetadata(t *testing.T) {
	if got := CookiesFromMetadata(map[string]any{"session_token": "s"}); got[SessionCookieName] != "s" {
		t.Fatalf("session_token fallback: %v", got)
	}
	if got := CookiesFromMetadata(map[string]any{"cookie": "__Secure-next-auth.session-token=c; x=y"}); got["x"] != "y" {
		t.Fatalf("cookie fallback: %v", got)
	}
}
