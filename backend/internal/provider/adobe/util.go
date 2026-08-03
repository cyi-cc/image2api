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
	"time"

	"github.com/google/uuid"
)

// adobeUserIDPat matches Adobe IMS user IDs embedded in cookies (e.g.
// "4BDA81F069FC6DA40A495FAB@AdobeID").
var adobeUserIDPat = regexp.MustCompile(`[A-Fa-f0-9]{20,}@AdobeID`)

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

func buildARPSessionID() string {
	// Matches adobe2api's format exactly:
	// base64({"sid":"<uuid>","ftr":"<hex16>_<ts_ms>_<pid>_dUAL43-mnts-ants-d4_31ck__tt"})
	// Two fields only (no "ark") — mirrors what a real browser session sends.
	ftr := randomHex(16) + "_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "_" + strconv.Itoa(randomInt(1000, 99999)) + "_dUAL43-mnts-ants-d4_31ck__tt"
	raw := map[string]any{
		"sid": uuid.NewString(),
		"ftr": ftr,
	}
	b, _ := json.Marshal(raw)
	return base64.StdEncoding.EncodeToString(b)
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
