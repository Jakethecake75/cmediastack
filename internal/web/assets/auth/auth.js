/* CMediaStack — anonymous bundle.
 *
 * This file is served to anyone who can reach the login page, so it must not
 * describe the authenticated API. It knows about the anonymous endpoints below
 * and nothing else: no route table, no permission list, no admin paths. The
 * application's client lives in the app bundle, behind authentication.
 *
 * Hand-written, no framework. CSP forbids inline script and inline style, so
 * everything here is addEventListener and class/hidden toggling — never
 * element.style.x or onclick attributes, both of which the policy blocks.
 */
'use strict';

(function () {
  var CSRF_COOKIE = 'cms_csrf';
  var CSRF_HEADER = 'X-CSRF-Token';

  function $(id) { return document.getElementById(id); }

  function cookie(name) {
    var parts = document.cookie ? document.cookie.split('; ') : [];
    for (var i = 0; i < parts.length; i++) {
      var eq = parts[i].indexOf('=');
      if (eq > 0 && parts[i].slice(0, eq) === name) {
        return decodeURIComponent(parts[i].slice(eq + 1));
      }
    }
    return '';
  }

  /* api performs one JSON call.
   *
   * The CSRF cookie is re-read on every call rather than captured once: the
   * server rotates it at each authentication step, and a stale value would be
   * rejected. It resolves to {status, body} and rejects only on transport
   * failure, so callers handle HTTP errors as data rather than exceptions. */
  function api(method, path, body) {
    var opts = {
      method: method,
      credentials: 'same-origin',
      redirect: 'error',
      headers: { 'Accept': 'application/json' }
    };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    if (method !== 'GET' && method !== 'HEAD') {
      opts.headers[CSRF_HEADER] = cookie(CSRF_COOKIE);
    }
    return fetch(path, opts).then(function (res) {
      return res.text().then(function (text) {
        var parsed = null;
        if (text) { try { parsed = JSON.parse(text); } catch (e) { parsed = null; } }
        return { status: res.status, body: parsed };
      });
    });
  }

  /* message renders the server's text verbatim through textContent.
   *
   * Never innerHTML: the strings here originate from the server but some of
   * them echo values the caller supplied, and one assignment is all it takes to
   * turn an error banner into a script host. */
  function show(el, text) {
    if (!el) { return; }
    el.textContent = text;
    el.hidden = false;
  }

  function hide(el) { if (el) { el.hidden = true; } }

  function failure(res, fallback) {
    if (res.body && typeof res.body.error === 'string' && res.body.error) {
      return res.body.error;
    }
    if (res.status === 429) { return 'too many attempts; wait a while and try again'; }
    if (res.status === 0) { return 'could not reach the server'; }
    return fallback;
  }

  /* busy disables a form's submit control for the duration of a request. It is
   * a usability guard, not a security one — the server refuses a duplicate
   * regardless. */
  function busy(form, on) {
    var btn = form.querySelector('button[type="submit"]');
    if (btn) { btn.disabled = on; }
  }

  function submit(form, handler) {
    if (!form) { return; }
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      busy(form, true);
      var done = function () { busy(form, false); };
      try {
        var p = handler();
        if (p && typeof p.then === 'function') { p.then(done, done); } else { done(); }
      } catch (e) {
        done();
        throw e;
      }
    });
  }

  function copy(text, button) {
    var restore = button.textContent;
    var settle = function (ok) {
      button.textContent = ok ? 'Copied' : 'Press Ctrl+C';
      setTimeout(function () { button.textContent = restore; }, 1600);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () { settle(true); },
        function () { settle(false); });
    } else {
      settle(false);
    }
  }

  // -------------------------------------------------------------------------

  function loginPage() {
    var creds = $('credentials'), mfa = $('second-factor'), err = $('error');

    submit(creds, function () {
      hide(err);
      return api('POST', '/api/v1/auth/login', {
        username: $('username').value,
        password: $('password').value
      }).then(function (res) {
        if (res.status !== 200) { show(err, failure(res, 'invalid credentials')); return; }
        if (res.body && res.body.next === 'enroll') {
          window.location.assign('/enroll');
          return;
        }
        creds.hidden = true;
        mfa.hidden = false;
        $('code').focus();
      }, function () { show(err, 'could not reach the server'); });
    });

    submit(mfa, function () {
      hide(err);
      return api('POST', '/api/v1/auth/login/mfa', { code: $('code').value.trim() })
        .then(function (res) {
          if (res.status !== 200) {
            show(err, failure(res, 'invalid code'));
            $('code').value = '';
            $('code').focus();
            return;
          }
          window.location.assign('/');
        }, function () { show(err, 'could not reach the server'); });
    });

    $('back').addEventListener('click', function () {
      // Reloading rather than un-hiding: the half-finished session cookie from
      // the password step should not linger behind a form that looks fresh.
      window.location.reload();
    });
  }

  /* sha256 is SHA-256 of an ASCII string, as eight 32-bit words. Written here
   * because this bundle loads nothing it did not write (ADR-0011), and
   * SubtleCrypto's one-promise-per-hash is far too slow for a proof-of-work. */
  var SHA_K = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2];
  var SHA_W = new Array(64);

  function sha256(text) {
    var n = text.length, words = ((n + 8) >> 6 << 4) + 16, m = new Array(words), i;
    for (i = 0; i < words; i++) { m[i] = 0; }
    for (i = 0; i < n; i++) { m[i >> 2] |= (text.charCodeAt(i) & 0xff) << (24 - (i % 4) * 8); }
    m[n >> 2] |= 0x80 << (24 - (n % 4) * 8);
    m[words - 1] = n * 8;
    var h = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    for (var off = 0; off < words; off += 16) {
      var w = SHA_W, a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7], t;
      for (t = 0; t < 64; t++) {
        if (t < 16) {
          w[t] = m[off + t] | 0;
        } else {
          var x = w[t - 15], y = w[t - 2];
          var s0 = ((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3);
          var s1 = ((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10);
          w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0;
        }
        var S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        var t1 = (hh + S1 + ((e & f) ^ (~e & g)) + SHA_K[t] + w[t]) | 0;
        var S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        var t2 = (S0 + ((a & b) ^ (a & c) ^ (b & c))) | 0;
        hh = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      h[0] = (h[0] + a) | 0; h[1] = (h[1] + b) | 0; h[2] = (h[2] + c) | 0; h[3] = (h[3] + d) | 0;
      h[4] = (h[4] + e) | 0; h[5] = (h[5] + f) | 0; h[6] = (h[6] + g) | 0; h[7] = (h[7] + hh) | 0;
    }
    return h;
  }

  /* leadingZeros counts the zero bits a digest begins with. */
  function leadingZeros(h) {
    for (var i = 0, z = 0; i < h.length; i++) {
      if (h[i] === 0) { z += 32; continue; }
      return z + Math.clz32(h[i]);
    }
    return z;
  }

  /* solve finds a counter for which SHA-256(challenge + ":" + counter) begins
   * with bits zero bits (ADR-0051), in slices so the page stays responsive. */
  function solve(challenge, bits) {
    return new Promise(function (resolve) {
      var counter = 0;
      (function slice() {
        for (var end = counter + 20000; counter < end; counter++) {
          if (leadingZeros(sha256(challenge + ':' + counter)) >= bits) {
            resolve(String(counter));
            return;
          }
        }
        setTimeout(slice, 0);
      })();
    });
  }

  /* proof fetches and solves the signup's challenge; none needed, it is empty. */
  function proof(status) {
    return api('GET', '/api/v1/auth/signup/challenge').then(function (res) {
      if (res.status !== 200 || !res.body || !res.body.challenge) { return { challenge: '', counter: '' }; }
      show(status, 'Working out the signup\u2019s small puzzle — a second or two\u2026');
      return solve(res.body.challenge, res.body.bits).then(function (counter) {
        hide(status);
        return { challenge: res.body.challenge, counter: counter };
      });
    });
  }

  function signupPage() {
    var form = $('signup'), err = $('error'), done = $('done');
    submit(form, function () {
      hide(err); hide(done);
      return proof(done).then(function (p) {
        return api('POST', '/api/v1/auth/signup', {
          username: $('username').value,
          email: $('email').value,
          password: $('password').value,
          note: $('note').value,
          invite_code: $('invite').value.trim(),
          pow_challenge: p.challenge,
          pow_counter: p.counter
        });
      }).then(function (res) {
        if (res.status !== 202 && res.status !== 201) {
          show(err, failure(res, 'could not submit that request'));
          return;
        }
        form.hidden = true;
        show(done, (res.body && res.body.message) || 'submitted');
      }, function () { show(err, 'could not reach the server'); });
    });
  }

  function resetPage() {
    var initiate = $('initiate'), complete = $('complete');
    var err = $('error'), done = $('done');

    // A token in the query string means the user followed a reset link. There
    // is no request to the server to decide which half of the page to show, so
    // an invalid token is indistinguishable from a valid one until it is used.
    var token = new URLSearchParams(window.location.search).get('token') || '';
    if (token) { complete.hidden = false; } else { initiate.hidden = false; }

    submit(initiate, function () {
      hide(err); hide(done);
      return api('POST', '/api/v1/auth/reset/initiate', { username: $('username').value })
        .then(function (res) {
          if (res.status !== 202) { show(err, failure(res, 'could not start a reset')); return; }
          initiate.hidden = true;
          show(done, (res.body && res.body.message) || 'submitted');
        }, function () { show(err, 'could not reach the server'); });
    });

    submit(complete, function () {
      hide(err); hide(done);
      return api('POST', '/api/v1/auth/reset/complete', {
        token: token,
        password: $('password').value
      }).then(function (res) {
        if (res.status !== 200) { show(err, failure(res, 'invalid or expired reset link')); return; }
        complete.hidden = true;
        show(done, (res.body && res.body.message) || 'password changed');
      }, function () { show(err, 'could not reach the server'); });
    });
  }

  function setupPage() {
    var form = $('setup'), err = $('error'), done = $('done');
    submit(form, function () {
      hide(err); hide(done);
      return api('POST', '/api/v1/setup', {
        username: $('username').value,
        email: $('email').value,
        password: $('password').value
      }).then(function (res) {
        if (res.status !== 201) {
          show(err, failure(res, 'could not create the administrator'));
          return;
        }
        form.hidden = true;
        show(done, 'Administrator created. Taking you to sign in…');
        setTimeout(function () { window.location.assign('/login'); }, 1200);
      }, function () { show(err, 'could not reach the server'); });
    });
  }

  function enrollPage() {
    var offer = $('offer'), codes = $('codes'), err = $('error');
    var secret = '';

    api('GET', '/api/v1/auth/mfa/enroll').then(function (res) {
      if (res.status === 409) {
        window.location.assign('/');
        return;
      }
      if (res.status !== 200 || !res.body) {
        show(err, failure(res, 'could not start enrollment'));
        return;
      }
      secret = res.body.secret || '';
      // Grouped in fours because the alternative is typing 32 unbroken
      // base32 characters into a phone, which people get wrong.
      $('secret').textContent = secret.replace(/(.{4})/g, '$1 ').trim();
      $('uri-link').href = res.body.uri || '#';

      /* The QR is a data: URI, which the CSP permits under img-src. If the
       * server could not render one the manual key is opened instead — it is a
       * complete path on its own, not a consolation prize. */
      if (res.body.qr) {
        $('qr').src = res.body.qr;
        $('qr-wrap').hidden = false;
      } else {
        $('manual').open = true;
      }
      try {
        $('account-label').textContent =
          decodeURIComponent(String(res.body.uri || '').split('?')[0].split('/').pop());
      } catch (e) {
        $('account-label').textContent = 'CMediaStack';
      }
      offer.hidden = false;
      $('code').focus();
    }, function () { show(err, 'could not reach the server'); });

    $('copy-secret').addEventListener('click', function () {
      copy(secret, $('copy-secret'));
    });

    submit($('confirm'), function () {
      hide(err);
      return api('POST', '/api/v1/auth/mfa/enroll/confirm', {
        secret: secret,
        code: $('code').value.trim()
      }).then(function (res) {
        if (res.status !== 200 || !res.body || !res.body.recovery_codes) {
          show(err, failure(res, 'that code was not accepted'));
          $('code').value = '';
          $('code').focus();
          return;
        }
        var list = $('code-list');
        res.body.recovery_codes.forEach(function (c) {
          var li = document.createElement('li');
          li.textContent = c;
          list.appendChild(li);
        });
        offer.hidden = true;
        codes.hidden = false;
        $('copy-codes').addEventListener('click', function () {
          copy(res.body.recovery_codes.join('\n'), $('copy-codes'));
        });
      }, function () { show(err, 'could not reach the server'); });
    });

    $('go-app').addEventListener('click', function () {
      window.location.assign('/');
    });

    $('logout').addEventListener('click', function () {
      api('POST', '/api/v1/auth/logout').then(function () {
        window.location.assign('/login');
      }, function () { window.location.assign('/login'); });
    });
  }

  var routes = {
    login: loginPage,
    signup: signupPage,
    reset: resetPage,
    setup: setupPage,
    enroll: enrollPage
  };

  var page = document.body.getAttribute('data-page');
  if (routes[page]) { routes[page](); }
})();
