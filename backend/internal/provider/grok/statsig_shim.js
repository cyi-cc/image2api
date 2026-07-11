// statsig_shim.js — browser-environment shim that lets grok.com's own obfuscated
// x-statsig-id signer run inside goja. grok's code does all the (per-build,
// rotating) byte-indexing/curve-selection; we only provide the STABLE browser
// primitives it reads from: the seed <meta>, the .r-aufz1o SVG curve DOM, and a
// standard Web-Animations getComputedStyle sampler. Inputs arrive via globals set
// by Go before each sign: __SEED (base64 str), __CURVES (JSON [[{color,deg,bezier}]]),
// __PATH, __METHOD. Go also injects __goSha256(Uint8Array)->ArrayBuffer.
(function () {
  'use strict';
  var g = globalThis;

  // ---- base64 (goja has no atob/btoa) ----
  var B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  g.atob = function (s) {
    s = String(s).replace(/=+$/, '');
    var out = '', bits = 0, val = 0;
    for (var i = 0; i < s.length; i++) {
      var c = B64.indexOf(s.charAt(i));
      if (c < 0) continue;
      val = (val << 6) | c; bits += 6;
      if (bits >= 8) { bits -= 8; out += String.fromCharCode((val >> bits) & 0xff); }
    }
    return out;
  };
  g.btoa = function (s) {
    s = String(s); var out = '';
    for (var i = 0; i < s.length; i += 3) {
      var b0 = s.charCodeAt(i), b1 = s.charCodeAt(i + 1), b2 = s.charCodeAt(i + 2);
      var h0 = b0 >> 2, h1 = ((b0 & 3) << 4) | (b1 >> 4);
      var h2 = ((b1 & 15) << 2) | (b2 >> 6), h3 = b2 & 63;
      out += B64[h0] + B64[h1];
      out += isNaN(b1) ? '=' : B64[h2];
      out += isNaN(b2) ? '=' : B64[h3];
    }
    return out;
  };

  // ---- TextEncoder (goja has no TextEncoder) ----
  if (typeof g.TextEncoder === 'undefined') {
    g.TextEncoder = function () {};
    g.TextEncoder.prototype.encode = function (str) {
      str = String(str);
      var bytes = [];
      for (var i = 0; i < str.length; i++) {
        var c = str.charCodeAt(i);
        if (c < 0x80) bytes.push(c);
        else if (c < 0x800) { bytes.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f)); }
        else if (c >= 0xd800 && c <= 0xdbff) { // surrogate pair
          var c2 = str.charCodeAt(++i);
          var cp = 0x10000 + ((c & 0x3ff) << 10) + (c2 & 0x3ff);
          bytes.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
        } else { bytes.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f)); }
      }
      return Uint8Array.from(bytes);
    };
  }

  // ---- crypto.subtle.digest, backed by Go SHA-256 ----
  g.crypto = g.crypto || {};
  g.crypto.subtle = g.crypto.subtle || {};
  g.crypto.subtle.digest = function (algo, data) {
    // grok only ever asks for sha-256; g.__goSha256 returns an ArrayBuffer
    var bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    return Promise.resolve(g.__goSha256(bytes));
  };

  // ---- Web Animations getComputedStyle sampler (the only real math we own) ----
  var K = 4096;
  function cubicBezier(x1, y1, x2, y2, p) {
    if (p <= 0) return 0; if (p >= 1) return 1;
    function bez(t, a, b) { var mt = 1 - t; return 3 * a * mt * mt * t + 3 * b * mt * t * t + t * t * t; }
    var lo = 0, hi = 1;
    for (var i = 0; i < 100; i++) { var m = (lo + hi) / 2; if (bez(m, x1, x2) < p) lo = m; else hi = m; }
    return bez((lo + hi) / 2, y1, y2);
  }
  function hexToRgb(h) { h = h.replace('#', ''); return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]; }
  function sample(anim) {
    var kf = anim.keyframes, dur = anim.duration || K;
    var frac = anim.currentTime / dur; if (frac < 0) frac = 0; if (frac > 1) frac = 1;
    var bm = /cubic-bezier\(([^)]+)\)/.exec(kf.easing || '');
    var eased = frac;
    if (bm) { var p = bm[1].split(',').map(Number); eased = cubicBezier(p[0], p[1], p[2], p[3], frac); }
    var c0 = hexToRgb(kf.color[0]), c1 = hexToRgb(kf.color[1]);
    var col = [0, 1, 2].map(function (i) { return Math.round(c0[i] + (c1[i] - c0[i]) * eased); });
    var d0 = parseFloat(/rotate\(([-\d.]+)deg\)/.exec(kf.transform[0])[1]);
    var d1 = parseFloat(/rotate\(([-\d.]+)deg\)/.exec(kf.transform[1])[1]);
    var ang = (d0 + (d1 - d0) * eased) * Math.PI / 180;
    var cos = Math.cos(ang), sin = Math.sin(ang);
    return { color: 'rgb(' + col[0] + ', ' + col[1] + ', ' + col[2] + ')',
             transform: 'matrix(' + cos + ', ' + sin + ', ' + (-sin) + ', ' + cos + ', 0, 0)' };
  }

  // ---- minimal DOM ----
  function makeEl(props) {
    var e = {
      nodeName: 'DIV', style: {}, childNodes: [], attrs: {}, _anim: null, _parent: null,
      setAttribute: function (k, v) { e.attrs[k] = v; },
      getAttribute: function (k) { return (k in e.attrs) ? e.attrs[k] : (props && props.attrs && k in props.attrs ? props.attrs[k] : null); },
      appendChild: function (c) { e.childNodes.push(c); return c; },
      append: function (c) { e.childNodes.push(c); return c; },
      removeChild: function (c) { return c; },
      remove: function () {},
      animate: function (keyframes, opts) {
        var anim = {
          keyframes: keyframes,
          duration: (opts && opts.duration) || (typeof opts === 'number' ? opts : K),
          currentTime: 0, pause: function () {}, play: function () {}, cancel: function () {},
          effect: { getKeyframes: function () { return Array.isArray(keyframes) ? keyframes : [keyframes]; } },
        };
        e._anim = anim; return anim;
      },
      getAnimations: function () { return e._anim ? [e._anim] : []; },
    };
    if (props) for (var k in props) if (k !== 'attrs') e[k] = props[k];
    Object.defineProperty(e, 'parentElement', { get: function () { return e._parent; } });
    Object.defineProperty(e, 'innerHTML', { set: function (v) { e._html = v; }, get: function () { return e._html; } });
    Object.defineProperty(e, 'textContent', { set: function (v) { e._text = v; }, get: function () { return e._text; } });
    return e;
  }

  // .r-aufz1o group: g.childNodes[0].childNodes[1].getAttribute('d') = svg path whose
  // numbers (after substring(9), split('C')) decode back to each curve [c0..c5,deg,b0..b3].
  function groupEl(flatCurves) {
    var d = '_________' + flatCurves.map(function (c) { return c.join(' '); }).join('C');
    var path = makeEl({ attrs: { d: d } });
    var inner = makeEl(); inner.childNodes = [makeEl(), path];
    var outer = makeEl(); outer.childNodes = [inner]; outer._parent = makeEl();
    return outer;
  }

  var docBody = makeEl();
  g.document = {
    currentScript: null, body: docBody, head: makeEl(),
    createElement: function (tag) { return makeEl({ nodeName: String(tag || 'div').toUpperCase() }); },
    querySelectorAll: function (sel) {
      sel = String(sel);
      if (/aufz1o/.test(sel)) {
        var curves = JSON.parse(g.__CURVES);
        return curves.map(function (grp) {
          return groupEl(grp.map(function (cv) { return cv.color.concat([cv.deg], cv.bezier); }));
        });
      }
      if (/verification|name/i.test(sel)) {
        var seed = g.__SEED;
        return [{ nodeName: 'META', getAttribute: function (a) { return a === 'content' ? seed : null; },
                  get content() { return seed; } }];
      }
      return [];
    },
    querySelector: function (sel) { var r = this.querySelectorAll(sel); return r[0] || null; },
  };
  g.window = g;
  g.self = g;
  g.getComputedStyle = function (el) { return el && el._anim ? sample(el._anim) : { color: 'rgb(0, 0, 0)', transform: 'none' }; };
  g.navigator = g.navigator || { userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36' };
  g.location = g.location || { href: 'https://grok.com/', origin: 'https://grok.com', pathname: '/' };

  // ---- Turbopack capture + bootstrap ----
  var TP = []; TP.push = function (entry) { TP._entry = entry; return 0; };
  g.TURBOPACK = TP;

  // Called by Go AFTER the signer chunk is eval'd: run the module factory, grab default.
  g.__grokBootstrap = function () {
    var entry = TP._entry;
    if (!entry) throw new Error('turbopack entry not registered');
    var factory = entry[2];
    var exports = {};
    var ctx = {
      s: function () {
        var flat = Array.prototype.slice.call(arguments).flat(Infinity);
        var name = null;
        for (var i = 0; i < flat.length; i++) {
          var x = flat[i];
          if (typeof x === 'string') name = x;
          else if (typeof x === 'function' && name != null) {
            (function (nm, getter) { Object.defineProperty(exports, nm, { get: getter, configurable: true, enumerable: true }); })(name, x);
            name = null;
          }
        }
      },
    };
    factory(ctx);
    // exports.default is a GETTER that invokes the module factory on every read,
    // returning a FRESH async signer (path,method)=>Promise<id> with a fresh internal
    // curve cache. Keep the exports object; read .default fresh per sign so different
    // sessions' curves never leak through the closure cache.
    g.__grokExports = exports;
    if (typeof exports.default !== 'function') throw new Error('no default export');
  };

  // Called by Go per sign. Fresh signer each time via the re-invoking getter.
  g.__grokSign = function () {
    var signer = g.__grokExports.default; // fresh async signer
    return signer(g.__PATH, g.__METHOD);  // returns Promise<string>
  };

  // Synchronous bridge: Go calls this via RunString (which drains goja's job queue),
  // then reads __grokResult / __grokErr. Works because crypto.subtle.digest resolves
  // synchronously (Promise.resolve over a Go SHA-256), so the whole await chain settles
  // within the microtask drain.
  g.__grokResult = null;
  g.__grokErr = null;
  g.__grokSignInto = function () {
    g.__grokResult = null; g.__grokErr = null;
    try {
      g.__grokSign().then(
        function (r) { g.__grokResult = r; },
        function (e) { g.__grokErr = (e && e.stack) ? String(e.stack) : String(e); }
      );
    } catch (e) { g.__grokErr = (e && e.stack) ? String(e.stack) : String(e); }
  };
})();
