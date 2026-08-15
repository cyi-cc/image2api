package adobe

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"testing"
)

// captured from live firefly.adobe.com traffic:
//
//	ark: 91818c89a54748463.1048135404|r=ap-southeast-1|…|rid=84|ag=101|…
//	ftr: dbd9d77a491b4437bc5c4d649a04a794_1785846934401_6890_UDF43-m4_31ck_YRQXWT0P1AE=-7389-v2_tt
//
// The Arkose slot is deliberately emitted empty rather than synthesized — see
// buildARPSessionID — so the expected ftr ends in "_31ck__tt".
var (
	arkPat = regexp.MustCompile(`^[0-9a-f]{17}\.[1-9][0-9]{9}\|r=ap-southeast-1\|.*\|rid=[0-9]{1,2}\|ag=101\|`)
	ftrPat = regexp.MustCompile(`^[0-9a-f]{32}_[0-9]{13}_[0-9]{4,5}_UDF43-m4_31ck__tt$`)
)

func TestARPSessionIDShape(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(buildARPSessionID("tok-a"))
	if err != nil {
		t.Fatalf("not base64: %v", err)
	}
	var got struct{ Sid, Ark, Ftr string }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if !arkPat.MatchString(got.Ark) {
		t.Errorf("ark shape mismatch:\n%s", got.Ark)
	}
	if !ftrPat.MatchString(got.Ftr) {
		t.Errorf("ftr shape mismatch:\n%s", got.Ftr)
	}
	if len(got.Sid) != 36 {
		t.Errorf("sid not a uuid: %q", got.Sid)
	}
}

// ark must differ per call — a frozen blob is a cross-account correlation key.
func TestARKVariesAcrossCalls(t *testing.T) {
	if buildARKBlob() == buildARKBlob() {
		t.Error("ark is constant across calls")
	}
}

// pid is stable per token but distinct across tokens.
func TestPIDStablePerToken(t *testing.T) {
	defer ReleasePID("tok-x")
	defer ReleasePID("tok-y")
	if allocPID("tok-x") != allocPID("tok-x") {
		t.Error("pid changed for the same token")
	}
	if allocPID("tok-x") == allocPID("tok-y") {
		t.Error("two tokens share a pid")
	}
}
