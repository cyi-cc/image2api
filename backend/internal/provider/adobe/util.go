package adobe

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// adobeUserIDPat matches Adobe IMS user IDs embedded in cookies (e.g.
// "4BDA81F069FC6DA40A495FAB@AdobeID").
var adobeUserIDPat = regexp.MustCompile(`[A-Fa-f0-9]{20,}@AdobeID`)

// PID pool: one PID per token, reused across requests for the same account.
var (
	arpPIDMu    sync.Mutex
	arpTokenPID = map[string]int{} // token → pid
	arpPIDToken = map[int]string{} // pid → token
)

func extractUserIDFromCookie(cookie string) string {
	decoded, err := url.QueryUnescape(cookie)
	if err != nil {
		decoded = cookie
	}
	return adobeUserIDPat.FindString(decoded)
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return strings.TrimSpace(strings.ReplaceAll(toJSONScalar(x), "\n", " "))
	}
}

func toJSONScalar(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func defaultString(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// arkHead and arkTail are the site-constant halves of the ark blob, as captured
// from firefly.adobe.com. The leading "<17hex>.<10digits>" pair and the rid
// field sit between them and vary per session, so buildARKBlob randomizes those
// instead of shipping one frozen blob for every account.
const (
	arkHead = "|r=ap-southeast-1|meta=3|metabgclr=transparent|metaiconclr=%23757575|guitextcolor=%23000000|pk=BBCC314C-4937-4CCD-B0A3-FDF0F0F7603C|at=40|sup=1"
	arkTail = "|ag=101|cdn_url=https%3A%2F%2Farks-client.adobe.com%2Fcdn%2Ffc|surl=https%3A%2F%2Farks-client.adobe.com|smurl=https%3A%2F%2Farks-client.adobe.com%2Fcdn%2Ffc%2Fassets%2Fstyle-manager"
)

func buildARKBlob() string {
	return randomHex(9)[:17] + "." + randomDigits(10) +
		arkHead + "|rid=" + strconv.Itoa(randomInt(1, 99)) + arkTail
}

// buildARPSessionID returns the x-arp-session-id value: base64 of
// {"sid","ark","ftr"}, shaped to match live firefly.adobe.com traffic:
//
//	ark: <17hex>.<10digits>|…|rid=<n>|…
//	ftr: <32hex>_<unix_ms>_<pid>_UDF43-m4_31ck__tt
//
// The pid is allocated per token (allocPID) so it stays stable across an
// account's requests, the way a real browser session would.
//
// The Arkose segment is left empty on purpose. In a real browser that slot
// carries a "<b64>=-<4digits>-v2" payload emitted by Adobe's Arkose bot-manager
// script; a synthesized one has the right shape but fails Adobe's server-side
// validation, and 3p-images/generate-async rejects it with a misleading
// {"error_code":"timeout_error","message":"system under load"}. Sending the
// slot empty reads as "no Arkose payload yet" and is accepted.
func buildARPSessionID(token string) string {
	ftr := strings.Join([]string{
		randomHex(16), // 32 hex chars, matching the capture
		strconv.FormatInt(time.Now().UnixMilli(), 10),
		strconv.Itoa(allocPID(token)),
		"UDF43-m4",
		"31ck",
		"",
		"tt",
	}, "_")

	b, _ := json.Marshal(map[string]any{
		"sid": uuid.NewString(),
		"ark": buildARKBlob(),
		"ftr": ftr,
	})
	return base64.StdEncoding.EncodeToString(b)
}

// randomDigits returns n decimal digits with a non-zero leading digit, so the
// rendered value keeps a constant width.
func randomDigits(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteByte(byte('1' + randomInt(0, 8)))
	for i := 1; i < n; i++ {
		b.WriteByte(byte('0' + randomInt(0, 9)))
	}
	return b.String()
}

func randomBase64(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func allocPID(token string) int {
	arpPIDMu.Lock()
	defer arpPIDMu.Unlock()
	if pid, ok := arpTokenPID[token]; ok {
		return pid
	}
	for {
		pid := randomInt(1000, 99999)
		if _, used := arpPIDToken[pid]; !used {
			arpPIDToken[pid] = token
			arpTokenPID[token] = pid
			return pid
		}
	}
}

// ReleasePID releases the PID bound to token so it can be reused by another
// account.
func ReleasePID(token string) {
	arpPIDMu.Lock()
	defer arpPIDMu.Unlock()
	if pid, ok := arpTokenPID[token]; ok {
		delete(arpPIDToken, pid)
		delete(arpTokenPID, token)
	}
}

func randomHex(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		now := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(now >> ((i % 8) * 8))
		}
	}
	return hex.EncodeToString(buf)
}

func randomInt(min, max int) int {
	if max <= min {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}

func intOrNil(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return nil
		}
		return int(n)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return nil
		}
		return n
	default:
		return nil
	}
}

func emptyStringNil(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}
