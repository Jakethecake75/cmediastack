/* CMediaStack — a television's remote (ADR-0079), loaded by every page.
 *
 * On a television the arrows move to the nearest thing that can be chosen in
 * that direction, OK presses it (the browser already does that), and Back
 * goes back. Anywhere else this file does nothing: the arrows scroll a page
 * and Escape is left to the player.
 *
 * It listens on window, which hears a key after every listener on document
 * has, so a key the player has already used (it calls preventDefault) is
 * left alone.
 */
'use strict';

(function () {
  if (!/Tizen|SMART-TV|Web0S/.test(navigator.userAgent)) { return; }
  document.documentElement.classList.add('tv');

  /* Samsung's Return and LG's Back. */
  var BACK = { 10009: true, 461: true };
  var DIRS = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' };
  var CHOOSABLE = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])';
  var TEXT = /^(text|search|url|email|password|number|tel)$/;

  function shown(el) {
    return !el.disabled && el.getClientRects().length > 0;
  }

  /* tabindex="-1" is a page saying "not by keys": the player's seek bar on a
   * television, which would otherwise take left and right for itself. Such
   * an element can still be where a move starts (the player's picture is). */
  function target(el) {
    return shown(el) && el.getAttribute('tabindex') !== '-1';
  }

  /* Distance between two spans on one axis: 0 when they overlap. */
  function gap(a1, a2, b1, b2) {
    return Math.max(0, b1 - a2, a1 - b2);
  }

  /* How far `to` is from `from` going `dir`, or -1 when it is not wholly
   * that way: Right from the last poster in a row goes nowhere rather than
   * to something below that pokes out further right. Distance along the way
   * counts once and distance across it three times, so Down from a poster
   * picks the one under it over a nearer one beside it. */
  function distance(from, to, dir) {
    var a = from.getBoundingClientRect(), b = to.getBoundingClientRect();
    var along, across;
    if (dir === 'down') {
      along = b.top - a.bottom; across = gap(a.left, a.right, b.left, b.right);
    } else if (dir === 'up') {
      along = a.top - b.bottom; across = gap(a.left, a.right, b.left, b.right);
    } else if (dir === 'right') {
      along = b.left - a.right; across = gap(a.top, a.bottom, b.top, b.bottom);
    } else {
      along = a.left - b.right; across = gap(a.top, a.bottom, b.top, b.bottom);
    }
    /* A pixel of overlap is rounding, not a direction. */
    if (along < -1) { return -1; }
    return Math.max(0, along) + 3 * across;
  }

  /* A field keeps the arrows it moves within: a slider its left and right,
   * a text field with something typed in it its left and right (the caret),
   * a text area all four. */
  function keepsArrow(el, dir) {
    var sideways = dir === 'left' || dir === 'right';
    if (el.tagName === 'TEXTAREA') { return true; }
    if (el.tagName !== 'INPUT') { return false; }
    if (el.type === 'range') { return sideways; }
    return TEXT.test(el.type) && sideways && el.value !== '';
  }

  function move(dir) {
    var from = document.activeElement;
    var all = Array.prototype.filter.call(document.querySelectorAll(CHOOSABLE), target);
    if (!from || from === document.body || !shown(from)) {
      /* Nothing chosen yet: start in the open view, else at the top. */
      var view = document.querySelector('.view:not([hidden])');
      var inView = view ? all.filter(function (el) { return view.contains(el); }) : [];
      return inView[0] || all[0] || null;
    }
    var best = null, bestScore = Infinity;
    all.forEach(function (el) {
      if (el === from) { return; }
      var d = distance(from, el, dir);
      if (d >= 0 && d < bestScore) { best = el; bestScore = d; }
    });
    return best;
  }

  function leave() {
    if (window.tizen) {
      try { window.tizen.application.getCurrentApplication().exit(); } catch (e) { /* not ours to close */ }
    }
  }

  /* Back: in the player it is Escape, which closes its menu or leaves it.
   * On the first screen (Home, or signing in) it leaves the app. Anywhere
   * else it is the browser's Back, and the views are in its history. */
  function back() {
    var watch = document.querySelector('[data-view="watch"]');
    if (watch && !watch.hidden) {
      (document.activeElement || document.body).dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      return;
    }
    var page = document.body.getAttribute('data-page');
    var view = (window.location.hash || '').replace(/^#/, '').split('/')[0];
    if (page === 'login' || (page === 'app' && (view === '' || view === 'home'))) {
      leave();
      return;
    }
    window.history.back();
  }

  window.addEventListener('keydown', function (e) {
    if (e.defaultPrevented) { return; }
    if (BACK[e.keyCode]) {
      e.preventDefault();
      back();
      return;
    }
    var dir = DIRS[e.key];
    if (!dir) { return; }
    var from = document.activeElement;
    if (from && from !== document.body && keepsArrow(from, dir)) { return; }
    /* The arrow is the remote's now, whether or not anything is that way:
     * left to the browser it would scroll, or work a slider it is leaving. */
    e.preventDefault();
    var next = move(dir);
    if (next) { next.focus(); }
  });

  /* The remote's play, pause, fast-forward and rewind keys reach a page only
   * once asked for, and only the television's own API can ask. They are
   * handed to the player as the keys it already knows. */
  var MEDIA = { 10252: 'k', 415: 'k', 19: 'k', 417: 'ArrowRight', 412: 'ArrowLeft' };
  try {
    ['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaFastForward', 'MediaRewind'].forEach(function (k) {
      window.tizen.tvinputdevice.registerKey(k);
    });
  } catch (e) { /* no television API in this page: the keys stay the television's */ }
  window.addEventListener('keydown', function (e) {
    var key = MEDIA[e.keyCode];
    var player = document.getElementById('watch-player');
    if (!key || !player) { return; }
    e.preventDefault();
    player.dispatchEvent(new KeyboardEvent('keydown', { key: key, bubbles: true, cancelable: true }));
  });
})();
