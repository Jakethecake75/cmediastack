/* CMediaStack for Samsung televisions (ADR-0079): ask once for the server's
 * address, then open it in this window. Everything after that is the server's
 * own web interface, which knows a television's remote. */
'use strict';
(function () {
  var KEY = 'cms-server';
  var saved = localStorage.getItem(KEY);
  if (saved) { location.replace(saved); return; }

  var form = document.getElementById('f');
  var input = document.getElementById('url');
  var err = document.getElementById('err');
  form.hidden = false;
  input.focus();

  /* Only an address of the server's own origin is kept: a path would be
   * opened instead of the server's home. */
  function origin(text) {
    var t = text.trim();
    if (!/^https?:\/\//i.test(t)) { t = 'https://' + t; }
    try { return new URL(t).origin + '/'; } catch (e) { return null; }
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var u = origin(input.value);
    if (!u) { err.textContent = 'That is not an address.'; return; }
    localStorage.setItem(KEY, u);
    location.replace(u);
  });

  /* Back on this screen leaves the app, as it does on every television app's
   * first screen. */
  document.addEventListener('keydown', function (e) {
    if (e.keyCode === 10009 && window.tizen) { tizen.application.getCurrentApplication().exit(); }
  });
})();
