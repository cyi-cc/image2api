package grok

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ground-truth (seed, curves, F) triples. The first row is server-verified
// (captured from a real grok browser token that returned 200); the remainder
// exercise every curve group / currentTime branch and are checked against the
// reference implementation the algorithm was reverse-engineered against.
var statsigTriples = []struct {
	seedHex string
	wantM   string
}{
	{seedHex: "732c9800d181e47c9b2a2705713306f42a51a10d69c6a6c32e53b26d1599b0b75399035a9953fedf05903cb9eb70a989", wantM: "ff75470a3d70a3d70a3d80c51eb851eb8520c51eb851eb8520a3d70a3d70a3d800"},
	{seedHex: "732c9800d1802530bb1d6d057133064d2aa5a1ca69c6a6c32e53b26d1599b0b75399035a9953fe1805903cb9eb70a989", wantM: "27fa10fd70a3d70a3d7028f5c28f5c28f6028f5c28f5c28f60fd70a3d70a3d700"},
	{seedHex: "732c9800d180237b2ed91e057133062c2a13a1de69c6a6c32e53b26d1599b0b75399035a9953fed605903cb9eb70a989", wantM: "079ff0d1eb851eb851e80947ae147ae14780947ae147ae14780d1eb851eb851e800"},
	{seedHex: "732c9800d180197117449405713306722a3fa11f69c6a6c32e53b26d1599b0b75399035a9953fecb05903cb9eb70a989", wantM: "195fe0fae147ae147ae035c28f5c28f5c2035c28f5c28f5c20fae147ae147ae00"},
	{seedHex: "732c9800d1815c3460be3105713306492ad6a13c69c6a6c32e53b26d1599b0b75399035a9953fe9d05903cb9eb70a989", wantM: "ff6ab5035c28f5c28f5c20fae147ae147ae0fae147ae147ae035c28f5c28f5c200"},
	{seedHex: "732c9800d181daa0eee8b9057133061e2a20a16969c6a6c32e53b26d1599b0b75399035a9953fefe05903cb9eb70a989", wantM: "3da0d70e3d70a3d70a3d8075c28f5c28f5c4075c28f5c28f5c40e3d70a3d70a3d800"},
	{seedHex: "732c9800d1812999fdafe5057133067f2a99a15c69c6a6c32e53b26d1599b0b75399035a9953fe7c05903cb9eb70a989", wantM: "1cac4c0451eb851eb8520f5c28f5c28f5c0f5c28f5c28f5c0451eb851eb85200"},
	{seedHex: "732c9800d18254af4dfad705713306252a93a13c69c6a6c32e53b26d1599b0b75399035a9953fed605903cb9eb70a989", wantM: "b40480d70a3d70a3d70808a3d70a3d70a408a3d70a3d70a40d70a3d70a3d70800"},
	{seedHex: "732c9800d182b3fee9232f05713306272a14a1a069c6a6c32e53b26d1599b0b75399035a9953feae05903cb9eb70a989", wantM: "13757a100100"},
	{seedHex: "732c9800d1829ee491c5b105713306f22a8aa12169c6a6c32e53b26d1599b0b75399035a9953fe1f05903cb9eb70a989", wantM: "eb4fb9100a3d70a3d70a3d800a3d70a3d70a3d8100"},
	{seedHex: "732c9800d1833bfc1e6f9305713306ec2a0ba1b569c6a6c32e53b26d1599b0b75399035a9953fe5605903cb9eb70a989", wantM: "8a56650fae147ae147ae02b851eb851eb8602b851eb851eb860fae147ae147ae00"},
	{seedHex: "732c9800d183fe2955e5cd057133067e2a42a1cb69c6a6c32e53b26d1599b0b75399035a9953fec805903cb9eb70a989", wantM: "b242950deb851eb851eb807d70a3d70a3d707d70a3d70a3d70deb851eb851eb800"},
	{seedHex: "732c9800d183d4b7c2764d05713306462a8ea1dc69c6a6c32e53b26d1599b0b75399035a9953fe8e05903cb9eb70a989", wantM: "1e70d8070a3d70a3d70a40e66666666666680e6666666666668070a3d70a3d70a400"}}

const statsigTestCurves = `[[{"color":[48,44,6,37,198,15],"deg":192,"bezier":[118,76,158,16]},{"color":[224,216,196,111,43,97],"deg":119,"bezier":[67,167,95,219]},{"color":[90,235,250,5,223,64],"deg":104,"bezier":[100,57,106,204]},{"color":[6,109,253,44,29,224],"deg":151,"bezier":[204,60,142,122]},{"color":[81,0,121,208,228,133],"deg":181,"bezier":[182,89,105,123]},{"color":[31,3,160,181,226,184],"deg":98,"bezier":[207,150,215,136]},{"color":[231,243,81,28,109,131],"deg":175,"bezier":[17,103,7,81]},{"color":[222,250,130,169,55,247],"deg":141,"bezier":[21,238,12,84]},{"color":[74,62,116,145,209,185],"deg":109,"bezier":[0,74,58,233]},{"color":[204,168,203,138,107,125],"deg":214,"bezier":[41,13,239,45]},{"color":[246,116,162,162,84,234],"deg":127,"bezier":[160,202,52,76]},{"color":[138,202,210,36,61,195],"deg":234,"bezier":[31,50,177,229]},{"color":[47,46,196,176,79,255],"deg":221,"bezier":[39,14,9,76]},{"color":[245,164,227,71,201,110],"deg":190,"bezier":[193,208,125,9]},{"color":[199,253,44,33,130,240],"deg":191,"bezier":[168,247,61,37]},{"color":[19,91,5,180,202,243],"deg":216,"bezier":[96,152,42,123]}],[{"color":[42,154,230,170,197,128],"deg":108,"bezier":[160,52,34,184]},{"color":[224,132,83,0,231,162],"deg":238,"bezier":[227,37,122,102]},{"color":[24,82,237,199,100,74],"deg":130,"bezier":[186,106,188,209]},{"color":[47,50,169,65,124,44],"deg":228,"bezier":[198,22,146,225]},{"color":[111,131,95,195,131,73],"deg":207,"bezier":[134,146,202,172]},{"color":[192,10,166,28,236,3],"deg":219,"bezier":[85,77,157,235]},{"color":[236,139,199,72,233,250],"deg":197,"bezier":[180,9,79,147]},{"color":[146,195,78,16,231,211],"deg":93,"bezier":[123,18,87,27]},{"color":[168,211,49,42,124,18],"deg":172,"bezier":[232,171,120,118]},{"color":[43,146,96,11,203,53],"deg":146,"bezier":[11,4,83,198]},{"color":[183,97,101,38,115,41],"deg":159,"bezier":[69,223,197,236]},{"color":[78,73,207,132,109,134],"deg":49,"bezier":[59,18,133,168]},{"color":[239,153,225,207,127,157],"deg":194,"bezier":[13,206,154,182]},{"color":[19,146,127,191,68,199],"deg":165,"bezier":[148,212,7,16]},{"color":[93,225,249,144,88,255],"deg":215,"bezier":[62,30,77,69]},{"color":[203,79,164,145,3,20],"deg":81,"bezier":[228,28,93,130]}],[{"color":[171,53,4,125,232,43],"deg":122,"bezier":[136,9,128,97]},{"color":[143,97,205,57,63,69],"deg":192,"bezier":[232,20,219,189]},{"color":[28,173,18,107,158,44],"deg":255,"bezier":[93,187,184,198]},{"color":[170,17,62,142,200,32],"deg":29,"bezier":[14,31,12,97]},{"color":[19,117,122,173,239,66],"deg":74,"bezier":[117,176,139,212]},{"color":[213,151,230,112,224,255],"deg":25,"bezier":[1,223,72,233]},{"color":[153,131,51,105,69,47],"deg":108,"bezier":[123,177,126,140]},{"color":[120,114,44,151,88,83],"deg":165,"bezier":[16,104,134,75]},{"color":[245,145,194,75,120,26],"deg":142,"bezier":[79,235,38,43]},{"color":[147,63,50,255,239,106],"deg":190,"bezier":[122,143,160,150]},{"color":[233,78,184,130,25,123],"deg":54,"bezier":[87,18,184,226]},{"color":[162,180,233,70,57,249],"deg":87,"bezier":[123,238,61,124]},{"color":[146,95,56,171,38,240],"deg":239,"bezier":[241,134,228,44]},{"color":[111,11,149,62,208,177],"deg":70,"bezier":[103,149,4,37]},{"color":[159,128,118,21,197,153],"deg":175,"bezier":[246,215,172,236]},{"color":[194,131,68,247,215,108],"deg":30,"bezier":[23,91,151,231]}],[{"color":[239,129,141,243,85,208],"deg":38,"bezier":[252,248,245,195]},{"color":[205,56,138,49,126,99],"deg":107,"bezier":[72,85,228,91]},{"color":[185,54,148,122,170,158],"deg":192,"bezier":[33,88,51,136]},{"color":[7,14,75,26,23,41],"deg":93,"bezier":[153,21,55,147]},{"color":[245,211,213,64,5,253],"deg":49,"bezier":[253,19,106,155]},{"color":[144,232,165,21,114,130],"deg":200,"bezier":[193,179,133,226]},{"color":[34,196,100,42,114,0],"deg":52,"bezier":[4,4,71,65]},{"color":[195,8,130,102,201,141],"deg":210,"bezier":[8,173,23,33]},{"color":[86,136,44,95,223,62],"deg":249,"bezier":[220,98,68,113]},{"color":[205,48,9,247,236,71],"deg":75,"bezier":[163,240,28,25]},{"color":[43,190,29,239,55,135],"deg":146,"bezier":[109,245,34,188]},{"color":[146,91,92,2,3,251],"deg":97,"bezier":[183,188,95,157]},{"color":[40,213,196,70,81,174],"deg":120,"bezier":[153,197,61,201]},{"color":[131,92,180,68,131,214],"deg":251,"bezier":[94,191,198,89]},{"color":[47,113,219,96,115,228],"deg":238,"bezier":[22,35,60,63]},{"color":[246,244,203,196,78,136],"deg":44,"bezier":[88,23,205,184]}]]`

// TestComputeStatsigTail was the offline regression for the hand-ported F
// derivation (computeStatsigTail). That algorithm is now only a last-resort
// fallback: the durable path runs grok's own signer in goja (statsig_engine.go),
// which self-heals across web reships. grok rotates the F algorithm on every
// reship, so these browser-captured (seed,F) triples go stale and CANNOT be
// regenerated offline. TestSelfHealStatsigE2E (live, via goja) is the real
// regression now. Kept skipped so the reference vectors aren't lost.
func TestComputeStatsigTail(t *testing.T) {
	t.Skip("static F derivation is a deprecated fallback; see TestSelfHealStatsigE2E (goja)")
	var curves [][]statsigCurve
	if err := json.Unmarshal([]byte(statsigTestCurves), &curves); err != nil {
		t.Fatalf("curves: %v", err)
	}
	for i, tc := range statsigTriples {
		seed, err := hex.DecodeString(tc.seedHex)
		if err != nil {
			t.Fatalf("[%d] seed hex: %v", i, err)
		}
		got, err := computeStatsigTail(seed, curves)
		if err != nil {
			t.Fatalf("[%d] computeStatsigTail: %v", i, err)
		}
		if got != tc.wantM {
			t.Errorf("[%d] group=%d\n got=%s\nwant=%s", i, int(seed[5])%len(curves), got, tc.wantM)
		}
	}
}

// TestSelfHealStatsigE2E exercises the full browser-free self-healing path:
// fetch the homepage, derive seed+F, cache the challenge, then hit the
// anti-bot-gated conversations/new endpoint. Requires a live GROK_TOK and no
// GROK_STATSIG_* env overrides.
func TestSelfHealStatsigE2E(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("GROK_TOK"))
	if token == "" {
		t.Skip("no GROK_TOK")
	}
	c := NewClient("")
	client, err := c.newTLSClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	c.ensureChallenge(ctx, client, token)
	statsigMu.Lock()
	ch, ok := statsigCache[token]
	statsigMu.Unlock()
	if !ok {
		t.Fatal("challenge not cached (homepage fetch/derive failed)")
	}
	t.Logf("dynamic header[:6]=%x suffix=%s", ch.header[:6], ch.suffix)

	body, err := c.postStream(ctx, client, token, "/rest/app-chat/conversations/new", map[string]any{
		"temporary": true,
		"modelName": "grok-3",
		"message":   "hi",
	})
	if err != nil {
		t.Fatalf("conversations/new: %v", err)
	}
	t.Logf("OK bytes=%d head=%.80s", len(body), strings.ReplaceAll(body, "\n", " "))
}

// TestStatsigEngineConcurrent fires many concurrent goja signs through the pool to
// catch data races / engine cross-talk (run with -race). Requires a live GROK_TOK
// so the engine is built and the challenge cached.
func TestStatsigEngineConcurrent(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("GROK_TOK"))
	if token == "" {
		t.Skip("no GROK_TOK")
	}
	c := NewClient("")
	client, err := c.newTLSClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c.ensureChallenge(ctx, client, token)
	statsigMu.Lock()
	ch, ok := statsigCache[token]
	statsigMu.Unlock()
	if !ok || ch.seedB64 == "" || ch.curvesJSON == "" {
		t.Fatal("engine inputs not cached")
	}

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := signWithEngine(ch.seedB64, ch.curvesJSON, "/rest/app-chat/conversations/new", "POST")
			if err != nil {
				errs <- err
				return
			}
			if len(id) < 40 {
				errs <- errTooShort
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent sign: %v", e)
	}
}

var errTooShort = errors.New("statsig id too short")

// TestGenerateVideoE2E generates a real grok video using only the dynamic
// self-healed statsig (no env overrides). Requires a live GROK_TOK.
func TestGenerateVideoE2E(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("GROK_TOK"))
	if token == "" {
		t.Skip("no GROK_TOK")
	}
	c := NewClient("")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	data, meta, err := c.GenerateVideo(ctx, token, "a cat playing piano", "16:9", "720p", 6, nil, true)
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	t.Logf("video bytes=%d meta=%v", len(data), meta)
	if len(data) < 1<<20 {
		t.Fatalf("video too small: %d bytes", len(data))
	}
	if !strings.Contains(string(data[:16]), "ftyp") {
		t.Fatalf("not an mp4: % x", data[:16])
	}
}
