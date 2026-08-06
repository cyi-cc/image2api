package leonardo

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func TestAuthErrorMatchesErrAuthAndKeepsSafeDetail(t *testing.T) {
	err := &AuthError{
		Code:   "get_session_http_401",
		Detail: "get-session 返回 HTTP 401，session cookie 已失效或被拒绝",
	}
	if !errors.Is(err, ErrAuth) {
		t.Fatal("AuthError must match ErrAuth")
	}
	if got := err.Error(); got != "leonardo auth failed: get-session 返回 HTTP 401，session cookie 已失效或被拒绝" {
		t.Fatalf("AuthError.Error() = %q", got)
	}
}

func TestIsLeonardoCookieMatchesCookieNames(t *testing.T) {
	tests := []struct {
		name   string
		cookie string
		want   bool
	}{
		{name: "secure token", cookie: "a=1; __Secure-better-auth.session_token=abc", want: true},
		{name: "chunked cache only", cookie: "__Secure-better-auth.session_data.0=abc", want: false},
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
	want := "analytics=keep; __cf_bm=keep-too; __Secure-better-auth.session_token=new; __Secure-better-auth.session_data.0=new0"
	if got != want {
		t.Fatalf("mergeAuthCookies() = %q, want %q", got, want)
	}
}

func TestMergeAuthCookiesTokenRotationKeepsUntouchedCache(t *testing.T) {
	old := "__Secure-better-auth.session_token=old; __Secure-better-auth.session_data.0=stale; other=keep"
	got, changed := mergeAuthCookies(old, []*http.Cookie{{Name: "__Secure-better-auth.session_token", Value: "new"}})
	if !changed {
		t.Fatal("mergeAuthCookies() changed = false, want true")
	}
	want := "__Secure-better-auth.session_data.0=stale; other=keep; __Secure-better-auth.session_token=new"
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

func TestMergeAuthCookiesKeepsSessionDataWithoutSetCookie(t *testing.T) {
	old := "__Secure-better-auth.session_token=durable; __Secure-better-auth.session_data.0=expired-cache; analytics=keep"
	got, changed := mergeAuthCookies(old, nil)
	if changed {
		t.Fatal("mergeAuthCookies() changed = true, want false")
	}
	if got != old {
		t.Fatalf("mergeAuthCookies() = %q, want %q", got, old)
	}
}

func TestSessionEndpointUsesSupportedCacheBypass(t *testing.T) {
	if got, want := sessionEndpoint(false), appBase+"/api/auth/get-session"; got != want {
		t.Fatalf("sessionEndpoint(false) = %q, want %q", got, want)
	}
	if got, want := sessionEndpoint(true), appBase+"/api/auth/get-session?disableCookieCache=true"; got != want {
		t.Fatalf("sessionEndpoint(true) = %q, want %q", got, want)
	}
}

func TestAuthSessionCookiesDropsBrowserBoundFingerprintState(t *testing.T) {
	cookie := "__cf_bm=browser-bound; CF_Access_Token=edge-bound; __Secure-better-auth.session_token=durable; analytics=ignored; __Secure-better-auth.session_data.0=cache0; __Secure-better-auth.session_data.1=cache1"
	want := "__Secure-better-auth.session_token=durable; __Secure-better-auth.session_data.0=cache0; __Secure-better-auth.session_data.1=cache1"
	if got := authSessionCookies(cookie); got != want {
		t.Fatalf("authSessionCookies() = %q, want %q", got, want)
	}
}

func TestFreshSessionSingleflightCoalescesSameAccount(t *testing.T) {
	client := NewClient("")
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	client.fetchSessionHook = func(ctx context.Context, cookie string, forceFresh bool) (*Session, []*http.Cookie, error) {
		if !forceFresh {
			t.Fatal("singleflight hook called without forceFresh")
		}
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-release:
		}
		return &Session{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil, nil
	}

	const workers = 20
	begin := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-begin
			cookie := fmt.Sprintf("analytics=%d; __Secure-better-auth.session_token=same-account; __cf_bm=edge-%d; __Secure-better-auth.session_data.0=cache-%d", index, index, index)
			_, _, err := client.RefreshCookie(context.Background(), cookie)
			errs <- err
		}(i)
	}
	close(begin)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream refresh did not start")
	}
	// Let every worker join the already-running account flight.
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RefreshCookie() error = %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream refresh calls = %d, want 1", got)
	}
}

func TestFreshSessionSingleflightSeparatesAccounts(t *testing.T) {
	client := NewClient("")
	var calls atomic.Int32
	client.fetchSessionHook = func(context.Context, string, bool) (*Session, []*http.Cookie, error) {
		calls.Add(1)
		return &Session{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil, nil
	}
	for _, token := range []string{"account-a", "account-b"} {
		if _, _, err := client.RefreshCookie(context.Background(), "__Secure-better-auth.session_token="+token); err != nil {
			t.Fatalf("RefreshCookie(%s) error = %v", token, err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream refresh calls = %d, want 2", got)
	}
}

func TestFreshSessionWaiterCancellationDoesNotCancelSharedRequest(t *testing.T) {
	client := NewClient("")
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	upstreamDone := make(chan error, 1)
	client.fetchSessionHook = func(ctx context.Context, _ string, _ bool) (*Session, []*http.Cookie, error) {
		calls.Add(1)
		close(started)
		<-release
		upstreamDone <- ctx.Err()
		return &Session{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil, nil
	}

	cookie := "__Secure-better-auth.session_token=cancel-account"
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := client.RefreshCookie(leaderCtx, cookie)
		leaderErr <- err
	}()
	<-started

	waiterErr := make(chan error, 1)
	go func() {
		_, _, err := client.RefreshCookie(context.Background(), cookie+"; __cf_bm=different")
		waiterErr <- err
	}()
	// Give the second caller time to join the already-running flight before the
	// first caller abandons its own wait.
	time.Sleep(25 * time.Millisecond)
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-waiterErr; err != nil {
		t.Fatalf("remaining waiter error = %v", err)
	}
	if err := <-upstreamDone; err != nil {
		t.Fatalf("shared upstream context was canceled: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream refresh calls = %d, want 1", got)
	}
}

func TestSessionAccountKeyRequiresDurableToken(t *testing.T) {
	a, okA := sessionAccountKey("__Secure-better-auth.session_token=same; __cf_bm=one")
	b, okB := sessionAccountKey("__Secure-better-auth.session_data.0=cache; __Host-better-auth.session_token=same; analytics=two")
	if !okA || !okB || a != b {
		t.Fatalf("same durable token keys differ: %q/%v vs %q/%v", a, okA, b, okB)
	}
	if _, ok := sessionAccountKey("__Secure-better-auth.session_data.0=cache-only"); ok {
		t.Fatal("sessionAccountKey accepted cookie without session_token")
	}
}
