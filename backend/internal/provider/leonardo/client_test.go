package leonardo

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

func TestAccessTokenExpiry(t *testing.T) {
	exp := int64(1_785_900_690)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	token := "e30." + payload + ".signature"
	if got := accessTokenExpiry(token, (exp+3600)*1000); got != exp {
		t.Fatalf("accessTokenExpiry() preferred JWT exp = %d, want %d", got, exp)
	}
	if got := accessTokenExpiry("opaque", exp*1000); got != exp {
		t.Fatalf("accessTokenExpiry() milliseconds = %d, want %d", got, exp)
	}
	if got := accessTokenExpiry("opaque", exp); got != exp {
		t.Fatalf("accessTokenExpiry() seconds = %d, want %d", got, exp)
	}
}

func TestIsLeonardoCookieMatchesCookieNames(t *testing.T) {
	tests := []struct {
		name   string
		cookie string
		want   bool
	}{
		{name: "secure token", cookie: "a=1; __Secure-better-auth.session_token=abc", want: true},
		{name: "chunked cache", cookie: "__Secure-better-auth.session_data.0=abc", want: true},
		{name: "host token", cookie: "__Host-better-auth.session_token=abc", want: true},
		{name: "lookalike value", cookie: "note=__Secure-better-auth.session_token", want: false},
		{name: "unrelated", cookie: "__cf_bm=abc", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsLeonardoCookie(tt.cookie); got != tt.want {
				t.Fatalf("IsLeonardoCookie() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeAuthCookiesReplacesTokenAndChunks(t *testing.T) {
	old := "analytics=keep; __Secure-better-auth.session_token=old; __Secure-better-auth.session_data.0=old0; __Secure-better-auth.session_data.1=old1; __cf_bm=keep-too"
	setCookies := []*http.Cookie{
		{Name: "__Secure-better-auth.session_token", Value: "new"},
		{Name: "__Secure-better-auth.session_data.0", Value: "new0"},
		{Name: "__Secure-better-auth.session_data.1", Value: "", MaxAge: -1},
		{Name: "tracking", Value: "ignored"},
	}

	got, changed := mergeAuthCookies(old, setCookies)
	if !changed {
		t.Fatal("mergeAuthCookies() changed = false, want true")
	}
	want := "analytics=keep; __cf_bm=keep-too; __Secure-better-auth.session_token=new"
	if got != want {
		t.Fatalf("mergeAuthCookies() = %q, want %q", got, want)
	}
}

func TestMergeAuthCookiesTokenRotationDropsStaleCache(t *testing.T) {
	old := "__Secure-better-auth.session_token=old; __Secure-better-auth.session_data.0=stale; other=keep"
	got, changed := mergeAuthCookies(old, []*http.Cookie{{Name: "__Secure-better-auth.session_token", Value: "new"}})
	if !changed {
		t.Fatal("mergeAuthCookies() changed = false, want true")
	}
	want := "other=keep; __Secure-better-auth.session_token=new"
	if got != want {
		t.Fatalf("mergeAuthCookies() = %q, want %q", got, want)
	}
}

func TestMergeAuthCookiesIgnoresUnrelatedSetCookie(t *testing.T) {
	old := "__Secure-better-auth.session_token=same; analytics=keep"
	got, changed := mergeAuthCookies(old, []*http.Cookie{
		{Name: "__cf_bm", Value: "new-edge-value", Expires: time.Now().Add(time.Hour)},
	})
	if changed {
		t.Fatal("mergeAuthCookies() changed = true, want false")
	}
	if got != old {
		t.Fatalf("mergeAuthCookies() = %q, want original %q", got, old)
	}
}

func TestMergeAuthCookiesDropsSessionDataWithoutSetCookie(t *testing.T) {
	old := "__Secure-better-auth.session_token=durable; __Secure-better-auth.session_data.0=expired-cache; analytics=keep"
	got, changed := mergeAuthCookies(old, nil)
	if !changed {
		t.Fatal("mergeAuthCookies() changed = false, want true")
	}
	want := "__Secure-better-auth.session_token=durable; analytics=keep"
	if got != want {
		t.Fatalf("mergeAuthCookies() = %q, want %q", got, want)
	}
}

func TestWithoutSessionData(t *testing.T) {
	cookie := "a=1; __Secure-better-auth.session_data.0=cache0; __Secure-better-auth.session_token=durable; __Secure-better-auth.session_data.1=cache1"
	want := "a=1; __Secure-better-auth.session_token=durable"
	if got := withoutSessionData(cookie); got != want {
		t.Fatalf("withoutSessionData() = %q, want %q", got, want)
	}
}
