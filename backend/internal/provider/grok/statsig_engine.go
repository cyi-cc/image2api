package grok

// This file makes x-statsig-id durable across grok web reships by executing grok's
// OWN obfuscated signer (a Turbopack chunk) inside an embedded JS engine (goja),
// under a synthesized DOM + Web-Animations getComputedStyle shim (statsig_shim.js).
// grok's code does all the per-build byte-indexing / curve-selection; we only supply
// the stable browser primitives. This replaces the brittle hand-ported byte-offset
// algorithm in computeStatsigTail (kept as a last-resort fallback). See the package
// doc and the grok-statsig-signer memory for the reverse-engineering details.

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/dop251/goja"
)

//go:embed statsig_shim.js
var statsigShimJS string

const sigPoolSize = 4

// errEngineNotReady means the durable signer engine has not been built yet (no
// homepage fetched, or chunk location failed). Callers fall back to the static path.
var errEngineNotReady = errors.New("statsig engine not ready")

var (
	// signer-chunk location patterns (Turbopack). The caller chunk contains the
	// literal "x-statsig-id" and a lazy import `.A(<moduleId>).then(e=>t(e.default()))`.
	statsigCallerRe = regexp.MustCompile(`\.A\((\d+)\)\.then\(`)
	chunkPathRe     = regexp.MustCompile(`/_next/static/chunks/[a-zA-Z0-9_.\-/]+\.js`)
	// goja's parser tries to fetch //# sourceMappingURL=... from disk and errors.
	sourceMapRe = regexp.MustCompile(`(?m)//[#@]\s*sourceMappingURL=\S*`)

	sigMgrMu    sync.Mutex
	sigBuildKey string           // hash of the homepage chunk list; changes on reship
	sigChunkSrc string           // current signer chunk source
	sigPool     chan *sigEngine  // pool of ready engines for sigChunkSrc
)

// sigEngine wraps one goja runtime with grok's signer chunk loaded. A goja runtime
// is not safe for concurrent use; the pool hands each engine to one goroutine at a
// time so no per-engine locking is needed.
type sigEngine struct {
	rt   *goja.Runtime
	fire goja.Callable // __grokSignInto
}

func newSigEngine(chunkSrc string) (*sigEngine, error) {
	rt := goja.New()
	// SHA-256 bridge for crypto.subtle.digest.
	if err := rt.Set("__goSha256", func(call goja.FunctionCall) goja.Value {
		data := jsBytes(rt, call.Argument(0))
		sum := sha256.Sum256(data)
		return rt.ToValue(rt.NewArrayBuffer(sum[:]))
	}); err != nil {
		return nil, err
	}
	if _, err := rt.RunString(statsigShimJS); err != nil {
		return nil, fmt.Errorf("shim: %w", err)
	}
	if _, err := rt.RunString(sourceMapRe.ReplaceAllString(chunkSrc, "")); err != nil {
		return nil, fmt.Errorf("chunk eval: %w", err)
	}
	if _, err := rt.RunString("__grokBootstrap()"); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	fire, ok := goja.AssertFunction(rt.Get("__grokSignInto"))
	if !ok {
		return nil, errors.New("statsig js: __grokSignInto missing")
	}
	return &sigEngine{rt: rt, fire: fire}, nil
}

// statsigID runs grok's signer for one request. seedB64 is the raw <meta> content;
// curvesJSON is [[{color,deg,bezier}...]...].
func (e *sigEngine) statsigID(seedB64, curvesJSON, path, method string) (string, error) {
	_ = e.rt.Set("__SEED", seedB64)
	_ = e.rt.Set("__CURVES", curvesJSON)
	_ = e.rt.Set("__PATH", path)
	_ = e.rt.Set("__METHOD", method)
	// RunString drains goja's microtask queue, settling the async signer's promise.
	if _, err := e.fire(goja.Undefined()); err != nil {
		return "", err
	}
	if errv := e.rt.Get("__grokErr"); errv != nil && !goja.IsNull(errv) && !goja.IsUndefined(errv) {
		return "", fmt.Errorf("statsig js: %s", errv.String())
	}
	res := e.rt.Get("__grokResult")
	if res == nil || goja.IsNull(res) || goja.IsUndefined(res) {
		return "", errors.New("statsig js: promise did not settle")
	}
	id := res.String()
	if id == "" {
		return "", errors.New("statsig js: empty id")
	}
	return id, nil
}

// jsBytes extracts the byte contents of a JS Uint8Array / ArrayBuffer value.
func jsBytes(rt *goja.Runtime, v goja.Value) []byte {
	if ab, ok := v.Export().(goja.ArrayBuffer); ok {
		return ab.Bytes()
	}
	obj := v.ToObject(rt)
	if buf := obj.Get("buffer"); buf != nil {
		if ab, ok := buf.Export().(goja.ArrayBuffer); ok {
			return ab.Bytes()
		}
	}
	n := int(obj.Get("length").ToInteger())
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = byte(obj.Get(strconv.Itoa(i)).ToInteger())
	}
	return out
}

// signWithEngine borrows an engine from the pool (building one on demand), signs,
// and returns it. Returns an error if the engine subsystem is not ready.
func signWithEngine(seedB64, curvesJSON, path, method string) (string, error) {
	sigMgrMu.Lock()
	src, pool := sigChunkSrc, sigPool
	sigMgrMu.Unlock()
	if src == "" || pool == nil {
		return "", errEngineNotReady
	}
	var eng *sigEngine
	select {
	case eng = <-pool:
	default:
		var err error
		if eng, err = newSigEngine(src); err != nil {
			return "", err
		}
	}
	id, err := eng.statsigID(seedB64, curvesJSON, path, method)
	select {
	case pool <- eng:
	default:
	}
	return id, err
}

// ensureEngine refreshes the global engine pool when the homepage's chunk set
// changes (i.e. grok reshipped). It locates the signer chunk build-agnostically and
// rebuilds the pool. Cheap no-op when the build is unchanged.
func ensureEngine(ctx context.Context, client tlsclient.HttpClient, homeHTML string) {
	paths := chunkPathRe.FindAllString(homeHTML, -1)
	if len(paths) == 0 {
		return
	}
	key := hashStrings(paths)

	sigMgrMu.Lock()
	unchanged := key == sigBuildKey && sigPool != nil
	sigMgrMu.Unlock()
	if unchanged {
		return
	}

	src, err := locateSignerChunk(ctx, client, dedupe(paths))
	if err != nil {
		log.Printf("grok statsig: locate signer chunk failed (will use static fallback): %v", err)
		return
	}
	// smoke-test: a build must produce a loadable engine before we commit to it.
	eng, err := newSigEngine(src)
	if err != nil {
		log.Printf("grok statsig: signer chunk did not load in goja (static fallback): %v", err)
		return
	}
	pool := make(chan *sigEngine, sigPoolSize)
	pool <- eng // reuse the smoke-test engine instead of discarding it
	sigMgrMu.Lock()
	sigBuildKey = key
	sigChunkSrc = src
	sigPool = pool
	sigMgrMu.Unlock()
	log.Printf("grok statsig: self-heal engine ready (build %s..)", key[:8])
}

// locateSignerChunk finds grok's signer chunk from the homepage chunk list:
// the caller chunk holds "x-statsig-id" + `.A(<id>)`; a loader chunk registers that
// <id> with `Promise.all(["static/chunks/XXX.js"]...)` — XXX is the signer.
func locateSignerChunk(ctx context.Context, client tlsclient.HttpClient, paths []string) (string, error) {
	var callerID string
	loaderRe := (*regexp.Regexp)(nil)
	var signerPath string

	// pass 1: find the caller chunk + its lazy module id.
	for _, p := range paths {
		body, err := fetchChunk(ctx, client, p)
		if err != nil || !strings.Contains(body, "x-statsig-id") {
			continue
		}
		if m := statsigCallerRe.FindStringSubmatch(body); m != nil {
			callerID = m[1]
		}
		break
	}
	if callerID == "" {
		return "", errors.New("statsig caller module id not found")
	}
	// loader registers: ,<callerID>,<param>=>{ ... Promise.all(["static/chunks/XXX.js"] ...
	loaderRe = regexp.MustCompile(`,` + callerID + `,\w+=>\{[^}]*?Promise\.all\(\["(static/chunks/[^"]+\.js)"`)

	// pass 2: find the loader chunk that maps callerID -> signer chunk path.
	for _, p := range paths {
		body, err := fetchChunk(ctx, client, p)
		if err != nil {
			continue
		}
		if m := loaderRe.FindStringSubmatch(body); m != nil {
			signerPath = m[1]
			break
		}
	}
	if signerPath == "" {
		return "", fmt.Errorf("signer chunk path for module %s not found", callerID)
	}
	src, err := fetchChunk(ctx, client, "/_next/"+signerPath)
	if err != nil {
		return "", fmt.Errorf("fetch signer chunk: %w", err)
	}
	return src, nil
}

func fetchChunk(ctx context.Context, client tlsclient.HttpClient, path string) (string, error) {
	if !strings.HasPrefix(path, "http") {
		path = apiBase + path
	}
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":            {"*/*"},
		"user-agent":        {userAgent},
		http.HeaderOrderKey: {"accept", "user-agent"},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("chunk http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func hashStrings(ss []string) string {
	uniq := dedupe(ss)
	h := sha512.New()
	for _, s := range uniq {
		_, _ = io.WriteString(h, s)
		_, _ = io.WriteString(h, "\n")
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
