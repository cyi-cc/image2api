package adobe

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	refreshURL = "https://adobeid-na1.services.adobe.com/ims/check/v6/token?jslVersion=v2-v0.48.0-1-g1e322cb"
	clientID   = "projectx_webapp"
	scopeValue = "AdobeID,firefly_api,openid"
	// clioClientID / clioScopeValue mint the firefly.adobe.com token that is
	// entitled to the Firefly-native seedance models (creative_production scope).
	// Unlike the Express token this exchange requires user_id and no guest_allowed.
	clioClientID   = "clio-playground-web"
	clioScopeValue = "AdobeID,firefly_api,openid,pps.read,pps.write,additional_info.projectedProductContext,additional_info.ownerOrg,uds_read,uds_write,ab.manage,read_organizations,additional_info.roles,account_cluster.read,creative_production,tk_platform,tk_platform_sync,profile"
)

var ErrAdobeCookieEmpty = errors.New("cookie is empty")

type CookieExchangeResult struct {
	AccessToken string
	ExpiresIn   int
	Raw         map[string]any
}

func ExchangeCookieToAccessToken(ctx context.Context, client *http.Client, cookie string) (*CookieExchangeResult, error) {
	_ = client
	tlsClient, err := NewClient(clientID, "").newTLSClient()
	if err != nil {
		return nil, err
	}
	return exchangeCookieWithTLSClient(ctx, tlsClient, cookie)
}

func normalizeCookie(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(v), "cookie:") {
		v = strings.TrimSpace(v[len("cookie:"):])
	}
	return v
}
