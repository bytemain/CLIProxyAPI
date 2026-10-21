package util

import (
	"strings"
	"testing"
)

func TestMaskSensitiveHeaderValueCookies(t *testing.T) {
	token := "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0SECRETSESSIONVALUE"
	cookie := MaskSensitiveHeaderValue("Cookie", "__Secure-next-auth.session-token="+token+"; cf_clearance=abcdefghij; a=1")
	if strings.Contains(cookie, token) || strings.Contains(cookie, "abcdefghij") {
		t.Fatalf("cookie values leaked: %s", cookie)
	}
	if !strings.Contains(cookie, "__Secure-next-auth.session-token=eyJh...ALUE") || !strings.Contains(cookie, "a=*") {
		t.Fatalf("cookie names should stay readable: %s", cookie)
	}
	setCookie := MaskSensitiveHeaderValue("Set-Cookie", "__Secure-next-auth.session-token="+token+"; Path=/; HttpOnly; Secure")
	if strings.Contains(setCookie, token) || !strings.HasSuffix(setCookie, "; Path=/; HttpOnly; Secure") {
		t.Fatalf("unexpected set-cookie mask: %s", setCookie)
	}
}
