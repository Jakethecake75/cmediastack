/* CMediaStack — authenticated bundle.
 *
 * Hand-written, no framework. CSP forbids inline script and inline style, so
 * this file uses addEventListener and class/hidden toggling only — never
 * element.style.x or onclick attributes, both of which the policy blocks.
 *
 * Nothing here is a security control. The navigation hides sections the
 * caller's role does not carry, which is a courtesy so people are not shown
 * buttons that would fail; the server refuses those routes regardless, and the
 * admin ones answer 404 rather than 403 so their existence is not disclosed.
 * Treat every check in this file as cosmetic and every check on the server as
 * real.
 */
'use strict';

(function () {
  var CSRF_COOKIE = 'cms_csrf';
  var CSRF_HEADER = 'X-CSRF-Token';

  var me = null;

  /* Views, in nav order. `perm` is the permission that reveals the tab; a view
   * with no perm is available to every signed-in account. */
  var VIEWS = [
    { id: 'overview',  label: 'Overview' },
    { id: 'library',   label: 'Library',   perm: 'media.browse' },
    /* Adding a series before any of it is on disk (ADR-0025). */
    { id: 'add',       label: 'Add',       perm: 'library.edit' },
    { id: 'identify',  label: 'Identify',  perm: 'library.edit' },
    { id: 'wanted',    label: 'Wanted',    perm: 'media.browse' },
    /* Reached from a Play button in the library rather than from the nav —
     * "Watch" with nothing chosen is an empty screen with no way forward. */
    { id: 'watch',     label: 'Watch',     perm: 'media.browse', hidden: true },
    { id: 'search',    label: 'Search',    perm: 'acquisition.search' },
    { id: 'queue',     label: 'Queue',     perm: 'acquisition.queue' },
    /* What is popular, and a request beside each (ADR-0043). */
    { id: 'discover',  label: 'Discover',  perm: 'media.browse' },
    { id: 'requests',  label: 'Requests',  perm: 'request.submit' },
    /* A checklist over the settings below, for a new instance (ADR-0065). */
    { id: 'start',     label: 'Getting started', perm: 'admin.system' },
    { id: 'storage',   label: 'Storage',   perm: 'library.root_folders' },
    { id: 'indexers',  label: 'Indexers',  perm: 'admin.indexers' },
    /* The provider's key and the tunnel. Both were reachable only with curl,
     * and an error message sent operators to a Metadata screen that did not
     * exist. */
    { id: 'metadata',  label: 'Metadata',  perm: 'admin.system' },
    { id: 'notifications', label: 'Notifications', perm: 'admin.system' },
    { id: 'network',   label: 'Network',   perm: 'admin.network' },
    { id: 'users',     label: 'Accounts',  perm: 'admin.users' },
    { id: 'approvals', label: 'Approvals', perm: 'account.approve' },
    { id: 'invites',   label: 'Invites',   perm: 'account.invite' },
    { id: 'sessions',  label: 'Sessions' },
    { id: 'tokens',    label: 'API tokens' },
    { id: 'security',  label: 'Security' },
    { id: 'migrate',   label: 'Migrate',   perm: 'admin.system' },
    /* Read, never changed: there is no route that edits or removes a line
     * (ADR-0031). */
    { id: 'audit',     label: 'Audit log', perm: 'admin.audit' },
    { id: 'tasks',     label: 'Tasks',     perm: 'admin.system' },
    /* Taken and listed here; never downloaded or restored here (ADR-0029). */
    { id: 'backups',   label: 'Backups',   perm: 'admin.system' },
    /* Every health check at once, and what the process has been saying
     * (ADR-0040). */
    { id: 'health',    label: 'Health',    perm: 'admin.system' }
  ];

  // -------------------------------------------------------------------------
  // plumbing
  // -------------------------------------------------------------------------

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
      // Clear both banners before a mutation starts. Without this, an operator
      // who clicks Approve while an earlier success message is still on screen
      // reads that message as the answer to the click they just made — and the
      // one time it matters is the time the new request failed.
      $('error').hidden = true;
      $('notice').hidden = true;
    }
    return fetch(path, opts).then(function (res) {
      return res.text().then(function (text) {
        var parsed = null;
        if (text) { try { parsed = JSON.parse(text); } catch (e) { parsed = null; } }
        return { status: res.status, body: parsed };
      });
    }, function () {
      return { status: 0, body: null };
    });
  }

  function failure(res, fallback) {
    if (res.body && typeof res.body.error === 'string' && res.body.error) {
      return res.body.error;
    }
    if (res.status === 0) { return 'could not reach the server'; }
    if (res.status === 429) { return 'too many requests; wait a while'; }
    return fallback;
  }

  function fail(res, fallback) { flash($('error'), failure(res, fallback)); }
  function ok(text) { flash($('notice'), text); }

  /* refuse reports a problem with what the OPERATOR did, before any request is
   * made. It exists because the obvious shortcut — fail({status: 0}, 'pick a
   * role first') — does not work: status 0 means "the fetch never completed" in
   * failure(), so every one of these messages came out as "could not reach the
   * server". An operator who left a field blank was being told their server was
   * down. Found in a browser, and it had been wrong in three places. */
  function refuse(text) { flash($('error'), text); }

  function flash(el, text) {
    el.textContent = text;
    el.hidden = false;
    if (el.id === 'notice') {
      setTimeout(function () { el.hidden = true; }, 6000);
    }
  }

  /* el builds a node. Text always goes in through textContent, so a username,
   * a note or a user agent from the database cannot become markup. There is no
   * innerHTML anywhere in this file, deliberately. */
  function el(tag, cls, text) {
    var node = document.createElement(tag);
    if (cls) { node.className = cls; }
    if (text !== undefined && text !== null) { node.textContent = String(text); }
    return node;
  }

  function clear(node) { while (node.firstChild) { node.removeChild(node.firstChild); } }

  /* every(3600) -> "every 1h". Raw seconds are readable at 60 and useless at
   * 86400, and this column exists to be glanced at. */
  function every(seconds) {
    if (!seconds) { return 'manual only'; }
    var units = [['d', 86400], ['h', 3600], ['m', 60], ['s', 1]];
    for (var i = 0; i < units.length; i++) {
      var n = seconds / units[i][1];
      if (n >= 1 && n === Math.floor(n)) { return 'every ' + n + units[i][0]; }
    }
    return 'every ' + seconds + 's';
  }

  function when(value) {
    if (!value) { return 'never'; }
    var d = new Date(value);
    return isNaN(d.getTime()) ? String(value) : d.toLocaleString();
  }

  function can(perm) {
    return !!(me && me.permissions && me.permissions.indexOf(perm) !== -1);
  }

  function button(label, cls, onClick) {
    var b = el('button', cls, label);
    b.type = 'button';
    b.addEventListener('click', function () { onClick(b); });
    return b;
  }

  function empty(container, text) {
    clear(container);
    container.appendChild(el('div', 'empty', text));
  }

  function copy(text, btn) {
    var restore = btn.textContent;
    var settle = function (good) {
      btn.textContent = good ? 'Copied' : 'Press Ctrl+C';
      setTimeout(function () { btn.textContent = restore; }, 1600);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () { settle(true); },
        function () { settle(false); });
    } else { settle(false); }
  }

  function submit(form, handler) {
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var btn = form.querySelector('button[type="submit"]');
      if (btn) { btn.disabled = true; }
      var done = function () { if (btn) { btn.disabled = false; } };
      var p = handler();
      if (p && typeof p.then === 'function') { p.then(done, done); } else { done(); }
    });
  }

  // -------------------------------------------------------------------------
  // navigation
  // -------------------------------------------------------------------------

  var loaders = {};

  function visibleViews() {
    return VIEWS.filter(function (v) { return !v.perm || can(v.perm); });
  }

  /* Views that exist but are not tabs. They are still permission-checked; they
   * simply have no entry in the bar, because they are reached from somewhere
   * that already knows what they are about. */
  function navViews() {
    return visibleViews().filter(function (v) { return !v.hidden; });
  }

  function buildNav() {
    var nav = $('nav');
    clear(nav);
    navViews().forEach(function (v) {
      var b = el('button', null, v.label);
      b.type = 'button';
      b.setAttribute('data-target', v.id);
      b.addEventListener('click', function () { window.location.hash = '#' + v.id; });
      nav.appendChild(b);
    });
  }

  function currentView() {
    var want = (window.location.hash || '').replace(/^#/, '');
    var allowed = visibleViews();
    for (var i = 0; i < allowed.length; i++) {
      if (allowed[i].id === want) { return want; }
    }
    return 'overview';
  }

  function showView() {
    var target = currentView();
    var views = document.querySelectorAll('.view');
    for (var i = 0; i < views.length; i++) {
      views[i].hidden = views[i].getAttribute('data-view') !== target;
    }
    var tabs = $('nav').querySelectorAll('button');
    for (var j = 0; j < tabs.length; j++) {
      if (tabs[j].getAttribute('data-target') === target) {
        tabs[j].setAttribute('aria-current', 'page');
      } else {
        tabs[j].removeAttribute('aria-current');
      }
    }
    $('error').hidden = true;
    /* Adding for a request lasts while the Add screen is open: leaving it is
     * leaving that errand, and the next visit to Add is an ordinary one. */
    if (target !== 'add') { addingFor = null; }
    if (loaders[target]) { loaders[target](); }
  }

  // -------------------------------------------------------------------------
  // overview
  // -------------------------------------------------------------------------

  function stat(value, label) {
    var box = el('div', 'stat');
    box.appendChild(el('div', 'value', value));
    box.appendChild(el('div', 'label', label));
    return box;
  }

  loaders.overview = function () {
    var cards = $('overview-cards');
    clear(cards);
    cards.appendChild(stat(me.username, 'signed in as'));
    cards.appendChild(stat(me.role, 'role'));
    cards.appendChild(stat(me.permissions.length, 'permissions'));
    cards.appendChild(stat(me.all_libraries ? 'all' : (me.library_ids || []).length, 'libraries'));

    if (can('account.approve')) {
      api('GET', '/api/v1/accounts/requests').then(function (res) {
        if (res.status === 200 && res.body) {
          cards.appendChild(stat(res.body.count, 'requests waiting'));
        }
      });
    }
  };

  // -------------------------------------------------------------------------
  // approvals
  // -------------------------------------------------------------------------

  var assignableRoles = [];

  function loadRoles() {
    if (assignableRoles.length || !can('account.approve')) { return Promise.resolve(); }
    return api('GET', '/api/v1/roles').then(function (res) {
      if (res.status === 200 && res.body && res.body.roles) {
        assignableRoles = res.body.roles;
        ratingCeilings = res.body.rating_ceilings || [];
      }
    });
  }

  /* What an account may see of the library (ADR-0037): every library, or the
   * root folders ticked, and a rating ceiling. The roots offered are the ones
   * the person choosing can see themselves — the server refuses a grant wider
   * than the grantor's, and a form offering one would only produce refusals. */
  var ratingCeilings = [];
  var grantableRoots = null;

  function loadGrantableRoots() {
    if (grantableRoots) { return Promise.resolve(); }
    return api('GET', '/api/v1/rootfolders').then(function (res) {
      grantableRoots = (res.status === 200 && res.body && res.body.root_folders) || [];
    });
  }

  function describeAccess(a) {
    var libs = a.all_libraries ? 'every library'
      : (a.library_ids || []).map(function (id) {
        var rf = (grantableRoots || []).filter(function (r) { return r.id === id; })[0];
        return rf ? (rf.label || rf.path) : 'root folder ' + id;
      }).join(', ') || 'no library';
    var ceiling = ratingCeilings.filter(function (c) { return c.rank === a.rating_ceiling; })[0];
    return libs + ' · ' + (a.rating_ceiling ? 'rated ' + (ceiling ? ceiling.label : 'up to rank ' + a.rating_ceiling)
      : 'any rating');
  }

  function accessPicker(current) {
    current = current || { all_libraries: true, library_ids: [], rating_ceiling: 0 };
    var box = el('div', 'access');

    var libWrap = el('div');
    libWrap.appendChild(el('label', null, 'Libraries'));
    var scope = el('select');
    [['all', 'Every library'], ['some', 'Only the ones ticked']].forEach(function (o) {
      var opt = el('option', null, o[1]);
      opt.value = o[0];
      scope.appendChild(opt);
    });
    scope.value = current.all_libraries ? 'all' : 'some';
    libWrap.appendChild(scope);
    box.appendChild(libWrap);

    var roots = el('div', 'access-roots');
    var boxes = [];
    (grantableRoots || []).forEach(function (rf) {
      var lab = el('label', 'checkbox');
      var cb = el('input');
      cb.type = 'checkbox';
      cb.value = String(rf.id);
      cb.checked = (current.library_ids || []).indexOf(rf.id) >= 0;
      boxes.push(cb);
      lab.appendChild(cb);
      lab.appendChild(document.createTextNode(' ' + (rf.label || rf.path) + ' (' + rf.kind + ')'));
      roots.appendChild(lab);
    });
    if (!boxes.length) { roots.appendChild(el('div', 'why', 'There are no root folders to choose from.')); }
    box.appendChild(roots);
    function sync() { roots.hidden = scope.value !== 'some'; }
    scope.addEventListener('change', sync);
    sync();

    var ceilWrap = el('div');
    ceilWrap.appendChild(el('label', null, 'Rating ceiling'));
    var ceil = el('select');
    (ratingCeilings.length ? ratingCeilings : [{ rank: 0, label: 'No limit' }]).forEach(function (c) {
      var opt = el('option', null, c.label);
      opt.value = String(c.rank);
      if (c.rank === (current.rating_ceiling || 0)) { opt.selected = true; }
      ceil.appendChild(opt);
    });
    ceilWrap.appendChild(ceil);
    box.appendChild(ceilWrap);
    box.appendChild(el('div', 'why',
      'With a ceiling, a title with no rating is hidden too: rate it by hand on its page.'));

    return {
      node: box,
      value: function () {
        return {
          all_libraries: scope.value === 'all',
          library_ids: boxes.filter(function (b) { return b.checked; })
            .map(function (b) { return Number(b.value); }),
          rating_ceiling: Number(ceil.value)
        };
      }
    };
  }

  function roleSelect() {
    var sel = el('select');
    assignableRoles.forEach(function (r) {
      var opt = el('option', null, r.name + ' (rank ' + r.rank + ')');
      opt.value = String(r.id);
      sel.appendChild(opt);
    });
    if (!assignableRoles.length) {
      var none = el('option', null, 'no assignable role');
      none.value = '';
      sel.appendChild(none);
      sel.disabled = true;
    }
    return sel;
  }

  // -------------------------------------------------------------------------
  // requests
  // -------------------------------------------------------------------------

  /* What a request's state means to the person reading it. The raw word is
   * accurate and unhelpful: "approved" does not tell somebody whether anything
   * is actually happening, which is the only thing they want to know. */
  function requestState(rq) {
    if (rq.state === 'fulfilled') { return ['good', 'in your library']; }
    if (rq.state === 'denied') { return ['bad', 'declined']; }
    if (rq.state === 'approved') {
      if (rq.info_hash) { return ['good', 'downloading']; }
      /* Linked to a library item (ADR-0028): it is in the library, and it is
       * fulfilled when a file of it arrives. */
      if (rq.media_item_id) { return ['warn', 'in the library — nothing on disk yet']; }
      return ['warn', 'approved — not in the library yet'];
    }
    return ['warn', 'waiting for approval'];
  }

  /* Problems reported with titles (ADR-0042). An editor sees every one in
   * scope and resolves it; anyone else sees their own. */
  function loadIssues() {
    var list = $('issues-list');
    if (!list) { return; }
    var all = $('issues-all') && $('issues-all').checked;
    api('GET', '/api/v1/issues' + (all ? '?all=true' : '')).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load problems')); return; }
      if (!res.body.count) { empty(list, 'Nothing reported.'); return; }
      clear(list);
      res.body.issues.forEach(function (is) {
        var what = is.title + (is.episode ? ' S' + pad(is.season) + 'E' + pad(is.episode) : '');
        var r = row(what, is.kind.replace('_', ' ') + ' · reported by ' + (is.reported_by || 'somebody') +
          ' ' + when(is.reported_at));
        r.firstChild.appendChild(el('span', is.state === 'open' ? 'badge warn' : 'badge good', is.state));
        if (is.note) { r.appendChild(el('div', 'muted small', is.note)); }
        if (is.resolution) {
          r.appendChild(el('div', 'meta', 'Resolved by ' + is.resolved_by + ': ' + is.resolution));
        }
        if (is.state === 'open' && can('library.edit')) {
          var input = el('input');
          input.type = 'text';
          input.maxLength = 500;
          input.placeholder = 'What was done';
          var acts = actions(r);
          acts.appendChild(input);
          acts.appendChild(button('Resolve', 'primary', function (b) {
            if (!input.value.trim()) { refuse('say what was done'); return; }
            b.disabled = true;
            api('POST', '/api/v1/issues/' + is.id + '/resolve', { resolution: input.value }).then(function (u) {
              b.disabled = false;
              if (u.status !== 200) { fail(u, 'could not resolve that'); return; }
              ok('Resolved.');
              loadIssues();
            });
          }));
        }
        list.appendChild(r);
      });
    });
  }

  /* Reporting one, from a title's page. */
  function reportProblemRow(it) {
    var r = row('Something wrong?', 'Tell whoever looks after the library.');
    var kind = el('select');
    var kinds = [['video', 'The picture'], ['audio', 'The sound'], ['subtitles', 'The subtitles'],
      ['wrong_title', 'It is the wrong title'], ['other', 'Something else']];
    if (it.kind !== 'movie' && it.kind !== 'series') {
      kinds = kinds.filter(function (k) { return k[0] !== 'video' && k[0] !== 'subtitles'; });
    }
    /* A book has no sound either (ADR-0048). */
    if (it.kind === 'book') {
      kinds = kinds.filter(function (k) { return k[0] !== 'audio'; });
    }
    kinds.forEach(function (k) {
      var o = el('option', null, k[1]);
      o.value = k[0];
      kind.appendChild(o);
    });
    var note = el('input');
    note.type = 'text';
    note.maxLength = 500;
    note.placeholder = 'What happens (optional)';
    var acts = actions(r);
    acts.appendChild(kind);
    acts.appendChild(note);
    acts.appendChild(button('Report', 'ghost', function (b) {
      b.disabled = true;
      api('POST', '/api/v1/issues', { media_item_id: it.id, kind: kind.value, note: note.value }).then(function (u) {
        b.disabled = false;
        if (u.status !== 201 && u.status !== 200) { fail(u, 'could not report that'); return; }
        note.value = '';
        ok(u.body.message);
      });
    }));
    return r;
  }

  loaders.discover = function () {
    var list = $('discover-list');
    var section = $('discover-section').value;
    empty(list, 'Loading…');
    api('GET', '/api/v1/discover/' + section).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not read that list')); return; }
      if (!res.body.count) { empty(list, res.body.note || 'Nothing listed.'); return; }
      clear(list);
      res.body.items.forEach(function (m) {
        var r = row(m.title + (m.year ? ' (' + m.year + ')' : ''), m.kind === 'series' ? 'series' : 'film');
        if (m.in_library) { r.firstChild.appendChild(el('span', 'badge good', 'in the library')); }
        if (m.requested) { r.firstChild.appendChild(el('span', 'badge warn', 'requested')); }
        if (m.overview) { r.appendChild(el('div', 'muted small', m.overview)); }
        if (!m.in_library && !m.requested && can('request.submit')) {
          actions(r).appendChild(button('Request', 'primary', function (b) {
            b.disabled = true;
            var body = { kind: m.kind === 'series' ? 'series' : 'movie', title: m.title };
            if (m.year) { body.year = m.year; }
            api('POST', '/api/v1/requests', body).then(function (u) {
              if (u.status !== 201 && u.status !== 200) { b.disabled = false; fail(u, 'could not request that'); return; }
              ok((u.body && u.body.message) || 'Requested.');
              loaders.discover();
            });
          }));
        }
        list.appendChild(r);
      });
    });
  };

  loaders.requests = function () {
    loadIssues();
    var list = $('requests-list');
    empty(list, 'Loading…');

    api('GET', '/api/v1/requests').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load requests')); return; }

      /* Said in words, because a short list has two very different causes:
       * nobody has asked for anything, or you can only see your own. */
      $('request-scope').textContent = 'Showing ' + (res.body.scope || 'your own requests') + '.';

      if (!res.body.count) { empty(list, 'Nothing requested yet.'); return; }

      clear(list);
      res.body.requests.forEach(function (rq) {
        var tone = requestState(rq);
        var title = rq.title + (rq.year ? ' (' + rq.year + ')' : '');
        var item = row(title);
        item.firstChild.appendChild(el('span', 'badge ' + tone[0], tone[1]));

        var pairs = [
          ['kind', rq.kind === 'series' ? 'series' : 'film'],
          ['asked by', rq.requested_by || '(deleted account)'],
          ['asked', when(rq.requested_at)]
        ];
        if (rq.waiting > 1) { pairs.push(['also waiting', (rq.waiting - 1) + ' other(s)']); }
        if (rq.followers) { pairs.push(['waiting', rq.followers.join(', ')]); }
        if (rq.decided_by) { pairs.push(['decided by', rq.decided_by]); }
        /* Only when the server named it: the name is a read of the library,
         * given to somebody who may browse it. */
        if (rq.item && rq.item.name) { pairs.push(['in the library as', rq.item.name]); }
        item.appendChild(facts(pairs));

        if (rq.note) { item.appendChild(el('div', 'meta', 'Note: ' + rq.note)); }
        /* Only on a denial. An approval's reason is either empty or the
         * machine's own auto-approval sentence, and rendering that in the
         * same amber "something is wrong" style as a refusal misreads the one
         * case where the styling matters. */
        if (rq.state === 'denied' && rq.decision_reason) {
          item.appendChild(el('div', 'why', 'Reason: ' + rq.decision_reason));
        }

        /* Approval does not download anything, so an approved request with
         * nothing in the library for it is a real state somebody has to act
         * on. Saying so is the difference between a queue that works and one
         * people stop trusting. Acting on it is adding the title and linking
         * the request to it (ADR-0028); it is then fulfilled when a file of
         * that title arrives, whichever search grabbed it. */
        if (rq.state === 'approved' && can('request.approve')) {
          var next = actions(item);
          if (!rq.media_item_id) {
            /* Not when something is already downloading for it: a grab that
             * named the request closes it by itself (ADR-0017). */
            if (!rq.info_hash) {
              item.insertBefore(el('div', 'why', 'Approved, and nothing is in the library ' +
                'for it yet. Add it, then search for it from its page.'), next);
            }
            if (can('library.edit')) {
              next.appendChild(button('Add to the library\u2026', 'primary', function () {
                startAddForRequest(rq);
              }));
            }
          } else {
            next.appendChild(openInLibrary(rq.media_item_id));
            if (can('library.edit')) {
              next.appendChild(button('Not this one? Choose another\u2026', 'ghost', function () {
                startAddForRequest(rq);
              }));
            }
          }
        }

        if (rq.state === 'pending' && can('request.approve')) {
          var acts = actions(item);
          acts.appendChild(button('Approve', 'primary', function (b) {
            b.disabled = true;
            api('POST', '/api/v1/requests/' + rq.id + '/approve').then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not approve that request'); return; }
              ok((r.body && r.body.message) || 'Approved.');
              loaders.requests();
            });
          }));

          var why = el('input');
          why.type = 'text';
          why.maxLength = 500;
          why.placeholder = 'why not (required)';
          var wrap = el('div');
          wrap.appendChild(el('label', null, 'Reason'));
          wrap.appendChild(why);
          acts.appendChild(wrap);

          acts.appendChild(button('Deny', 'danger', function (b) {
            if (!why.value.trim()) {
              refuse('a denial needs a reason; the requester sees it');
              return;
            }
            b.disabled = true;
            api('POST', '/api/v1/requests/' + rq.id + '/deny', { reason: why.value })
              .then(function (r) {
                b.disabled = false;
                if (r.status !== 200) { fail(r, 'could not deny that request'); return; }
                ok('Denied. The requester can see the reason.');
                loaders.requests();
              });
          }));
        }

        list.appendChild(item);
      });
    });
  };

  // -------------------------------------------------------------------------
  // accounts
  // -------------------------------------------------------------------------

  /* State is what the row says; "can sign in" is what an operator actually
   * wants to know, and the two differ for an account that never enrolled an
   * authenticator. Colour is never the only signal — every badge carries its
   * word — because a red pill and an amber one are the same pill to a good
   * number of people. */
  function stateBadge(u) {
    var tone = u.state === 'active' ? 'good' : (u.state === 'suspended' ? 'bad' : 'warn');
    return el('span', 'badge ' + tone, u.state.replace('_', ' '));
  }

  loaders.users = function () {
    var list = $('users-list');
    empty(list, 'Loading…');

    loadRoles().then(loadGrantableRoots).then(function () {
      fillCreateUserForm();
      loadRoleEditor();
      return api('GET', '/api/v1/admin/users');
    }).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load accounts')); return; }
      if (!res.body.count) { empty(list, 'No accounts.'); return; }

      clear(list);
      res.body.users.forEach(function (u) {
        var item = row(u.username);
        item.firstChild.appendChild(stateBadge(u));

        item.appendChild(facts([
          ['role', u.role + ' (rank ' + u.rank + ')'],
          ['can sign in', u.enrolled ? 'yes' : 'no — never enrolled an authenticator'],
          ['joined', when(u.created_at)],
          ['last seen', when(u.last_login_at)],
          ['sees', describeAccess(u)]
        ]));

        /* Said in words rather than by hiding the buttons. An operator who
         * cannot act on a row should learn why, not wonder where the controls
         * went — and "you may not act on yourself, a peer or a superior" is not
         * a rule anybody infers from an absence. */
        if (!u.manageable) {
          var why = u.id === (me && me.id)
            ? 'This is you. An account may not change its own role or state.'
            : 'At or above your own rank, so not yours to change.';
          item.appendChild(el('div', 'why', why));
          list.appendChild(item);
          return;
        }

        var acts = actions(item);

        if (u.state === 'suspended') {
          acts.appendChild(button('Reactivate', 'primary', function (b) {
            b.disabled = true;
            api('PATCH', '/api/v1/admin/users/' + u.id, { state: 'active' }).then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not reactivate that account'); return; }
              ok(u.username + ' reactivated.');
              loaders.users();
            });
          }));
        } else {
          acts.appendChild(button('Suspend', 'danger', function (b) {
            b.disabled = true;
            api('PATCH', '/api/v1/admin/users/' + u.id, {
              state: 'suspended', reason: 'suspended from the accounts screen'
            }).then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not suspend that account'); return; }
              ok(u.username + ' suspended. Their sessions, tokens and invites are revoked.');
              loaders.users();
            });
          }));
        }

        /* Role changes are a separate request from state changes: sending both
         * at once is refused by the API, because it makes the audit line
         * ambiguous about what was intended. */
        var wrap = el('div');
        wrap.appendChild(el('label', null, 'Role'));
        var sel = roleSelect();
        sel.value = String(u.role_id);
        wrap.appendChild(sel);
        acts.appendChild(wrap);

        /* Which libraries, and up to which rating (ADR-0037). Shown on
         * demand: it is changed rarely, and a picker on every row would bury
         * the actions used daily. */
        var accessBox = el('div', 'access-edit');
        accessBox.hidden = true;
        item.appendChild(accessBox);
        acts.appendChild(button('Change access', 'ghost', function () {
          if (!accessBox.hidden) { accessBox.hidden = true; return; }
          clear(accessBox);
          var picker = accessPicker(u);
          accessBox.appendChild(picker.node);
          accessBox.appendChild(button('Save access', 'primary', function (b) {
            b.disabled = true;
            api('PUT', '/api/v1/admin/users/' + u.id + '/access', picker.value()).then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not change that account\'s access'); return; }
              ok(u.username + ' now sees ' + describeAccess(r.body) + '. It applies from their next request.');
              loaders.users();
            });
          }));
          accessBox.hidden = false;
        }));

        acts.appendChild(button('Change role', 'ghost', function (b) {
          if (!sel.value || Number(sel.value) === u.role_id) {
            refuse('pick a different role first');
            return;
          }
          b.disabled = true;
          api('PATCH', '/api/v1/admin/users/' + u.id, { role_id: Number(sel.value) })
            .then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not change that role'); return; }
              ok(u.username + "'s role changed. It applies to their open sessions immediately.");
              loaders.users();
            });
        }));

        list.appendChild(item);
      });
    });
  };

  loaders.approvals = function () {
    var list = $('approvals-list');
    empty(list, 'Loading…');
    loadRoles().then(loadGrantableRoots).then(function () {
      return api('GET', '/api/v1/accounts/requests');
    }).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load requests')); return; }
      if (!res.body.count) { empty(list, 'Nothing waiting.'); return; }

      clear(list);
      res.body.requests.forEach(function (req) {
        var item = el('div', 'item');
        item.appendChild(el('div', 'title', req.username));
        item.appendChild(el('div', 'meta', req.email));
        if (req.note) { item.appendChild(el('div', 'meta', 'Note: ' + req.note)); }
        item.appendChild(el('div', 'meta',
          'from ' + (req.source_ip || 'unknown') + ' · requested ' + when(req.created_at) +
          ' · expires ' + when(req.expires_at)));
        if (req.user_agent) { item.appendChild(el('div', 'meta', req.user_agent)); }

        var actions = el('div', 'actions');
        var wrap = el('div');
        wrap.appendChild(el('label', null, 'Role'));
        var sel = roleSelect();
        wrap.appendChild(sel);
        actions.appendChild(wrap);
        var picker = accessPicker();
        actions.appendChild(picker.node);

        actions.appendChild(button('Approve', 'primary', function (b) {
          if (!sel.value) { refuse('no role to assign'); return; }
          b.disabled = true;
          var access = picker.value();
          api('POST', '/api/v1/accounts/requests/' + req.id + '/approve', {
            role_id: Number(sel.value),
            all_libraries: access.all_libraries,
            library_ids: access.library_ids,
            rating_ceiling: access.rating_ceiling
          }).then(function (r) {
            b.disabled = false;
            if (r.status !== 201) { fail(r, 'could not approve that request'); return; }
            ok(req.username + ' approved. They must enroll an authenticator before the account works.');
            loaders.approvals();
          });
        }));

        actions.appendChild(button('Deny', 'danger', function (b) {
          b.disabled = true;
          api('POST', '/api/v1/accounts/requests/' + req.id + '/deny', { reason: 'denied by reviewer' })
            .then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not deny that request'); return; }
              ok(req.username + ' denied.');
              loaders.approvals();
            });
        }));

        item.appendChild(actions);
        list.appendChild(item);
      });
    });
  };

  // -------------------------------------------------------------------------
  // invites
  // -------------------------------------------------------------------------

  var invitePicker = null;

  loaders.invites = function () {
    var sel = $('invite-role');
    loadRoles().then(loadGrantableRoots).then(function () {
      if (!sel.options.length) {
        assignableRoles.forEach(function (r) {
          var opt = el('option', null, r.name + ' (rank ' + r.rank + ')');
          opt.value = String(r.id);
          sel.appendChild(opt);
        });
      }
      var holder = $('invite-access');
      if (holder && !invitePicker) {
        invitePicker = accessPicker();
        holder.appendChild(invitePicker.node);
      }
    });

    var list = $('invites-list');
    empty(list, 'Loading…');
    api('GET', '/api/v1/invites').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load invites')); return; }
      if (!res.body.count) { empty(list, 'No invites.'); return; }

      clear(list);
      res.body.invites.forEach(function (inv) {
        var item = el('div', 'item');
        var title = el('div', 'title', inv.role);
        var cls = inv.state === 'active' ? 'badge good'
          : (inv.state === 'redeemed' ? 'badge' : 'badge warn');
        title.appendChild(el('span', cls, inv.state));
        if (inv.auto_approve) { title.appendChild(el('span', 'badge warn', 'auto-approve')); }
        item.appendChild(title);
        if (inv.note) { item.appendChild(el('div', 'meta', inv.note)); }
        item.appendChild(el('div', 'meta',
          'issued ' + when(inv.created_at) + ' · expires ' + when(inv.expires_at)));
        item.appendChild(el('div', 'meta', 'sees ' + describeAccess(inv)));

        if (inv.state === 'active') {
          var actions = el('div', 'actions');
          actions.appendChild(button('Revoke', 'danger', function (b) {
            b.disabled = true;
            api('DELETE', '/api/v1/invites/' + inv.id).then(function (r) {
              b.disabled = false;
              if (r.status !== 200) { fail(r, 'could not revoke that invite'); return; }
              ok('Invite revoked.');
              loaders.invites();
            });
          }));
          item.appendChild(actions);
        }
        list.appendChild(item);
      });
    });
  };

  function wireInviteForm() {
    var form = $('invite-form');
    if (!form) { return; }
    submit(form, function () {
      var out = $('invite-result');
      out.hidden = true;
      var access = invitePicker ? invitePicker.value()
        : { all_libraries: true, library_ids: [], rating_ceiling: 0 };
      return api('POST', '/api/v1/invites', {
        role_id: Number($('invite-role').value),
        all_libraries: access.all_libraries,
        library_ids: access.library_ids,
        rating_ceiling: access.rating_ceiling,
        auto_approve: $('invite-auto').checked,
        ttl_hours: Number($('invite-ttl').value),
        note: $('invite-note').value
      }).then(function (res) {
        if (res.status !== 201 || !res.body) { fail(res, 'could not issue that invite'); return; }
        clear(out);
        out.appendChild(el('div', null, res.body.message));
        out.appendChild(el('code', 'secret', res.body.code));
        out.appendChild(button('Copy code', 'ghost', function (b) { copy(res.body.code, b); }));
        out.hidden = false;
        loaders.invites();
      });
    });
  }

  // -------------------------------------------------------------------------
  // sessions
  // -------------------------------------------------------------------------

  loaders.sessions = function () {
    var list = $('sessions-list');
    empty(list, 'Loading…');
    api('GET', '/api/v1/me/sessions').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load sessions')); return; }
      if (!res.body.count) { empty(list, 'No live sessions.'); return; }

      clear(list);
      res.body.sessions.forEach(function (s) {
        var item = el('div', 'item');
        var title = el('div', 'title', s.source_ip || 'unknown address');
        if (s.current) { title.appendChild(el('span', 'badge good', 'this device')); }
        if (!s.mfa_satisfied) { title.appendChild(el('span', 'badge warn', 'no second factor')); }
        item.appendChild(title);
        item.appendChild(el('div', 'meta', s.user_agent || 'no user agent'));
        item.appendChild(el('div', 'meta',
          'started ' + when(s.created_at) + ' · last seen ' + when(s.last_seen_at) +
          ' · expires ' + when(s.expires_at)));

        var actions = el('div', 'actions');
        actions.appendChild(button(s.current ? 'Sign out here' : 'Revoke', 'danger', function (b) {
          b.disabled = true;
          api('DELETE', '/api/v1/me/sessions/' + encodeURIComponent(s.id)).then(function (r) {
            b.disabled = false;
            if (r.status !== 200) { fail(r, 'could not revoke that session'); return; }
            if (r.body && r.body.was_current) { window.location.assign('/login'); return; }
            ok('Session revoked.');
            loaders.sessions();
          });
        }));
        item.appendChild(actions);
        list.appendChild(item);
      });
    });
  };

  // -------------------------------------------------------------------------
  // API tokens
  // -------------------------------------------------------------------------

  function wireTokenForm() {
    var grid = $('token-perms');
    clear(grid);
    // The choices are the caller's own permissions, because a token can never
    // exceed them. Offering more would only produce a 403 at issuance.
    me.permissions.slice().sort().forEach(function (p) {
      var label = el('label');
      var box = document.createElement('input');
      box.type = 'checkbox';
      box.value = p;
      label.appendChild(box);
      label.appendChild(el('span', null, p));
      grid.appendChild(label);
    });

    submit($('token-form'), function () {
      var out = $('token-result');
      out.hidden = true;
      var chosen = [];
      var boxes = grid.querySelectorAll('input[type="checkbox"]');
      for (var i = 0; i < boxes.length; i++) {
        if (boxes[i].checked) { chosen.push(boxes[i].value); }
      }
      return api('POST', '/api/v1/me/tokens', {
        name: $('token-name').value,
        permissions: chosen,
        ttl_days: Number($('token-ttl').value)
      }).then(function (res) {
        if (res.status !== 201 || !res.body) { fail(res, 'could not issue that token'); return; }
        clear(out);
        out.appendChild(el('div', null, res.body.message));
        out.appendChild(el('code', 'secret', res.body.token));
        out.appendChild(button('Copy token', 'ghost', function (b) { copy(res.body.token, b); }));
        out.hidden = false;
        $('token-name').value = '';
        loaders.tokens();
      });
    });
  }

  loaders.tokens = function () {
    var list = $('tokens-list');
    empty(list, 'Loading…');
    api('GET', '/api/v1/me/tokens').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load tokens')); return; }
      if (!res.body.count) { empty(list, 'No tokens.'); return; }

      clear(list);
      res.body.tokens.forEach(function (t) {
        var item = el('div', 'item');
        item.appendChild(el('div', 'title', t.name));
        item.appendChild(el('div', 'meta',
          (t.permissions && t.permissions.length ? t.permissions.join(', ') : 'no permissions')));
        item.appendChild(el('div', 'meta',
          'issued ' + when(t.created_at) + ' · expires ' + when(t.expires_at) +
          ' · last used ' + when(t.last_used_at)));

        var actions = el('div', 'actions');
        actions.appendChild(button('Revoke', 'danger', function (b) {
          b.disabled = true;
          api('DELETE', '/api/v1/me/tokens/' + t.id).then(function (r) {
            b.disabled = false;
            if (r.status !== 200) { fail(r, 'could not revoke that token'); return; }
            ok('Token revoked.');
            loaders.tokens();
          });
        }));
        item.appendChild(actions);
        list.appendChild(item);
      });
    });
  };

  /* Creating an account on an administrator's authority (ADR-0039). The answer
   * is a one-time link that sets the password: the administrator never knows
   * it. */
  var createPicker = null;

  function fillCreateUserForm() {
    var sel = $('create-role');
    if (!sel || sel.options.length) { return; }
    assignableRoles.forEach(function (r) {
      var opt = el('option', null, r.name + ' (rank ' + r.rank + ')');
      opt.value = String(r.id);
      sel.appendChild(opt);
    });
    var holder = $('create-access');
    if (holder && !createPicker) {
      createPicker = accessPicker();
      holder.appendChild(createPicker.node);
    }
  }

  function wireCreateUserForm() {
    var form = $('create-user-form');
    if (!form) { return; }
    submit(form, function () {
      var out = $('create-user-result');
      out.hidden = true;
      var access = createPicker ? createPicker.value()
        : { all_libraries: true, library_ids: [], rating_ceiling: 0 };
      return api('POST', '/api/v1/admin/users', {
        username: $('create-username').value,
        email: $('create-email').value,
        role_id: Number($('create-role').value),
        all_libraries: access.all_libraries,
        library_ids: access.library_ids,
        rating_ceiling: access.rating_ceiling
      }).then(function (res) {
        if (res.status !== 201 || !res.body) { fail(res, 'could not create that account'); return; }
        var link = window.location.origin + res.body.link;
        clear(out);
        out.appendChild(el('div', null, res.body.message));
        out.appendChild(el('code', 'secret', link));
        out.appendChild(button('Copy link', 'ghost', function (b) { copy(link, b); }));
        out.hidden = false;
        $('create-username').value = '';
        $('create-email').value = '';
        loaders.users();
      });
    });
  }

  /* The roles and what they may do (ADR-0039). Admin is shown and not
   * editable; the three permissions reserved for it are shown and cannot be
   * ticked, with the reason beside them rather than their absence. */
  function loadRoleEditor() {
    var list = $('roles-list');
    if (!list) { return; }
    api('GET', '/api/v1/admin/roles').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not read the roles')); return; }
      clear(list);
      var reserved = res.body.reserved_for_admin || [];
      res.body.roles.forEach(function (role) {
        var r = row(role.name + ' (rank ' + role.rank + ')',
          role.holders + ' account(s) · ' + (role.defaults ? 'built-in permissions' : 'permissions chosen here'));
        if (!role.editable) {
          r.appendChild(el('div', 'why', 'Holds every permission, always.'));
          list.appendChild(r);
          return;
        }
        var grid = el('div', 'access-roots permissions');
        var boxes = [];
        res.body.permissions.forEach(function (p) {
          var lab = el('label', 'checkbox');
          var cb = el('input');
          cb.type = 'checkbox';
          cb.value = p;
          cb.checked = role.permissions.indexOf(p) >= 0;
          if (reserved.indexOf(p) >= 0) { cb.disabled = true; }
          if (p === 'auth.login') { cb.disabled = true; cb.checked = true; }
          boxes.push(cb);
          lab.appendChild(cb);
          lab.appendChild(document.createTextNode(' ' + p +
            (reserved.indexOf(p) >= 0 ? ' — Admin only' : (p === 'auth.login' ? ' — always' : ''))));
          grid.appendChild(lab);
        });
        r.appendChild(grid);
        var acts = actions(r);
        acts.appendChild(button('Save permissions', 'primary', function (b) {
          b.disabled = true;
          var perms = boxes.filter(function (x) { return x.checked; }).map(function (x) { return x.value; });
          api('PATCH', '/api/v1/admin/roles/' + role.id, { permissions: perms }).then(function (u) {
            b.disabled = false;
            if (u.status !== 200) { fail(u, 'could not change that role'); return; }
            ok(role.name + ' changed. ' + u.body.note);
            loadRoleEditor();
          });
        }));
        if (!role.defaults) {
          acts.appendChild(button('Put back the built-in permissions', 'ghost', function (b) {
            b.disabled = true;
            api('PATCH', '/api/v1/admin/roles/' + role.id, { defaults: true }).then(function (u) {
              b.disabled = false;
              if (u.status !== 200) { fail(u, 'could not put that role back'); return; }
              ok(role.name + ' is back on its built-in permissions.');
              loadRoleEditor();
            });
          }));
        }
        list.appendChild(r);
      });
    });
  }

  // -------------------------------------------------------------------------
  // health and logs (ADR-0040)
  // -------------------------------------------------------------------------

  loaders.health = function () {
    var list = $('health-list');
    empty(list, 'Checking…');
    api('GET', '/health/detail').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not read the health report')); return; }
      var b = res.body;
      $('health-summary').textContent = 'Overall: ' + b.state + ' · version ' + b.version +
        (b.schema_version ? ' · schema ' + b.schema_version : '') +
        ' · up ' + Math.round(b.uptime_seconds / 3600) + ' h';
      clear(list);
      b.checks.forEach(function (c) {
        var r = row(c.name, c.detail);
        var cls = c.state === 'ok' ? 'badge good' : 'badge warn';
        r.firstChild.appendChild(el('span', cls, c.state));
        list.appendChild(r);
      });
    });
    loadLogs();
  };

  function loadLogs() {
    var list = $('logs-list');
    empty(list, 'Loading…');
    var q = '/api/v1/admin/system/logs?level=' + encodeURIComponent($('logs-level').value) +
      '&limit=200' + ($('logs-text').value ? '&q=' + encodeURIComponent($('logs-text').value) : '');
    api('GET', q).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not read the logs')); return; }
      if (!res.body.count) { empty(list, 'Nothing matches.'); return; }
      clear(list);
      res.body.records.forEach(function (rec) {
        var attrs = Object.keys(rec.attrs).map(function (k) { return k + '=' + rec.attrs[k]; }).join(' ');
        var r = row(rec.message, when(rec.time) + ' · ' + rec.level);
        if (attrs) { r.appendChild(el('div', 'muted small', attrs)); }
        list.appendChild(r);
      });
    });
  }

  function wireLogs() {
    var form = $('logs-form');
    if (!form) { return; }
    submit(form, function () { loadLogs(); });
  }

  // -------------------------------------------------------------------------
  // security
  // -------------------------------------------------------------------------

  /* The calendar and feed addresses (ADR-0041): minted here, shown once. */
  loaders.security = function () {
    var status = $('feeds-status');
    var acts = $('feeds-actions');
    if (!status || !acts) { return; }
    api('GET', '/api/v1/me/feeds').then(function (res) {
      if (res.status !== 200 || !res.body) { status.textContent = failure(res, 'could not read your feeds'); return; }
      clear(acts);
      status.textContent = res.body.exists
        ? 'You have calendar and feed addresses, issued ' + when(res.body.created_at) +
          ', last read ' + when(res.body.last_used_at) + '.'
        : 'You have no calendar or feed address.';
      acts.appendChild(button(res.body.exists ? 'Replace the addresses' : 'Issue addresses', 'primary', function (b) {
        b.disabled = true;
        api('POST', '/api/v1/me/feeds', {}).then(function (r) {
          b.disabled = false;
          if (r.status !== 201 || !r.body) { fail(r, 'could not issue the addresses'); return; }
          var out = $('feeds-result');
          clear(out);
          out.appendChild(el('div', null, r.body.message));
          [['Calendar', r.body.calendar], ['Feed', r.body.rss]].forEach(function (pair) {
            out.appendChild(el('div', 'muted small', pair[0]));
            out.appendChild(el('code', 'secret', pair[1]));
            out.appendChild(button('Copy', 'ghost', function (c) { copy(pair[1], c); }));
          });
          out.hidden = false;
          loaders.security();
        });
      }));
      if (res.body.exists) {
        acts.appendChild(button('Revoke', 'danger', function (b) {
          b.disabled = true;
          api('DELETE', '/api/v1/me/feeds').then(function (r) {
            b.disabled = false;
            if (r.status !== 200) { fail(r, 'could not revoke the addresses'); return; }
            $('feeds-result').hidden = true;
            ok('Revoked. The old addresses answer nothing now.');
            loaders.security();
          });
        }));
      }
    });
  };

  function wireSecurity() {
    submit($('password-form'), function () {
      return api('POST', '/api/v1/me/password', {
        current_password: $('current-password').value,
        new_password: $('new-password').value
      }).then(function (res) {
        if (res.status !== 200) { fail(res, 'could not change the password'); return; }
        $('current-password').value = '';
        $('new-password').value = '';
        ok(res.body.message);
      });
    });

    submit($('email-form'), function () {
      return api('PATCH', '/api/v1/me', {
        email: $('new-email').value,
        current_password: $('email-password').value
      }).then(function (res) {
        $('email-password').value = '';
        if (res.status !== 200) { fail(res, 'could not change the email'); return; }
        $('new-email').value = '';
        ok('Your email is now ' + res.body.email + '.');
      });
    });

    submit($('recovery-form'), function () {
      var out = $('recovery-result');
      out.hidden = true;
      return api('POST', '/api/v1/me/mfa/recovery-codes', {
        current_password: $('recovery-password').value
      }).then(function (res) {
        if (res.status !== 200 || !res.body) { fail(res, 'could not replace the codes'); return; }
        $('recovery-password').value = '';
        clear(out);
        out.appendChild(el('div', null, res.body.message));
        var ul = el('ul', 'code-list');
        res.body.recovery_codes.forEach(function (c) { ul.appendChild(el('li', null, c)); });
        out.appendChild(ul);
        out.appendChild(button('Copy all', 'ghost', function (b) {
          copy(res.body.recovery_codes.join('\n'), b);
        }));
        out.hidden = false;
      });
    });
  }

  // -------------------------------------------------------------------------
  // tasks
  // -------------------------------------------------------------------------

  // migrate
  //
  // The dry run is the product here, not a preview of it. An operator looking
  // at 900 films needs to see what WOULD happen, and in particular needs the
  // two lists of misses — they are the worklist, and they are the reason to
  // read this screen rather than press the button and hope.

  var migrateState = { source: '', adopt: false, overwrite: false };

  loaders.migrate = function () {
    var where = $('migrate-where');
    var list = $('migrate-sources');
    clear(list);
    clear($('migrate-result'));

    api('GET', '/api/v1/admin/migrate/sources').then(function (res) {
      if (res.status !== 200 || !res.body) {
        where.textContent = '';
        return fail(res, 'could not read the migration directory');
      }
      where.textContent = 'Put an exported radarr.db in ' + res.body.directory +
        ' and it will appear here.';

      var files = res.body.files || [];
      if (!files.length) {
        return empty(list, 'No files in the migration directory yet.');
      }
      files.forEach(function (name) {
        var row = el('div', 'item');
        row.appendChild(el('div', 'title', name));
        row.appendChild(el('div', 'meta',
          'Read-only. Nothing is written to this file.'));
        var act = actions(row);
        act.appendChild(button('Dry run', 'primary', function (b) {
          migrateState.source = name;
          runMigration(b, false);
        }));
        list.appendChild(row);
      });
    });
  };

  function runMigration(btn, apply) {
    var out = $('migrate-result');
    var label = btn.textContent;
    btn.disabled = true;
    btn.textContent = apply ? 'Migrating…' : 'Reading…';

    api('POST', '/api/v1/admin/migrate/radarr', {
      source: migrateState.source,
      apply: apply,
      adopt_titles: migrateState.adopt,
      overwrite: migrateState.overwrite
    }).then(function (res) {
      btn.disabled = false;
      btn.textContent = label;
      clear(out);
      if (res.status !== 200 || !res.body) {
        return fail(res, 'the migration could not run');
      }
      if (apply) { ok('Migration applied.'); }
      renderMigration(out, res.body);
    });
  }

  function renderMigration(out, plan) {
    out.appendChild(el('h3', null, plan.applied ? 'What was done' : 'What would happen'));
    out.appendChild(el('p', 'hint', plan.summary));

    out.appendChild(facts([
      ['Radarr schema', plan.source_version || 'unknown'],
      ['Movies read', plan.movies_read],
      ['Items here', plan.items_in_library],
      ['To attach', plan.counts.attach],
      ['Already correct', plan.counts.already_correct],
      ['Conflicts', plan.counts.conflict]
    ]));

    /* Options, and then the button that acts on them — in that order, because
     * the reverse invites pressing Apply and then noticing the checkbox. */
    if (!plan.applied) {
      var opts = el('div', 'list compact');

      opts.appendChild(toggle('Also take Radarr\u2019s titles and years',
        'Radarr\u2019s titles are TMDB\u2019s, which is what identifying ' +
        'these films here would have set anyway. Off by default because ' +
        'renaming a whole library from a file is the destructive half.',
        migrateState.adopt, function (on) { migrateState.adopt = on; }));

      if (plan.counts.conflict > 0) {
        opts.appendChild(toggle('Replace identifiers this library already has',
          plan.counts.conflict + ' item(s) already carry a DIFFERENT id, ' +
          'which somebody or an identification pass decided. Leave this off ' +
          'unless you know Radarr is the better authority.',
          migrateState.overwrite, function (on) { migrateState.overwrite = on; }));
      }
      out.appendChild(opts);

      var go = el('div', 'item');
      go.appendChild(el('div', 'title', 'Apply this to the library'));
      go.appendChild(el('div', 'meta',
        'Writes the identifiers above. Running it again afterwards changes ' +
        'nothing, so it is safe to repeat.'));
      actions(go).appendChild(button('Migrate', 'primary', function (b) {
        runMigration(b, true);
      }));
      out.appendChild(go);
    }

    migrationList(out, 'Matched', (plan.matches || []).map(function (m) {
      var meta = m.action + ' \u00b7 TMDB ' + m.tmdb_id;
      if (m.action === 'conflict') { meta += ' \u00b7 this library has ' + m.current_tmdb_id; }
      if (m.case_insensitive) { meta += ' \u00b7 matched ignoring capitalisation'; }
      if (m.current_title) { meta += ' \u00b7 here called \u201c' + m.current_title + '\u201d'; }
      /* Radarr's label only when it says something the folder does not. In a
       * Radarr-managed library the folder IS "Title (Year)", so appending it
       * produces "Arrival (2016) — Arrival (2016)" on almost every row. */
      var label = m.title + (m.year ? ' (' + m.year + ')' : '');
      return [m.folder === label ? m.folder : m.folder + ' \u2014 ' + label, meta];
    }));

    migrationList(out, 'Folders Radarr does not know',
      (plan.unknown || []).map(function (f) {
        return [f, 'Still needs identifying the ordinary way, on the Identify screen.'];
      }));

    migrationList(out, 'Radarr films with no folder here',
      (plan.unmatched || []).map(function (u) {
        return [u.folder + ' \u2014 ' + u.title, 'TMDB ' + u.tmdb_id +
          ' \u00b7 nothing here is in a folder of that name'];
      }));

    migrationList(out, 'Ambiguous folder names',
      (plan.ambiguous || []).map(function (f) {
        return [f, 'Held more than once, so it cannot be matched to one film ' +
          'without guessing. Skipped.'];
      }));
  }

  function migrationList(out, heading, rows) {
    if (!rows.length) { return; }
    out.appendChild(el('h3', null, heading + ' (' + rows.length + ')'));
    var list = el('div', 'list compact');
    rows.forEach(function (r) {
      var item = el('div', 'item');
      item.appendChild(el('div', 'title', r[0]));
      item.appendChild(el('div', 'meta', r[1]));
      list.appendChild(item);
    });
    out.appendChild(list);
  }

  /* A labelled checkbox. <label> wrapping the input so the whole row is a hit
   * target, which is how a checkbox should behave and is not the default. */
  function toggle(label, explain, checked, onChange) {
    var row = el('div', 'item');
    var lab = el('label', 'title');
    var box = document.createElement('input');
    box.type = 'checkbox';
    box.checked = !!checked;
    box.addEventListener('change', function () { onChange(box.checked); });
    lab.appendChild(box);
    lab.appendChild(document.createTextNode(' ' + label));
    row.appendChild(lab);
    row.appendChild(el('div', 'meta', explain));
    return row;
  }

  // -------------------------------------------------------------------------
  // the audit log (ADR-0031)
  // -------------------------------------------------------------------------

  /* The filter a page was asked with, and where the next page starts. A new
   * filter starts again from the newest line; "Older" continues the same one. */
  var auditQuery = { params: '', next: 0 };

  function auditParams() {
    var p = [];
    [['category', 'audit-category'], ['outcome', 'audit-outcome'],
      ['actor', 'audit-actor'], ['q', 'audit-text']].forEach(function (pair) {
      var v = ($(pair[1]).value || '').trim();
      if (v) { p.push(pair[0] + '=' + encodeURIComponent(v)); }
    });
    return p.join('&');
  }

  /* One line of the log. Every value goes in as text: user agents, usernames
   * at signup and release names were written by strangers. */
  function auditRow(e) {
    var r = el('div', 'item audit-line ' + (e.outcome || ''));
    var head = el('div', 'title');
    head.appendChild(el('span', 'badge ' + (e.outcome === 'success' ? 'good' : e.outcome === 'denied' ? 'bad' : 'warn'),
      e.outcome));
    head.appendChild(el('span', 'audit-action', e.action));
    r.appendChild(head);
    var who = e.actor + (e.actor_user_id ? ' (#' + e.actor_user_id + ')' : '');
    r.appendChild(el('div', 'meta', when(e.occurred_at) + ' · ' + who +
      (e.source_ip ? ' · from ' + e.source_ip : '')));
    if (e.target_kind || e.target_id) {
      r.appendChild(el('div', 'meta', 'on ' + [e.target_kind, e.target_id].filter(Boolean).join(' ')));
    }
    if (e.detail) { r.appendChild(el('div', 'audit-detail', e.detail)); }
    if (e.user_agent || e.before !== undefined || e.after !== undefined) {
      var more = el('details', 'audit-more');
      more.appendChild(el('summary', null, 'More'));
      var f = [];
      if (e.user_agent) { f.push(['User agent', e.user_agent]); }
      if (e.before !== undefined) { f.push(['Before', JSON.stringify(e.before)]); }
      if (e.after !== undefined) { f.push(['After', JSON.stringify(e.after)]); }
      f.push(['Line', String(e.id)]);
      more.appendChild(facts(f));
      r.appendChild(more);
    }
    return r;
  }

  function loadAuditPage(append) {
    var list = $('audit-list');
    var older = $('audit-older');
    if (!append) {
      auditQuery = { params: auditParams(), next: 0 };
      empty(list, 'Loading…');
    }
    clear(older);
    /* A page that arrives after a new filter was asked for belongs to the old
     * one, and is dropped rather than added to the wrong list. */
    var asked = auditQuery;
    var q = asked.params;
    if (append && asked.next) { q += (q ? '&' : '') + 'before=' + asked.next; }
    return api('GET', '/api/v1/admin/audit' + (q ? '?' + q : '')).then(function (res) {
      if (asked !== auditQuery) { return; }
      if (res.status !== 200 || !res.body) {
        var why = failure(res, 'could not read the audit log');
        if (!append) { empty(list, why); return; }
        /* The lines already shown stay; the failure is said where the button
         * was, with the button back to try again. */
        older.appendChild(el('span', 'meta', why + ' '));
        older.appendChild(button('Try again', 'ghost', function (b) { b.disabled = true; loadAuditPage(true); }));
        return;
      }
      if (!append) { clear(list); }
      var events = res.body.events || [];
      if (!events.length && !append) {
        empty(list, auditQuery.params ? 'Nothing matches that.' : 'Nothing has been recorded yet.');
        return;
      }
      events.forEach(function (e) { list.appendChild(auditRow(e)); });
      auditQuery.next = res.body.next_before || 0;
      if (auditQuery.next) {
        older.appendChild(button('Older', 'ghost', function (b) {
          b.disabled = true;
          loadAuditPage(true);
        }));
      }
    });
  }

  loaders.audit = function () {
    var summary = $('audit-summary');
    clear(summary);
    api('GET', '/api/v1/admin/audit/summary').then(function (res) {
      if (res.status !== 200 || !res.body) { return; }
      var b = res.body;
      if (!b.total) { summary.textContent = 'Nothing was recorded in the last ' + b.days + ' days.'; return; }
      var denied = 0;
      (b.categories || []).forEach(function (c) { denied += (c.outcomes && c.outcomes.denied) || 0; });
      summary.textContent = 'In the last ' + b.days + ' days: ' + b.total + ' line(s) — ' +
        (b.categories || []).map(function (c) { return c.category + ' ' + c.count; }).join(', ') +
        '. ' + denied + ' denied. Denials over ' + b.ceiling.anonymous_per_address_per_hour +
        ' an hour from one address, ' + b.ceiling.anonymous_per_hour + ' from every address together or ' +
        b.ceiling.per_account_per_hour + ' from one account are counted rather than written one by one: ' +
        'look for authz.denied.suppressed.';
    });
    return loadAuditPage(false);
  };

  function wireAudit() {
    submit($('audit-filter'), function () { return loadAuditPage(false); });
  }

  loaders.tasks = function () {
    var list = $('tasks-list');
    empty(list, 'Loading…');
    api('GET', '/api/v1/admin/system/tasks').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load tasks')); return; }
      if (!res.body.count) { empty(list, 'No tasks registered.'); return; }

      clear(list);
      res.body.tasks.forEach(function (t) {
        var item = el('div', 'item');
        var title = el('div', 'title', t.name);
        if (t.running) { title.appendChild(el('span', 'badge warn', 'running')); }
        if (t.failures) { title.appendChild(el('span', 'badge bad', t.failures + ' failed')); }
        item.appendChild(title);
        if (t.description) { item.appendChild(el('div', 'meta', t.description)); }
        item.appendChild(el('div', 'meta',
          every(t.interval_seconds) +
          ' · ' + t.runs + ' runs · last ' + when(t.last_run) +
          ' · next ' + (t.next_run ? when(t.next_run) : 'not scheduled')));

        var last = t.history && t.history.length ? t.history[t.history.length - 1] : null;
        if (last) {
          item.appendChild(el('div', 'meta',
            'last outcome: ' + (last.ok ? (last.summary || 'ok') : (last.error || 'failed')) +
            ' (' + last.duration_ms + ' ms)'));
        }

        var actions = el('div', 'actions');
        actions.appendChild(button('Run now', 'ghost', function (b) {
          b.disabled = true;
          api('POST', '/api/v1/admin/system/tasks/' + encodeURIComponent(t.name) + '/run')
            .then(function (r) {
              b.disabled = false;
              if (r.status === 409) { fail(r, 'that task is already running'); return; }
              if (r.status !== 200 || !r.body) { fail(r, 'could not run that task'); return; }
              if (r.body.ok) { ok(t.name + ': ' + (r.body.summary || 'done')); }
              else { fail({ status: 200, body: { error: t.name + ': ' + r.body.error } }, 'failed'); }
              loaders.tasks();
            });
        }));
        item.appendChild(actions);
        list.appendChild(item);
      });
    });
  };

  /* Backups (ADR-0029). The page takes one and lists them. It offers no
   * download and no restore, and says where those happen instead: a download
   * button would be a way for a stolen session to carry off the database, and
   * a restore button a way for it to roll the instance back. */
  loaders.backups = function () {
    var list = $('backup-list');
    var policy = $('backup-policy');
    var actions = $('backup-actions');
    empty(list, 'Loading…');
    clear(policy);
    clear(actions);
    api('GET', '/api/v1/admin/system/backups').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(list, failure(res, 'could not load backups')); return; }
      var b = res.body;
      var days = function (s) { return s % 86400 === 0 ? (s / 86400) + (s === 86400 ? ' day' : ' days') : every(s).replace('every ', ''); };
      var text = 'Kept in ' + b.dir + '. ';
      if (b.interval_seconds) {
        text += 'A backup is taken when the newest is older than ' + days(b.interval_seconds) +
          ' (checked hourly and at startup)' + (b.next_due ? '; the next is due ' + when(b.next_due) : '') + '. ';
      } else {
        text += 'Scheduled backups are off (backup.interval is 0s). ';
      }
      text += 'Backups older than ' + days(b.keep_seconds) + ' are deleted, except the newest ' + b.keep_min + '.';
      policy.appendChild(document.createTextNode(text));
      if (b.same_filesystem_as_database) {
        policy.appendChild(el('div', 'why',
          'These backups are on the same filesystem as the database: they survive a bad upgrade ' +
          'or a mistake, not the loss of that disk. Copy them off this host, or point backup.dir ' +
          'at another disk.'));
      }

      actions.appendChild(button('Back up now', '', function (btn) {
        btn.disabled = true;
        api('POST', '/api/v1/admin/system/backup').then(function (r) {
          btn.disabled = false;
          if (r.status === 409) { fail(r, 'a backup is already being taken'); return; }
          if (r.status !== 201 || !r.body) { fail(r, 'the backup failed'); return; }
          ok(r.body.message);
          loaders.backups();
        });
      }));

      if (!b.count) {
        empty(list, 'No backups yet. The first is taken within the hour, or now with the button above.');
        return;
      }
      clear(list);
      b.backups.forEach(function (bk, i) {
        var item = el('div', 'item');
        var title = el('div', 'title', bk.name);
        if (i === 0) { title.appendChild(el('span', 'badge', 'newest')); }
        item.appendChild(title);
        item.appendChild(el('div', 'meta', 'taken ' + when(bk.taken_at) + ' · ' + bytes(bk.bytes)));
        list.appendChild(item);
      });
    });
  };

  function wireRequestForm() {
    var form = $('request-form');
    if (!form) { return; }
    submit(form, function () {
      var year = parseInt($('request-year').value, 10);
      return api('POST', '/api/v1/requests', {
        kind: $('request-kind').value,
        title: $('request-title').value,
        year: isNaN(year) ? 0 : year,
        note: $('request-note').value
      }).then(function (res) {
        if (res.status !== 201 && res.status !== 200) {
          fail(res, 'could not record that request');
          return;
        }
        ok((res.body && res.body.message) || 'Requested.');
        $('request-title').value = '';
        $('request-year').value = '';
        $('request-note').value = '';
        loaders.requests();
      });
    });
  }

  // -------------------------------------------------------------------------
  // boot
  // -------------------------------------------------------------------------


  // -------------------------------------------------------------------------
  // formatting shared by the media views
  // -------------------------------------------------------------------------

  /* bytes(n) -> "12.4 GiB". Raw byte counts are unreadable at the sizes this
   * software deals in, and every one of these is glanced at rather than read. */
  function bytes(n) {
    if (!n || n < 0) { return '—'; }
    var units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    var i = 0;
    var v = n;
    while (v >= 1024 && i < units.length - 1) { v = v / 1024; i++; }
    return (i === 0 ? v : v.toFixed(v < 10 ? 1 : 0)) + ' ' + units[i];
  }

  /* clock(4350) -> "1:12:30". A playback position is read against a scrubber,
   * so it wants the shape a scrubber uses — not "1 hour", which is where a
   * viewer is to the nearest hour and therefore nowhere. */
  function clock(seconds) {
    if (!isFinite(seconds) || seconds < 0) { return '0:00'; }
    var s = Math.floor(seconds % 60);
    var m = Math.floor(seconds / 60) % 60;
    var h = Math.floor(seconds / 3600);
    var mm = (h > 0 && m < 10 ? '0' : '') + m;
    return (h > 0 ? h + ':' : '') + mm + ':' + (s < 10 ? '0' : '') + s;
  }

  /* duration(604800) -> "7 days". A Go duration string reads "168h0m0s", which
   * makes an operator do arithmetic to learn it means a week. */
  function duration(seconds) {
    if (!seconds || seconds < 0) { return ''; }
    var units = [['day', 86400], ['hour', 3600], ['minute', 60]];
    for (var i = 0; i < units.length; i++) {
      var n = Math.round(seconds / units[i][1]);
      if (seconds >= units[i][1]) {
        return n + ' ' + units[i][0] + (n === 1 ? '' : 's');
      }
    }
    return seconds + ' seconds';
  }

  /* A progress bar with no inline style. CSP here forbids inline style and this
   * file bans element.style.x outright, so a div whose width is set from
   * JavaScript is not available. <progress> is the native answer and it is the
   * accessible one: a screen reader announces it, a styled div does not. */
  function progress(percent) {
    var p = document.createElement('progress');
    p.max = 100;
    p.value = Math.max(0, Math.min(100, percent || 0));
    p.textContent = Math.round(percent || 0) + '%';
    return p;
  }

  /* A labelled row of facts under a title. Every value goes through el(), so a
   * release name — which a stranger wrote — cannot become markup. */
  function facts(pairs) {
    var wrap = el('div', 'facts');
    pairs.forEach(function (pair) {
      if (pair[1] === undefined || pair[1] === null || pair[1] === '') { return; }
      var f = el('span', 'fact');
      f.appendChild(el('span', 'fact-label', pair[0]));
      f.appendChild(el('span', 'fact-value', pair[1]));
      wrap.appendChild(f);
    });
    return wrap;
  }

  /* The rest of this file renders lists as .item / .title / .meta / .actions,
   * and these use the same vocabulary rather than a parallel one — a second set
   * of class names would mean every future style change had to be made twice
   * and would drift the first time somebody forgot. */
  function row(title, subtitle) {
    var r = el('div', 'item');
    r.appendChild(el('div', 'title', title));
    if (subtitle) { r.appendChild(el('div', 'meta', subtitle)); }
    return r;
  }

  function actions(r) {
    var a = el('div', 'actions');
    r.appendChild(a);
    return a;
  }

  // -------------------------------------------------------------------------
  // library
  // -------------------------------------------------------------------------

  var libraryItems = [];

  function renderLibrary() {
    var list = $('library-list');
    var filter = ($('library-filter').value || '').toLowerCase();
    var shown = libraryItems.filter(function (it) {
      return !filter || (it.title || '').toLowerCase().indexOf(filter) !== -1;
    });

    $('library-count').textContent = shown.length === libraryItems.length
      ? shown.length + ' item(s)'
      : shown.length + ' of ' + libraryItems.length + ' item(s)';

    if (!shown.length) {
      empty(list, libraryItems.length
        ? 'Nothing matches that filter.'
        : 'The library is empty. Add a series from Add, scan a root folder that ' +
          'already holds media, or grab something from Search.');
      return;
    }

    clear(list);
    shown.forEach(function (it) {
      var r = row(it.title + (it.year ? ' (' + it.year + ')' : ''), kindWord(it.kind));
      var a = actions(r);
      a.appendChild(button('Files', 'ghost', function () { showItem(it.id); }));
      if (can('library.delete')) {
        a.appendChild(button('Delete', 'danger', function (btn) {
          btn.disabled = true;
          api('DELETE', '/api/v1/admin/media/' + it.id).then(function (res) {
            if (res.status !== 200) { btn.disabled = false; return fail(res, 'could not delete'); }
            ok((res.body && res.body.note) || 'Moved to trash.');
            loaders.library();
          });
        }));
      }
      list.appendChild(r);
    });
  }

  /* What this title is fetched as (ADR-0035): its own quality profile, or the
   * instance's default. Searches for it and automatic acquisition both judge
   * it by this, so it is said on its page whoever is looking, and changed here
   * by whoever may edit the library. */
  function titleProfileRow(it) {
    var r = row('Quality', 'Reading the profiles…');
    var meta = r.querySelector('.meta');
    api('GET', '/api/v1/quality-profiles').then(function (res) {
      if (res.status !== 200 || !res.body) {
        /* The list is the searchers'; without it, say only what the title
         * itself carries. */
        if (meta) {
          meta.textContent = it.quality_profile_id
            ? 'Judged by its own quality profile.' : 'Judged by the default quality profile.';
        }
        return;
      }
      var profiles = res.body.profiles || [];
      var dflt = profiles.filter(function (p) { return p['default']; })[0];
      var mine = profiles.filter(function (p) { return p.id === it.quality_profile_id; })[0];
      var said = mine ? mine.name + ' — this title’s own'
        : 'the default' + (dflt ? ' (' + dflt.name + ')' : '');
      if (meta) { meta.textContent = 'Judged by ' + said + '.'; }
      if (!can('library.edit') || !profiles.length) { return; }
      var sel = el('select');
      var first = el('option', null, 'The default' + (dflt ? ' (' + dflt.name + ')' : ''));
      first.value = '';
      sel.appendChild(first);
      profiles.forEach(function (p) {
        var o = el('option', null, p.name);
        o.value = String(p.id);
        if (p.id === it.quality_profile_id) { o.selected = true; }
        sel.appendChild(o);
      });
      sel.addEventListener('change', function () {
        sel.disabled = true;
        var chosen = sel.value ? Number(sel.value) : null;
        api('PUT', '/api/v1/media/' + it.id + '/quality-profile', { profile_id: chosen })
          .then(function (u) {
            sel.disabled = false;
            if (u.status !== 200) { return fail(u, 'could not change the profile'); }
            it.quality_profile_id = chosen || undefined;
            if (meta) { meta.textContent = (u.body && u.body.note) || 'Changed.'; }
          });
      });
      actions(r).appendChild(sel);
    });
    return r;
  }

  /* Its rating (ADR-0037): who may see it. Every account with a ceiling is
   * refused an unrated title, so the page says when a title is unrated, and
   * whoever may edit the library can rate it by hand or give it back to the
   * provider. */
  var certifications = ['G', 'PG', 'PG-13', 'R', 'NC-17', 'TV-Y', 'TV-Y7', 'TV-G', 'TV-PG', 'TV-14', 'TV-MA'];

  function titleRatingRow(it) {
    var rating = it.rating || { rated: false };
    function said(rt) {
      if (!rt.rated) { return 'Unrated — hidden from every account with a rating ceiling.'; }
      return 'Rated ' + rt.certification + (rt.source === 'person' ? ', by hand.' : ', by the metadata provider.');
    }
    var r = row('Rating', said(rating));
    var meta = r.querySelector('.meta');
    if (!can('library.edit')) { return r; }
    var sel = el('select');
    var first = el('option', null, rating.source === 'person' ? 'Give back to the provider' : 'The provider’s rating');
    first.value = '';
    sel.appendChild(first);
    certifications.forEach(function (c) {
      var o = el('option', null, c);
      o.value = c;
      if (rating.source === 'person' && rating.certification === c) { o.selected = true; }
      sel.appendChild(o);
    });
    sel.addEventListener('change', function () {
      sel.disabled = true;
      api('PUT', '/api/v1/media/' + it.id + '/rating', { certification: sel.value || null })
        .then(function (u) {
          sel.disabled = false;
          if (u.status !== 200) { return fail(u, 'could not change the rating'); }
          if (meta) { meta.textContent = said(u.body.rating) + ' ' + ((u.body && u.body.note) || ''); }
        });
    });
    actions(r).appendChild(sel);
    return r;
  }

  function showItem(id) {
    var box = $('library-detail');
    return api('GET', '/api/v1/media/' + id).then(function (res) {
      if (res.status !== 200 || !res.body) { return fail(res, 'could not read that item'); }
      var it = res.body;

      clear(box);
      box.hidden = false;
      box.appendChild(el('h3', null, it.title + (it.year ? ' (' + it.year + ')' : '')));
      if (it.author) { box.appendChild(el('div', 'muted', 'by ' + it.author)); }
      /* A quality profile and a certification are a film's and a series'
       * (ADR-0044): music and books have neither. */
      var video = it.kind === 'movie' || it.kind === 'series';
      if (video) {
        box.appendChild(titleProfileRow(it));
        box.appendChild(titleRatingRow(it));
      }
      if (can('request.submit')) { box.appendChild(reportProblemRow(it)); }

      /* A series shows what it is MISSING before what it has. The episode list
       * comes from the provider, never from the files — a list built from
       * files would be complete by construction and never show a gap
       * (ADR-0022). Rendered into its own container so the files below do not
       * wait on it. */
      if (it.kind === 'series') {
        var eps = el('div', null);
        box.appendChild(eps);
        showEpisodes(eps, it.id);
      }
      if (it.kind === 'artist') {
        var albums = el('div', 'list');
        box.appendChild(albums);
        showAlbums(albums, it.id);
      }

      var files = it.files || [];
      if (!files.length) {
        /* A series added from the provider has nothing on disk until its first
         * episode is imported (ADR-0025), and that is not a fault. */
        if (it.kind === 'series') {
          box.appendChild(el('p', 'empty', 'Nothing of this series is on disk yet.'));
          return;
        }
        /* An artist's albums above say what is held (ADR-0044). */
        if (it.kind === 'artist') { return; }
        /* Nor has a film (ADR-0026): it is wanted, and is searched for from
         * here. Whatever is grabbed from this search is filed under this film,
         * whatever the release calls itself. */
        var name = it.title + (it.year ? ' (' + it.year + ')' : '');
        var unwanted = it.monitored === false;
        var wanted = row('Not on disk yet', unwanted
          ? 'Not monitored: it is kept in the library and is not on the Wanted list, so ' +
            'nothing searches for it or downloads it.'
          : 'It is on the Wanted list until it is.');
        var a = actions(wanted);
        if (can('acquisition.search') && it.kind === 'movie') {
          a.appendChild(button('Search', null, function () {
            searchForFilm(it.id, name, wanted);
          }));
        }
        /* A book's search is the title's own route, ranked by format (ADR-0049). */
        if (can('acquisition.search') && it.kind === 'book') {
          a.appendChild(button('Search', null, function () {
            searchForBook(it.id, name + (it.author ? ' by ' + it.author : ''), wanted);
          }));
        }
        /* Monitoring decides whether it is wanted and so — with automatic
         * acquisition on — whether it is fetched (ADR-0030). */
        if (can('library.edit')) {
          a.appendChild(button(unwanted ? 'Monitor' : 'Unmonitor', 'ghost', function (btn) {
            btn.disabled = true;
            api('PUT', '/api/v1/media/' + it.id + '/monitored', { monitored: unwanted })
              .then(function (u) {
                if (u.status !== 200) { btn.disabled = false; return fail(u, 'could not change monitoring'); }
                ok((u.body && u.body.note) || 'Changed.');
                showItem(it.id);
              });
          }));
        }
        box.appendChild(wanted);
        return;
      }
      if (it.kind === 'series') { box.appendChild(el('h3', null, 'Files')); }
      files.forEach(function (f) {
        var label = f.path;
        if (f.season !== undefined && f.episode !== undefined) {
          label = 'S' + pad(f.season) + 'E' + pad(f.episode) +
            (f.episode_last ? '-E' + pad(f.episode_last) : '') + ' — ' + f.path;
        }
        var r = row(label, null);
        r.appendChild(facts([
          ['Quality', f.quality],
          ['Size', bytes(f.bytes)],
          ['Group', f.group],
          /* Said plainly: the same bytes may be being served to strangers, and
           * deleting here does not stop that. */
          ['Shared with a download', f.hardlinked ? 'yes — still seeding' : 'no'],
          ['Imported', when(f.imported_at)]
        ]));
        if (f.release) { r.appendChild(el('div', 'muted small', f.release)); }
        var acts = actions(r);
        /* A book is read elsewhere: its file is downloaded, not played (ADR-0049). */
        if (it.kind !== 'book') {
          acts.appendChild(button('Play', 'primary', function () {
            watch(f.id, it.title + (it.year ? ' (' + it.year + ')' : ''));
          }));
        }
        /* A subtitle in each wanted language, from OpenSubtitles (ADR-0055). */
        if (can('library.edit') && (it.kind === 'movie' || it.kind === 'series')) {
          subtitleButtons(acts, f.id);
        }
        /* One file to the trash, the title kept (ADR-0053). Recoverable from
         * the trash, so it acts at once, as a title's delete does. */
        if (can('library.delete')) {
          acts.appendChild(button('Delete file', 'danger', function (btn) {
            btn.disabled = true;
            api('DELETE', '/api/v1/admin/files/' + f.id).then(function (res) {
              if (res.status !== 200) { btn.disabled = false; return fail(res, 'could not delete the file'); }
              ok((res.body && (res.body.warning || res.body.note)) || 'Moved to trash.');
              showItem(it.id);
            });
          }));
        }
        /* The file as stored, saved rather than played (ADR-0038). The
         * response is an attachment, so the page stays where it is. */
        if (can('media.download_original')) {
          acts.appendChild(button('Download', 'ghost', function () {
            window.location.href = '/api/v1/media/' + it.id + '/original?file=' + f.id;
          }));
        }
        box.appendChild(r);
      });
    });
  }

  function pad(n) { return (n < 10 ? '0' : '') + n; }

  /* The seasons of one series, each episode marked held or missing.
   *
   * "Missing" only means something because the rows came from the provider.
   * An episode with no air date is ANNOUNCED — shown as such and never as
   * missing, because it is not overdue; it has not been scheduled. */
  function showEpisodes(box, itemID) {
    clear(box);
    api('GET', '/api/v1/media/' + itemID + '/children').then(function (res) {
      if (res.status !== 200 || !res.body) {
        return fail(res, 'could not read this series\u2019 episodes');
      }
      var body = res.body;
      var head = el('div', 'item');
      head.appendChild(el('div', 'title', 'Episodes'));
      head.appendChild(el('div', 'meta', body.known
        ? body.have + ' of ' + body.known + ' on disk'
        : (body.note || 'Nothing is known about this series\u2019 episodes yet.')));
      if (can('admin.system')) {
        actions(head).appendChild(button('Refresh from the provider', null, function (b) {
          b.disabled = true;
          b.textContent = 'Asking\u2026';
          api('POST', '/api/v1/admin/media/' + itemID + '/refresh-episodes', {})
            .then(function (r) {
              b.disabled = false;
              b.textContent = 'Refresh from the provider';
              if (r.status !== 200 || !r.body) {
                fail(r, 'the refresh failed');
                /* A rate limit met part-way still recorded the seasons read
                 * before it, so what is on screen is now out of date. */
                if (r.status === 503) { showEpisodes(box, itemID); }
                return;
              }
              /* Seasons it could not read are not a clean success, and the
               * green notice would say they were. */
              if (r.body.seasons_failed > 0) {
                flash($('error'), r.body.summary);
              } else {
                ok(r.body.summary);
              }
              showEpisodes(box, itemID);
            });
        }));
      }
      /* Whether a season first listed after now starts monitored (ADR-0061). */
      if (body.follow_new_seasons !== undefined) {
        head.appendChild(el('div', 'meta', body.follow_new_seasons
          ? 'New seasons are followed' : 'New seasons are not followed'));
        if (can('library.edit')) {
          actions(head).appendChild(button(body.follow_new_seasons ? 'Stop following new seasons'
            : 'Follow new seasons', null, function (b) {
            b.disabled = true;
            api('PUT', '/api/v1/media/' + itemID + '/new-seasons', { follow: !body.follow_new_seasons })
              .then(function (r) {
                if (r.status !== 200) { b.disabled = false; return fail(r, 'could not change that'); }
                ok(r.body.note);
                showEpisodes(box, itemID);
              });
          }));
        }
      }
      /* A series released by date is searched for by date (ADR-0064). */
      if (body.daily !== undefined) {
        head.appendChild(el('div', 'meta', body.daily
          ? 'Released daily: searched for by air date' : 'Searched for by season and episode'));
        if (can('library.edit')) {
          actions(head).appendChild(button(body.daily ? 'Search by episode number'
            : 'Released daily: search by date', null, function (b) {
            b.disabled = true;
            api('PUT', '/api/v1/media/' + itemID + '/daily', { daily: !body.daily })
              .then(function (r) {
                if (r.status !== 200) { b.disabled = false; return fail(r, 'could not change that'); }
                ok(r.body.note);
                showEpisodes(box, itemID);
              });
          }));
        }
      }
      /* Where episodes imported from now on are filed (ADR-0063). */
      if (body.season_folders !== undefined) {
        head.appendChild(el('div', 'meta', body.season_folders
          ? 'Filed in season folders' : 'Filed in the series’ own folder'));
        if (can('library.edit')) {
          actions(head).appendChild(button(body.season_folders ? 'File without season folders'
            : 'File in season folders', null, function (b) {
            b.disabled = true;
            api('PUT', '/api/v1/media/' + itemID + '/season-folders', { season_folders: !body.season_folders })
              .then(function (r) {
                if (r.status !== 200) { b.disabled = false; return fail(r, 'could not change that'); }
                ok(r.body.note);
                showEpisodes(box, itemID);
              });
          }));
        }
      }
      box.appendChild(head);

      (body.seasons || []).forEach(function (s) {
        box.appendChild(seasonBlock(s, itemID, function () { showEpisodes(box, itemID); }));
      });
    });
  }

  function seasonBlock(s, itemID, reload) {
    var wrap = el('div', 'list compact');
    var head = el('div', 'item');
    var name = s.number === 0 ? 'Specials' : (s.name || 'Season ' + s.number);
    head.appendChild(el('div', 'title', name));

    /* Both numbers when they differ. "have 3 of 9" measured against the rows
     * this instance holds is a different claim from "of the 9 the provider
     * says exist", and a season whose refresh failed part-way is exactly where
     * the two part company. */
    var summary;
    if (!s.known && !s.episode_count) {
      /* "0 of 0 on disk" is true and says nothing. A season the provider lists
       * with no episodes is ANNOUNCED — and it is the season about to change. */
      summary = 'announced \u2014 the provider lists no episodes yet';
    } else {
      summary = s.have + ' of ' + s.known + ' on disk';
      if (s.episode_count && s.episode_count !== s.known) {
        summary += ' \u00b7 the provider lists ' + s.episode_count + '; refresh to read the rest';
      }
    }
    if (!s.monitored) { summary += ' \u00b7 not monitored'; }
    head.appendChild(el('div', 'meta', summary));

    if (can('library.edit')) {
      actions(head).appendChild(button(s.monitored ? 'Stop monitoring' : 'Monitor', null,
        function (b) {
          b.disabled = true;
          api('PUT', '/api/v1/media/' + itemID + '/seasons/' + s.number + '/monitored',
            { monitored: !s.monitored }).then(function (r) {
            if (r.status !== 200) { b.disabled = false; return fail(r, 'could not change monitoring'); }
            reload();
          });
        }));
    }
    /* A season with anything missing can be searched for whole: a pack of it,
     * or its episodes one by one, from one search (ADR-0033). */
    if (can('acquisition.search') && s.known && s.have < s.known) {
      actions(head).appendChild(button('Search season', null, function () {
        searchForSeason(itemID, s.number, name, head);
      }));
    }
    wrap.appendChild(head);

    /* Collapsed unless something in it needs attention: a monitored episode
     * that has aired and is not on disk. Every episode as a full card is fine
     * for a nine-episode season and unusable for a show with four hundred, and
     * the seasons an operator opens this page to look at are exactly the ones
     * with gaps. */
    var eps = s.episodes || [];
    var needsAttention = eps.some(function (e) {
      return e.monitored && !e.have && !e.announced &&
        e.aired_at && new Date(e.aired_at) <= new Date();
    });
    var body = el('div', null);
    body.hidden = !needsAttention;
    if (eps.length) {
      actions(head).appendChild(button(body.hidden ? 'Show episodes' : 'Hide episodes', null,
        function (b) {
          body.hidden = !body.hidden;
          b.textContent = body.hidden ? 'Show episodes' : 'Hide episodes';
        }));
    }
    wrap.appendChild(body);

    eps.forEach(function (e) {
      var r = el('div', 'item');
      r.appendChild(el('div', 'title',
        'S' + pad(e.season) + 'E' + pad(e.number) + (e.title ? ' \u2014 ' + e.title : '')));
      var state;
      if (e.have) { state = 'on disk'; }
      else if (e.announced) { state = 'announced \u2014 no air date yet'; }
      else if (e.aired_at && new Date(e.aired_at) > new Date()) {
        state = 'airs ' + new Date(e.aired_at).toLocaleDateString();
      } else { state = e.monitored ? 'MISSING' : 'missing \u2014 not monitored'; }
      var meta = state;
      if (e.aired_at) { meta += ' \u00b7 ' + new Date(e.aired_at).toLocaleDateString(); }
      r.appendChild(el('div', 'meta', meta));
      var aired = !e.announced && e.aired_at && new Date(e.aired_at) <= new Date();
      if (can('acquisition.search') && !e.have && aired) {
        actions(r).appendChild(button('Search', null, function () {
          searchForEpisode(e.id, 'S' + pad(e.season) + 'E' + pad(e.number), r);
        }));
      }
      if (can('library.edit') && !e.have) {
        actions(r).appendChild(button(e.monitored ? 'Unmonitor' : 'Monitor', null, function (b) {
          b.disabled = true;
          api('PUT', '/api/v1/episodes/' + e.id + '/monitored', { monitored: !e.monitored })
            .then(function (res) {
              if (res.status !== 200) { b.disabled = false; return fail(res, 'could not change monitoring'); }
              reload();
            });
        }));
      }
      body.appendChild(r);
    });
    return wrap;
  }

  // wanted
  //
  // Everything the library should have and does not: films added with no file
  // (ADR-0026), then episodes across every series. Episodes the provider lists
  // with no air date are absent on purpose: they are announced, not overdue.
  loaders.wanted = function () {
    var list = $('wanted-list');
    var filmList = $('wanted-films');
    clear(list);
    clear(filmList);
    $('wanted-films-head').hidden = true;
    $('wanted-episodes-head').hidden = true;
    $('wanted-albums-head').hidden = true;
    clear($('wanted-albums'));
    $('wanted-books-head').hidden = true;
    clear($('wanted-books'));
    api('GET', '/api/v1/wanted').then(function (res) {
      if (res.status !== 200 || !res.body) { return fail(res, 'could not read the wanted list'); }
      (res.body.albums || []).forEach(function (a) {
        var r = row(a.artist + ' — ' + a.title + (a.year ? ' (' + a.year + ')' : ''),
          a.type + (a.tracks_known ? ' · ' + a.have + ' of ' + a.known + ' track(s) held' : ''));
        var auto = automaticLine(a.automatic);
        if (auto) { r.appendChild(auto); }
        if (can('acquisition.search')) {
          actions(r).appendChild(button('Search', 'ghost', function () {
            searchForAlbum(a.id, a.artist + ' — ' + a.title, r);
          }));
        }
        $('wanted-albums').appendChild(r);
      });
      $('wanted-albums-head').hidden = !(res.body.albums || []).length;
      (res.body.books || []).forEach(function (b) {
        var r = row(b.name, 'book · added ' + when(b.added_at));
        var bookAuto = automaticLine(b.automatic);
        if (bookAuto) { r.appendChild(bookAuto); }
        var acts = actions(r);
        if (can('acquisition.search')) {
          acts.appendChild(button('Search', 'ghost', function () {
            searchForBook(b.item_id, b.name, r);
          }));
        }
        acts.appendChild(openInLibrary(b.item_id));
        $('wanted-books').appendChild(r);
      });
      $('wanted-books-head').hidden = !(res.body.books || []).length;
      $('wanted-note').textContent = res.body.note || '';
      $('wanted-automatic').textContent = automaticSummary(res.body.automatic);
      var rows = res.body.wanted || [];
      var films = res.body.films || [];
      /* Headings only when both kinds are here: one list needs no labels. */
      $('wanted-films-head').hidden = !films.length || !rows.length;
      $('wanted-episodes-head').hidden = !films.length || !rows.length;
      films.forEach(function (f) {
        var r = el('div', 'item');
        r.appendChild(el('div', 'title', f.name));
        r.appendChild(el('div', 'meta', 'film · added ' + when(f.added_at)));
        var line = automaticLine(f.automatic);
        if (line) { r.appendChild(line); }
        var a = actions(r);
        if (can('acquisition.search')) {
          a.appendChild(button('Search', null, function () {
            searchForFilm(f.item_id, f.name, r);
          }));
        }
        /* With automatic acquisition on, the Wanted list is what gets
         * downloaded, so taking something off it belongs here too. */
        if (can('library.edit')) {
          a.appendChild(button('Unmonitor', 'ghost', function (btn) {
            btn.disabled = true;
            api('PUT', '/api/v1/media/' + f.item_id + '/monitored', { monitored: false })
              .then(function (u) {
                if (u.status !== 200) { btn.disabled = false; return fail(u, 'could not unmonitor'); }
                ok((u.body && u.body.note) || 'Unmonitored.');
                loaders.wanted();
              });
          }));
        }
        filmList.appendChild(r);
      });
      if (!rows.length && (films.length || (res.body.albums || []).length ||
          (res.body.books || []).length)) { return; }
      if (!rows.length) {
        return empty(list, 'Nothing is missing — or nothing is being followed yet. ' +
          'Add a film or a series from Add; a series\u2019 episode list comes from the ' +
          'provider, and can be refreshed from its page in the library.');
      }
      rows.forEach(function (w) {
        var r = el('div', 'item');
        r.appendChild(el('div', 'title', w.series + ' \u2014 S' + pad(w.season) +
          'E' + pad(w.number) + (w.title ? ' \u2014 ' + w.title : '')));
        r.appendChild(el('div', 'meta', 'aired ' +
          (w.aired_at ? new Date(w.aired_at).toLocaleDateString() : 'unknown')));
        var line = automaticLine(w.automatic);
        if (line) { r.appendChild(line); }
        var a = actions(r);
        if (can('acquisition.search')) {
          a.appendChild(button('Search', null, function () {
            searchForEpisode(w.id, w.series + ' S' + pad(w.season) + 'E' + pad(w.number), r);
          }));
        }
        if (can('library.edit')) {
          a.appendChild(button('Unmonitor', 'ghost', function (btn) {
            btn.disabled = true;
            api('PUT', '/api/v1/episodes/' + w.id + '/monitored', { monitored: false })
              .then(function (u) {
                if (u.status !== 200) { btn.disabled = false; return fail(u, 'could not unmonitor'); }
                ok('Unmonitored: it is off the Wanted list, and nothing will fetch it.');
                loaders.wanted();
              });
          }));
        }
        list.appendChild(r);
      });
    });
  };

  /* The Wanted screen's one sentence about automatic acquisition (ADR-0030):
   * off, or on with its budget \u2014 the numbers an operator needs to predict how
   * soon something will arrive. */
  function automaticSummary(auto) {
    if (!auto) { return ''; }
    if (!auto.enabled) { return auto.note || ''; }
    var text = 'Automatic acquisition is on: the indexers\u2019 recent releases are checked ' +
      every(auto.recent_every_seconds) + ', and up to ' + auto.searches_per_run +
      ' item(s) are searched for ' + every(auto.search_every_seconds) +
      '. What the default quality profile accepts is downloaded, at most ' +
      auto.max_grabs_per_run + ' a pass. Unmonitor anything you do not want fetched.';
    if (auto.error) { text += ' ' + auto.error; }
    return text;
  }

  /* What automatic acquisition last did about one wanted item, and what it
   * will do next. Absent when it is off. The detail is built server-side from
   * release names a stranger wrote, so it goes in as text, like everything. */
  function automaticLine(a) {
    if (!a) { return null; }
    var text;
    switch (a.status) {
      case 'downloading':
        text = a.outcome === 'grabbed' ? 'Downloading \u2014 ' + a.detail + '.' : a.note;
        break;
      case 'grabbed':
        text = 'Grabbed automatically ' + when(a.searched_at) + ': ' + a.detail + '.';
        break;
      case 'nothing':
        text = 'Searched ' + when(a.searched_at) + ': ' + a.detail + '. ' +
          (a.next_search_at ? 'Next search ' + when(a.next_search_at) + '.' : 'Searched again soon.');
        break;
      case 'failed':
        text = 'The search ' + when(a.searched_at) + ' failed: ' + a.detail + '. ' +
          (a.next_search_at ? 'Tried again ' + when(a.next_search_at) + '.' : 'Tried again soon.');
        break;
      default:
        text = a.note || '';
    }
    return el('div', 'muted small automatic ' + (a.status || ''), text);
  }

  loaders.library = function () {
    var kind = $('library-kind').value;
    api('GET', '/api/v1/media' + (kind ? '?kind=' + encodeURIComponent(kind) : ''))
      .then(function (res) {
        if (res.status !== 200 || !res.body) { return fail(res, 'could not read the library'); }
        libraryItems = res.body.items || [];
        $('library-detail').hidden = true;
        renderLibrary();
        /* Opened from somewhere else — the Add screen. Done here, after the
         * list has landed, because this callback hides the detail pane: opening
         * the item first would race it and lose. */
        if (openAfterLoad) {
          var id = openAfterLoad;
          openAfterLoad = null;
          showItem(id).then(function () { $('library-detail').scrollIntoView(); });
        }
      });
  };

  function wireLibrary() {
    $('library-kind').addEventListener('change', function () { loaders.library(); });
    $('library-filter').addEventListener('input', renderLibrary);
  }

  // -------------------------------------------------------------------------
  // add a series or a film (ADR-0025, ADR-0026)
  // -------------------------------------------------------------------------

  /* What each choice means, in the ADR's words. There is no default: the
   * select starts on a placeholder and Add refuses until something is chosen,
   * because "all" and "future" differ by a whole back catalogue on the wanted
   * list — and the server refuses a missing choice too, so this is a courtesy,
   * not the rule. */
  var MONITOR_CHOICES = [
    ['all', 'All episodes — everything that has aired is wanted'],
    ['future', 'Future episodes — only what airs from now on'],
    ['latest', 'Latest season — the current season and everything after it'],
    ['none', 'None — follow it without wanting anything']
  ];

  /* The item the library view opens once its list has loaded; see there. */
  var openAfterLoad = null;
  /* The approved request the Add screen is adding for, if it is (ADR-0028):
   * what it asked for, so the screen can search for that and link whatever is
   * added to it. */
  var addingFor = null;

  function startAddForRequest(rq) {
    addingFor = { id: rq.id, kind: rq.kind === 'series' ? 'series' : 'movie',
      title: rq.title, year: rq.year || 0, asker: rq.requested_by || 'somebody',
      searched: false };
    window.location.hash = '#add';
  }

  /* The line at the top of the Add screen that says it is adding for a
   * request, and lets that be abandoned. */
  function showAddingFor() {
    var box = $('add-for');
    clear(box);
    if (!addingFor) { box.hidden = true; return; }
    var what = addingFor.kind === 'movie' ? 'film' : 'series';
    box.appendChild(el('span', null, 'Adding for ' + addingFor.asker + '\u2019s request for the ' +
      what + ' \u201c' + addingFor.title + '\u201d' +
      (addingFor.year ? ' (' + addingFor.year + ')' : '') +
      '. Whatever you add here is linked to it, and the request is fulfilled when a file ' +
      'of it arrives.'));
    box.appendChild(button('Not for the request', 'ghost', function () {
      addingFor = null;
      showAddingFor();
    }));
    box.hidden = false;
  }

  /* linkRequest links a request to a library item; the errand is over once it
   * has been. */
  function linkRequest(rq, itemID) {
    return api('POST', '/api/v1/requests/' + rq.id + '/item', { media_item_id: itemID })
      .then(function (res) {
        if (res.status === 200 && addingFor === rq) {
          addingFor = null;
          showAddingFor();
        }
        return res;
      });
  }

  /* afterAdd says an add succeeded — and, when it was for a request, links the
   * request to what was added and says that in the same sentence. An add whose
   * link fails is still an add, and the banner says the link did not happen. */
  function afterAdd(said, itemID) {
    var rq = addingFor;
    if (!rq) { ok(said); return; }
    linkRequest(rq, itemID).then(function (res) {
      if (res.status !== 200) {
        return fail(res, 'it was added, but the request could not be linked to it');
      }
      ok(said + ' ' + ((res.body && res.body.message) || 'The request is linked to it.'));
    });
  }

  /* The title a request asked for is already in the library: offer to link the
   * request to it. Only for that conflict — never for whatever item happens to
   * occupy the folder the add would have used. The request is the one being
   * added for when the refusal came back, whatever happens to the errand
   * after. */
  function offerLinkToExisting(box, res) {
    var rq = addingFor;
    if (!rq || !res.body || res.body.conflict !== 'already_in_library') { return; }
    box.appendChild(button('Link the request to it', 'primary', function (b) {
      b.disabled = true;
      linkRequest(rq, res.body.item.id).then(function (r) {
        b.disabled = false;
        if (r.status !== 200) { return fail(r, 'could not link the request to it'); }
        ok((r.body && r.body.message) || 'Linked.');
        b.textContent = 'Linked';
        b.disabled = true;
      });
    }));
  }

  /* Root folders by their kind — "series" or "movies" — read once per visit. */
  var addRoots = {};

  loaders.add = function () {
    /* Re-read on every visit: a root folder added under Storage a minute ago
     * should be offered without a reload. */
    addRoots = {};
    showAddingFor();
    if (addingFor && !addingFor.searched) {
      /* Search the provider for what the request says, once. The approver
       * chooses the match: the request is words, the item is the provider's. */
      addingFor.searched = true;
      $('add-kind').value = addingFor.kind;
      $('add-term').value = addingFor.title;
      $('add-year').value = addingFor.year ? String(addingFor.year) : '';
      $('add-form').requestSubmit();
      return;
    }
    if (!$('add-results').firstChild) {
      empty($('add-results'), 'Search the metadata provider for a series or a film to add.');
    }
  };

  function loadRootsOf(kind) {
    if (addRoots[kind]) { return Promise.resolve(addRoots[kind]); }
    return api('GET', '/api/v1/rootfolders').then(function (res) {
      if (res.status !== 200 || !res.body) { return []; }
      addRoots[kind] = (res.body.root_folders || []).filter(function (rf) {
        return rf.kind === kind;
      });
      return addRoots[kind];
    });
  }

  function wireAdd() {
    /* A result row is added as the kind it was searched as, so results of the
     * other kind are cleared rather than left under the wrong heading. */
    $('add-kind').addEventListener('change', function () {
      empty($('add-results'), 'Search the metadata provider for a ' +
        ({ movie: 'film', artist: 'artist', book: 'book' }[$('add-kind').value] || 'series') + ' to add.');
    });
    submit($('add-form'), function () {
      var results = $('add-results');
      if ($('add-kind').value === 'artist') { return searchArtists(results); }
      if ($('add-kind').value === 'book') { return searchBooks(results); }
      var kind = $('add-kind').value === 'movie' ? 'movie' : 'series';
      var q = '/api/v1/metadata/search?kind=' + kind + '&title=' +
        encodeURIComponent($('add-term').value);
      if ($('add-year').value) { q += '&year=' + encodeURIComponent($('add-year').value); }
      empty(results, 'Asking the provider…');
      return api('GET', q).then(function (res) {
        if (res.status !== 200 || !res.body) {
          empty(results, '');
          return fail(res, 'the search failed');
        }
        var matches = res.body.matches || [];
        if (!matches.length) { return empty(results, 'The provider has nothing by that name.'); }
        clear(results);
        matches.forEach(function (m) { results.appendChild(addCandidate(m, kind)); });
      });
    });
  }

  /* Artists come from MusicBrainz, not the film and series provider
   * (ADR-0044). The disambiguation is what tells two artists of one name
   * apart, so it is shown. */
  var ALBUM_MONITOR_CHOICES = [
    ['all', 'All albums — everything released is wanted'],
    ['future', 'Future albums — only what is released from now on'],
    ['latest', 'Latest album — the newest, and everything after it'],
    ['none', 'None — follow the artist without wanting anything']
  ];

  function searchArtists(results) {
    empty(results, 'Asking MusicBrainz…');
    return api('GET', '/api/v1/music/artists?q=' + encodeURIComponent($('add-term').value)).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(results, ''); return fail(res, 'the search failed'); }
      if (!res.body.count) { return empty(results, 'MusicBrainz has no artist by that name.'); }
      clear(results);
      res.body.artists.forEach(function (a) {
        var about = [a.disambiguation, a.type, a.country, a.begin ? 'from ' + a.begin : '']
          .filter(function (x) { return x; }).join(' · ');
        var r = row(a.name, about || null);
        actions(r).appendChild(button('Add…', 'ghost', function (b) {
          b.disabled = true;
          loadRootsOf('music').then(function (roots) { r.appendChild(addArtistChoice(a, roots)); });
        }));
        results.appendChild(r);
      });
    });
  }

  function addArtistChoice(a, roots) {
    var box = el('div', 'add-choice');
    if (!roots.length) {
      box.appendChild(el('div', 'why', 'There is no root folder for music yet. Add one under Storage, then come back.'));
      return box;
    }
    var key = { provider_id: a.musicbrainz_id };
    var monitor = labelled(box, 'add-monitor-' + a.musicbrainz_id, 'Which albums do you want?',
      choices('Choose…', ALBUM_MONITOR_CHOICES));
    var place = whereItLives(box, key, roots, 'artist');
    box.appendChild(button('Add artist', 'primary', function (b) {
      if (!monitor.value) { return refuse('choose which albums you want first'); }
      if (place.root && !place.root.value) { return refuse('choose where the music lives first'); }
      var body = { kind: 'artist', musicbrainz_id: a.musicbrainz_id, monitor: monitor.value };
      if (place.root) { body.root_folder_id = Number(place.root.value); }
      if (place.folder.value) { body.folder = place.folder.value; }
      b.disabled = true;
      b.textContent = 'Adding — reading the albums from MusicBrainz…';
      api('POST', '/api/v1/media', body).then(function (res) {
        b.disabled = false;
        b.textContent = 'Add artist';
        if (res.status === 201 && res.body) {
          var it = res.body.item || {};
          afterAdd('Added ' + it.title + ': ' + res.body.albums + ' album(s), ' + res.body.wanted + ' wanted.', it.id);
          clear(box);
          box.appendChild(el('div', 'note', res.body.note));
          box.appendChild(openInLibrary(it.id));
          return;
        }
        fail(res, 'could not add them');
        if (res.status === 409 && res.body && res.body.item) { box.appendChild(openInLibrary(res.body.item.id)); }
      });
    }));
    return box;
  }

  /* Books come from Open Library (ADR-0048), added one at a time as a film
   * is. The number of editions is shown: it is what tells the book everybody
   * means from a study guide of the same name. */
  function searchBooks(results) {
    empty(results, 'Asking Open Library…');
    return api('GET', '/api/v1/books/search?q=' + encodeURIComponent($('add-term').value)).then(function (res) {
      if (res.status !== 200 || !res.body) { empty(results, ''); return fail(res, 'the search failed'); }
      if (!res.body.count) { return empty(results, 'Open Library has no book by that name.'); }
      clear(results);
      res.body.books.forEach(function (b) {
        var about = [(b.authors || []).join(', '), b.editions ? b.editions + ' edition(s)' : '']
          .filter(function (x) { return x; }).join(' · ');
        var r = row(b.title + (b.year ? ' (' + b.year + ')' : ''), about || null);
        actions(r).appendChild(button('Add…', 'ghost', function (btn) {
          btn.disabled = true;
          loadRootsOf('books').then(function (roots) { r.appendChild(addBookChoice(b, roots)); });
        }));
        results.appendChild(r);
      });
    });
  }

  function addBookChoice(b, roots) {
    var box = el('div', 'add-choice');
    if (!roots.length) {
      box.appendChild(el('div', 'why', 'There is no root folder for books yet. Add one under Storage, then come back.'));
      return box;
    }
    var place = whereItLives(box, { provider_id: b.openlibrary_id }, roots, 'author and book');
    box.appendChild(button('Add book', 'primary', function (btn) {
      if (place.root && !place.root.value) { return refuse('choose where the books live first'); }
      var body = { kind: 'book', openlibrary_id: b.openlibrary_id };
      if (place.root) { body.root_folder_id = Number(place.root.value); }
      if (place.folder.value) { body.folder = place.folder.value; }
      btn.disabled = true;
      api('POST', '/api/v1/media', body).then(function (res) {
        btn.disabled = false;
        if (res.status === 201 && res.body) {
          var it = res.body.item || {};
          afterAdd('Added ' + (res.body.name || it.title) + '.', it.id);
          clear(box);
          box.appendChild(el('div', 'note', res.body.note));
          box.appendChild(openInLibrary(it.id));
          return;
        }
        fail(res, 'could not add it');
        if (res.status === 409 && res.body && res.body.item) { box.appendChild(openInLibrary(res.body.item.id)); }
      });
    }));
    return box;
  }

  /* An artist's albums (ADR-0044): what is held of each, whether it is
   * wanted, and its tracks — fetched from MusicBrainz the first time. */
  function showAlbums(box, itemID) {
    empty(box, 'Reading the albums…');
    api('GET', '/api/v1/media/' + itemID + '/albums').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(box, failure(res, 'could not read the albums')); return; }
      if (!res.body.count) { empty(box, 'MusicBrainz lists no albums or EPs for this artist.'); return; }
      clear(box);
      res.body.albums.forEach(function (a) {
        var r = row(a.title + (a.year ? ' (' + a.year + ')' : ''),
          a.type + ' · ' + (a.tracks_known ? a.have + ' of ' + a.known + ' track(s) held' : 'track list not read yet'));
        r.firstChild.appendChild(el('span', a.monitored ? 'badge good' : 'badge', a.monitored ? 'wanted' : 'not wanted'));
        var tracks = el('div', 'list compact');
        tracks.hidden = true;
        var acts = actions(r);
        acts.appendChild(button('Tracks', 'ghost', function (b) {
          if (!tracks.hidden) { tracks.hidden = true; return; }
          b.disabled = true;
          api('GET', '/api/v1/albums/' + a.id).then(function (u) {
            b.disabled = false;
            if (u.status !== 200 || !u.body) { return fail(u, 'could not read the tracks'); }
            clear(tracks);
            (u.body.tracks || []).forEach(function (t) {
              var tr = row((u.body.tracks.length > 20 || t.disc > 1 ? t.disc + '-' : '') + pad(t.number) + ' ' + t.title,
                t.file_id ? 'held' : 'missing');
              if (t.file_id) {
                actions(tr).appendChild(button('Play', 'ghost', function () { watch(t.file_id, a.artist + ' — ' + t.title); }));
              }
              tracks.appendChild(tr);
            });
            if (!(u.body.tracks || []).length) { tracks.appendChild(el('div', 'empty', 'MusicBrainz lists no official release with a track list.')); }
            tracks.hidden = false;
          });
        }));
        if (can('acquisition.search')) {
          acts.appendChild(button('Search', 'ghost', function () {
            searchForAlbum(a.id, a.artist + ' — ' + a.title, r);
          }));
        }
        if (can('library.edit')) {
          acts.appendChild(button(a.monitored ? 'Unmonitor' : 'Monitor', 'ghost', function (b) {
            b.disabled = true;
            api('PUT', '/api/v1/albums/' + a.id + '/monitored', { monitored: !a.monitored }).then(function (u) {
              if (u.status !== 200) { b.disabled = false; return fail(u, 'could not change that'); }
              showAlbums(box, itemID);
            });
          }));
        }
        r.appendChild(tracks);
        box.appendChild(r);
      });
    });
  }

  function addCandidate(m, kind) {
    var r = row(m.title + (m.year ? ' (' + m.year + ')' : ''),
      m.original_title ? 'originally ' + m.original_title : null);
    if (m.overview) { r.appendChild(el('div', 'overview', m.overview)); }
    actions(r).appendChild(button('Add…', 'ghost', function (b) {
      b.disabled = true;
      loadRootsOf(kind === 'movie' ? 'movies' : 'series').then(function (roots) {
        r.appendChild(kind === 'movie' ? addFilmChoice(m, roots) : addChoice(m, roots));
      });
    }));
    return r;
  }

  /* labelled builds a label and control pair with the label bound to it, so
   * clicking the words focuses the control and a screen reader names it. */
  function labelled(box, id, text, control) {
    var l = el('label', null, text);
    l.setAttribute('for', id);
    control.id = id;
    box.appendChild(l);
    box.appendChild(control);
    return control;
  }

  function choices(placeholder, options) {
    var s = el('select');
    var first = el('option', null, placeholder);
    first.value = '';
    s.appendChild(first);
    options.forEach(function (o) {
      var opt = el('option', null, o[1]);
      opt.value = o[0];
      s.appendChild(opt);
    });
    return s;
  }

  function addChoice(m, roots) {
    var box = el('div', 'add-choice');
    if (!roots.length) {
      box.appendChild(el('div', 'why', 'There is no root folder for series yet. ' +
        'Add one under Storage, then come back.'));
      return box;
    }

    var monitor = labelled(box, 'add-monitor-' + m.provider_id,
      'Which episodes do you want?', choices('Choose…', MONITOR_CHOICES));

    var place = whereItLives(box, m, roots, 'series');
    var root = place.root;
    var folder = place.folder;

    var opener = null;
    box.appendChild(button('Add series', 'primary', function (b) {
      if (!monitor.value) { return refuse('choose which episodes you want first'); }
      if (root && !root.value) { return refuse('choose where the series lives first'); }
      var body = { kind: 'series', tmdb_id: m.provider_id, monitor: monitor.value };
      if (root) { body.root_folder_id = Number(root.value); }
      if (folder.value) { body.folder = folder.value; }

      b.disabled = true;
      b.textContent = 'Adding — reading every season from the provider…';
      api('POST', '/api/v1/media', body).then(function (res) {
        b.disabled = false;
        b.textContent = 'Add series';
        if (res.status === 201 && res.body) {
          var it = res.body.item || {};
          afterAdd('Added ' + it.title + ': ' + res.body.seasons + ' season(s), ' +
            res.body.episodes + ' episode(s), ' + res.body.wanted + ' wanted.', it.id);
          clear(box);
          box.appendChild(el('div', 'note', res.body.note));
          box.appendChild(openInLibrary(it.id));
          return;
        }
        fail(res, 'could not add it');
        /* Already in the library, or its folder is taken: say which item,
         * and offer to go to it, once — and, when adding for a request, to
         * link the request to the title already there. */
        if (res.status === 409 && res.body && res.body.item && !opener) {
          opener = box.appendChild(openInLibrary(res.body.item.id));
          offerLinkToExisting(box, res);
        }
      });
    }));
    return box;
  }

  /* whereItLives asks where a title will live: the root folder, when there is
   * more than one of its kind, and a folder name, which is optional.
   *
   * One root folder is not a choice, and is not asked; several are the
   * operator's to choose between — a title lives in its folder for years, and
   * which disk that is belongs to them. */
  function whereItLives(box, m, roots, what) {
    var root = null;
    if (roots.length === 1) {
      box.appendChild(el('div', 'muted small', 'Lives in ' + roots[0].path));
    } else {
      root = labelled(box, 'add-root-' + m.provider_id, 'Where does it live?',
        choices('Choose…', roots.map(function (rf) {
          return [String(rf.id), rf.path + ' (' + bytes(rf.free_bytes) + ' free)'];
        })));
    }
    var folder = el('input');
    folder.type = 'text';
    folder.maxLength = 200;
    folder.placeholder = 'leave empty to name it after the ' + what;
    labelled(box, 'add-folder-' + m.provider_id, 'Folder name (optional)', folder);
    return { root: root, folder: folder };
  }

  /* Adding a film (ADR-0026). No monitoring choice: adding a film is wanting
   * it. Once added, it can be searched for from here at once. */
  function addFilmChoice(m, roots) {
    var box = el('div', 'add-choice');
    if (!roots.length) {
      box.appendChild(el('div', 'why', 'There is no root folder for films yet. ' +
        'Add one under Storage, then come back.'));
      return box;
    }
    var place = whereItLives(box, m, roots, 'film');

    var opener = null;
    box.appendChild(button('Add film', 'primary', function (b) {
      if (place.root && !place.root.value) { return refuse('choose where the film lives first'); }
      var body = { kind: 'movie', tmdb_id: m.provider_id };
      if (place.root) { body.root_folder_id = Number(place.root.value); }
      if (place.folder.value) { body.folder = place.folder.value; }

      b.disabled = true;
      b.textContent = 'Adding…';
      api('POST', '/api/v1/media', body).then(function (res) {
        b.disabled = false;
        b.textContent = 'Add film';
        if (res.status === 201 && res.body) {
          var it = res.body.item || {};
          var name = it.title + (it.year ? ' (' + it.year + ')' : '');
          afterAdd('Added ' + name + '. It is on the Wanted list.', it.id);
          clear(box);
          box.appendChild(el('div', 'note', res.body.note));
          var a = el('div', 'actions');
          if (can('acquisition.search')) {
            a.appendChild(button('Search for it now', 'primary', function () {
              searchForFilm(it.id, name, box);
            }));
          }
          a.appendChild(openInLibrary(it.id));
          box.appendChild(a);
          return;
        }
        fail(res, 'could not add it');
        if (res.status === 409 && res.body && res.body.item && !opener) {
          opener = box.appendChild(openInLibrary(res.body.item.id));
          offerLinkToExisting(box, res);
        }
      });
    }));
    return box;
  }

  function openInLibrary(id) {
    return button('Open in the library', 'ghost', function () {
      openAfterLoad = id;
      window.location.hash = '#library';
    });
  }

  // -------------------------------------------------------------------------
  // watch
  // -------------------------------------------------------------------------

  /* What this browser will actually decode, asked of the browser rather than
   * assumed from its user agent. The server answers with a conservative
   * default because a library listing has no browser in the conversation; here
   * there is one, and canPlayType is free.
   *
   * "probably" and "maybe" are both treated as yes. A browser saying "maybe"
   * is saying it cannot tell without the bytes, and refusing on that basis
   * would reject files that play. Being wrong here costs a failed <video>
   * load, which the error handler turns into a sentence. */
  function browserCan(type) {
    try {
      var v = document.createElement('video');
      return v.canPlayType(type) !== '';
    } catch (e) { return false; }
  }

  var watchState = { fileId: null, title: '' };
  /* A generation counter, bumped every time the view is asked to load.
   *
   * Two things call loaders.watch: the Play button, and the hashchange the Play
   * button causes. Both were rendering, and the refusal list came out with
   * every blocker in it twice — the clear happens synchronously and both
   * responses arrive after it.
   *
   * Guarding the caller would have fixed that one case and not the real one: a
   * viewer who presses Play on a second film before the first answer arrives.
   * A late response for a film nobody is looking at any more must be dropped,
   * whatever caused it. */
  var watchGeneration = 0;
  /* Where the player last told the server it was, so an unload does not send a
   * position identical to the one already stored. */
  var lastSaved = -1;

  function blockerRow(b) {
    var r = el('div', 'item');
    r.appendChild(el('div', 'title', b.says));
    r.appendChild(el('div', 'meta', b.code + ' — ' + b.what));
    return r;
  }

  /* A one-line answer to "what is in this file", which is the question an
   * operator asks when something will not play. */
  function trackSummary(info) {
    var parts = [];
    (info.video || []).forEach(function (v) {
      var s = (v.codec || '').toUpperCase() + ' ' + v.width + '\u00d7' + v.height;
      if (v.bit_depth && v.bit_depth > 8) { s += ' ' + v.bit_depth + '-bit'; }
      if (v.hdr) { s += ' HDR'; }
      parts.push(s);
    });
    (info.audio || []).forEach(function (a) {
      var s = (a.codec || '').toUpperCase();
      if (a.layout) { s += ' ' + a.layout; }
      else if (a.channels) { s += ' ' + a.channels + 'ch'; }
      if (a.language) { s += ' (' + a.language + ')'; }
      if (a.default) { s += ' \u2605'; }
      parts.push(s);
    });
    return parts.join('  \u00b7  ');
  }

  /* Subtitles.
   *
   * A <track> element takes WebVTT and nothing else, so the server converts on
   * the way out; everything here deals in ids the server issued and never in
   * filenames or stream indices.
   *
   * The subtitle menu is the browser's own. Chrome, Firefox and Safari all put
   * a captions button in the default controls as soon as a media element has
   * text tracks, and it already handles the things a hand-built menu gets
   * wrong — keyboard access, the viewer's own caption styling, and full
   * screen. Building a second one would be worse and larger.
   */

  /* Enough of ISO 639-2 to cover what actually appears on releases. A code
   * that is not here is shown as it came, uppercased, which is honest: a made
   * up language name would be worse than three letters. */
  var LANGUAGES = {
    eng: 'English', en: 'English', spa: 'Spanish', es: 'Spanish',
    fre: 'French', fra: 'French', fr: 'French', ger: 'German', deu: 'German',
    de: 'German', ita: 'Italian', it: 'Italian', por: 'Portuguese',
    pt: 'Portuguese', rus: 'Russian', ru: 'Russian', jpn: 'Japanese',
    ja: 'Japanese', kor: 'Korean', ko: 'Korean', chi: 'Chinese',
    zho: 'Chinese', zh: 'Chinese', ara: 'Arabic', ar: 'Arabic',
    dut: 'Dutch', nld: 'Dutch', nl: 'Dutch', swe: 'Swedish', sv: 'Swedish',
    nor: 'Norwegian', no: 'Norwegian', dan: 'Danish', da: 'Danish',
    fin: 'Finnish', fi: 'Finnish', pol: 'Polish', pl: 'Polish',
    tur: 'Turkish', tr: 'Turkish', hin: 'Hindi', hi: 'Hindi',
    cze: 'Czech', ces: 'Czech', cs: 'Czech', hun: 'Hungarian', hu: 'Hungarian',
    gre: 'Greek', ell: 'Greek', el: 'Greek', heb: 'Hebrew', he: 'Hebrew',
    tha: 'Thai', th: 'Thai', vie: 'Vietnamese', vi: 'Vietnamese'
  };

  function subtitleLabel(t) {
    var name = LANGUAGES[(t.language || '').toLowerCase()] ||
      (t.language ? t.language.toUpperCase() : 'Unknown language');
    if (t.title) { name += ' \u2014 ' + t.title; }
    if (t.forced) { name += ' (forced)'; }
    if (!t.embedded) { name += ' (file)'; }
    return name;
  }

  /* Old tracks go before new ones arrive. Changing video.src does not remove
   * track children, so without this a viewer switching films would be offered
   * the previous film's subtitles — which would then load, because the ids are
   * still valid for the OTHER file. */
  function clearTracks(video) {
    var tracks = video.querySelectorAll('track');
    for (var i = 0; i < tracks.length; i++) {
      video.removeChild(tracks[i]);
    }
  }

  /* Where a subtitle failure is said.
   *
   * Shared, because the first version of this passed null from the conversion
   * path and a track that 422'd there said nothing at all — found by turning
   * on a deliberately malformed subtitle in a real browser. A failure handler
   * that some callers pass and others do not is not a failure handler. */
  function subtitleComplaint(label) {
    var blockers = $('watch-blockers');
    if (!blockers) { return; }
    blockers.appendChild(el('div', 'item',
      'The subtitle track \u201c' + label + '\u201d would not load. The file ' +
      'is there, but this server could not turn it into something a browser ' +
      'can show.'));
  }

  /* The tracks are attached to the ELEMENT, not to the source, so they survive
   * a conversion: a remuxed stream carries video and one audio track and no
   * subtitles at all, and these still work because they are fetched
   * separately. */
  function attachSubtitles(video, fileId, list) {
    clearTracks(video);
    var forcedUsed = false;
    (list || []).forEach(function (t) {
      if (!t.usable) { return; }
      var track = document.createElement('track');
      track.kind = 'subtitles';
      track.label = subtitleLabel(t);
      if (t.language) { track.srclang = t.language; }
      track.src = '/api/v1/files/' + fileId + '/subtitles/' +
        encodeURIComponent(t.id);
      /* A forced track is the one the film intends everybody to see — the
       * signs and the foreign dialogue — so it is the only one turned on
       * without being asked for. At most one, because the element takes at
       * most one default. */
      if (t.forced && !forcedUsed) { track.default = true; forcedUsed = true; }
      /* A track that fails loads nothing and says nothing: the viewer picks it
       * from the menu and the picture simply stays bare. Said out loud
       * instead. */
      track.addEventListener('error', function () {
        subtitleComplaint(track.label);
      });
      video.appendChild(track);
    });
    return video.querySelectorAll('track').length;
  }

  function subtitleCount(info) {
    var list = info.subtitle_tracks || [];
    var usable = list.filter(function (t) { return t.usable; }).length;
    if (!list.length) { return ''; }
    if (usable === list.length) { return String(usable); }
    /* Both numbers, because "3" next to a menu offering one is a bug report.
     * The reason each is unavailable is listed below. */
    return usable + ' of ' + list.length;
  }

  /* Every track, including the ones that are not offered, each with the reason.
   * A viewer who can see that a PGS track exists and why it cannot be shown has
   * been told something true; a menu that silently omits it has not. */
  function subtitleDetail(detail, list) {
    if (!list || !list.length) { return; }
    detail.appendChild(el('h3', null, 'Subtitles'));
    var rows = el('div', 'list compact');
    list.forEach(function (t) {
      var row = el('div', 'item');
      row.appendChild(el('div', 'title', subtitleLabel(t)));
      row.appendChild(el('div', 'meta', t.usable
        ? (t.embedded ? 'In the file' : 'A file beside the video') +
          ' \u00b7 ' + (t.codec || '').toUpperCase() +
          ' \u00b7 converted to WebVTT when you turn it on'
        : t.why || 'Not available.'));
      rows.appendChild(row);
    });
    detail.appendChild(rows);
  }

  function watch(fileId, title) {
    watchState = { fileId: fileId, title: title || 'Watch' };
    /* The hash may already be #watch — switching from one film to another —
     * in which case no hashchange fires and showView never runs. So load
     * directly, and let the generation guard discard whichever of the two
     * requests loses. */
    window.location.hash = '#watch';
    loaders.watch();
  }

  loaders.watch = function () {
    var mine = ++watchGeneration;
    var video = $('watch-video');
    var player = $('watch-player');
    var blockers = $('watch-blockers');
    var detail = $('watch-detail');

    $('watch-title').textContent = watchState.title || 'Watch';
    clear(blockers);
    clear(detail);
    detail.hidden = true;

    if (!watchState.fileId) {
      player.hidden = true;
      $('watch-summary').textContent =
        'Choose something from the library and press Play.';
      return;
    }

    /* Stop whatever was playing before the next request goes out. Leaving the
     * old source attached means the browser keeps streaming a file the viewer
     * has navigated away from, which on a metered connection is somebody
     * else's data bill. */
    video.pause();
    video.removeAttribute('src');
    clearTracks(video);
    video.load();
    player.hidden = true;
    $('watch-summary').textContent = 'Reading the file\u2026';

    api('GET', '/api/v1/files/' + watchState.fileId + '/playback')
      .then(function (res) {
        /* Somebody asked for something else while this was in flight. */
        if (mine !== watchGeneration) { return; }
        if (res.status !== 200 || !res.body) {
          $('watch-summary').textContent = '';
          return fail(res, 'could not read that file');
        }
        var info = res.body;
        $('watch-summary').textContent = info.summary || '';

        /* The server's opinion is conservative on purpose. Ask the browser
         * before refusing: it may well decode something the default set left
         * out, and a refusal a viewer could have disproved by pressing play is
         * the wrong kind of caution. */
        var serverSaysNo = !info.direct_play;
        var browserSaysYes = serverSaysNo && askTheBrowser(info);

        if (serverSaysNo && !browserSaysYes) {
          (info.blockers || []).forEach(function (b) {
            blockers.appendChild(blockerRow(b));
          });
          if (!(info.blockers || []).length) {
            blockers.appendChild(el('div', 'empty', 'This file cannot be played.'));
          }

          /* An offer, or an explanation of why there is none.
           *
           * Converting copies the video and rebuilds the audio, so it helps
           * when the container or the soundtrack was the problem and does
           * nothing when the pictures were. The server has already worked out
           * which, so the button appears only when pressing it would achieve
           * something — and when it would not, the reason is shown rather than
           * the button simply being absent. */
          if (info.can_convert) {
            var row = el('div', 'item');
            row.appendChild(el('div', 'title', 'This server can convert it'));
            row.appendChild(el('div', 'meta', info.convert_reencodes_audio
              ? 'The picture is copied unchanged and only the soundtrack is ' +
                'rebuilt, so this costs very little.'
              : 'Only the container changes. The picture and sound are copied ' +
                'unchanged.'));
            actions(row).appendChild(button('Convert and play', 'primary',
              function (btn) {
                btn.disabled = true;
                btn.textContent = 'Converting…';
                playConverted(video, player, info.subtitle_tracks);
              }));
            blockers.appendChild(row);
          } else if (info.convert_why_not) {
            blockers.appendChild(el('div', 'item', info.convert_why_not));
          }
        } else {
          if (serverSaysNo) {
            /* Said out loud rather than silently overridden. If it then fails,
             * the viewer knows why it was attempted. */
            blockers.appendChild(el('div', 'item',
              'This server did not expect your browser to play this file, but ' +
              'your browser says it can. Trying anyway.'));
          }
          (info.caveats || []).forEach(function (c) {
            blockers.appendChild(el('div', 'item', c));
          });
          video.src = '/api/v1/files/' + watchState.fileId + '/stream';
          attachSubtitles(video, watchState.fileId, info.subtitle_tracks);
          player.hidden = false;
          lastSaved = -1;

          /* Resume, once the element knows how long the film is. Setting
           * currentTime before metadata has loaded is silently ignored, which
           * is the shape of bug that makes resume look like it works locally
           * and not on a slow connection. */
          if (info.resume_ms > 0) {
            var at = info.resume_ms / 1000;
            video.addEventListener('loadedmetadata', function once() {
              video.removeEventListener('loadedmetadata', once);
              if (isFinite(video.duration) && at < video.duration) {
                video.currentTime = at;
              }
            });
            blockers.appendChild(el('div', 'item',
              'Resuming from ' + clock(at) + '. ' +
              'Use the scrubber to start from the beginning.'));
          } else if (info.finished) {
            blockers.appendChild(el('div', 'item',
              'You finished this before, so it starts from the beginning.'));
          }
        }

        detail.hidden = false;
        detail.appendChild(el('h3', null, 'What is in this file'));
        detail.appendChild(el('div', 'muted small', trackSummary(info)));
        detail.appendChild(facts([
          ['Container', info.container],
          ['Duration', info.duration_ms ? duration(Math.round(info.duration_ms / 1000)) : ''],
          ['Size', bytes(info.size_bytes)],
          ['Subtitles', subtitleCount(info)],
          /* Whether the parser ran in the jail. An operator auditing later
           * deserves to know which guarantee this answer was taken under. */
          ['Parsed in the sandbox', info.sandboxed ? 'yes' : 'NO']
        ]));
        subtitleDetail(detail, info.subtitle_tracks);
      });
  };

  /* Which audio codec to ask the conversion for.
   *
   * Asked of the browser rather than assumed, because this genuinely differs
   * between builds of the SAME browser: a Chromium compiled without the
   * proprietary codecs has Opus and no AAC at all, which is what this
   * project's own browser tests run against. Sending everybody AAC would work
   * everywhere it is shipped and produce silence everywhere it is not — the
   * exact failure the conversion exists to fix.
   *
   * AAC first, because it is the one every browser that has it prefers and the
   * only one Safari will take. */
  function conversionAudio() {
    if (browserCan('audio/mp4; codecs="mp4a.40.2"')) { return 'aac'; }
    if (browserCan('audio/mp4; codecs="opus"')) { return 'opus'; }
    /* Neither answered. canPlayType is advisory and pessimistic (ADR-0020), so
     * the honest move is to try the universal one rather than refuse on the
     * strength of a method that guesses. */
    return 'aac';
  }

  function playConverted(video, player, subtitles) {
    lastSaved = -1;
    video.src = '/api/v1/files/' + watchState.fileId +
      '/convert?audio=' + encodeURIComponent(conversionAudio());
    /* The conversion carries video and one audio track and no subtitles, so
     * these matter MORE here than on a direct play: without them a converted
     * foreign-language film has lost its subtitles entirely. */
    attachSubtitles(video, watchState.fileId, subtitles);
    player.hidden = false;
    /* The converted stream is produced as it is watched, so there is no file
     * to seek within. Said before somebody drags the scrubber and finds out. */
    $('watch-summary').textContent =
      'Converting as it plays. Seeking is not available in a converted stream.';
  }

  /* Whether this browser contradicts the server's refusal. Only the codec
   * blockers can be argued with — a 10-bit or HDR refusal is about what the
   * browser can DISPLAY, and canPlayType does not answer that. */
  function askTheBrowser(info) {
    var codecBlockers = (info.blockers || []).filter(function (b) {
      return b.code === 'video-codec' || b.code === 'container' ||
        b.code === 'audio-codec';
    });
    if (codecBlockers.length !== (info.blockers || []).length) { return false; }
    if (!codecBlockers.length) { return false; }

    var v = (info.video || [])[0];
    if (!v) { return false; }
    var mime = /matroska/.test(info.container || '') ? 'video/x-matroska'
      : (/webm/.test(info.container || '') ? 'video/webm' : 'video/mp4');
    var codec = { h264: 'avc1.640028', hevc: 'hvc1', vp9: 'vp09.00.10.08',
      av1: 'av01.0.04M.08', vp8: 'vp8' }[v.codec];
    if (!codec) { return false; }
    return browserCan(mime + '; codecs="' + codec + '"');
  }

  /* Saving a position.
   *
   * Two triggers, and the second is the one that matters. A throttle every ten
   * seconds keeps the stored position roughly current; what actually decides
   * whether resume works is the write as the tab closes, because that is the
   * moment a viewer stops watching.
   *
   * fetch with keepalive, not navigator.sendBeacon. sendBeacon is the usual
   * answer and it cannot set headers, so it cannot carry the CSRF token this
   * application requires on every mutating request — the write would be
   * refused, silently, exactly when it mattered. keepalive gives the same
   * survives-the-unload behaviour with headers intact. */
  function savePosition(video, opts) {
    if (!watchState.fileId || !isFinite(video.duration) || video.duration <= 0) {
      return;
    }
    var ms = Math.round(video.currentTime * 1000);
    if (!(opts && opts.force) && Math.abs(ms - lastSaved) < 5000) { return; }
    lastSaved = ms;

    var body = JSON.stringify({
      position_ms: ms,
      duration_ms: Math.round(video.duration * 1000)
    });
    var path = '/api/v1/files/' + watchState.fileId + '/position';

    if (opts && opts.unloading) {
      /* Not through api(): that returns a promise nobody will be alive to
       * await, and the unload path needs the request handed to the browser
       * rather than tracked. */
      try {
        fetch(path, {
          method: 'PUT',
          credentials: 'same-origin',
          keepalive: true,
          headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': cookie(CSRF_COOKIE)
          },
          body: body
        });
      } catch (e) { /* the tab is going away; there is nobody to tell */ }
      return;
    }
    api('PUT', path, JSON.parse(body));
  }

  function wireWatch() {
    var video = $('watch-video');

    /* Every ten seconds of playback, and on the events that mean somebody
     * stopped: pausing, seeking, and reaching the end. */
    video.addEventListener('timeupdate', function () {
      if (!video.paused) { savePosition(video); }
    });
    video.addEventListener('pause', function () { savePosition(video, { force: true }); });
    video.addEventListener('seeked', function () { savePosition(video, { force: true }); });
    video.addEventListener('ended', function () { savePosition(video, { force: true }); });

    /* The unload path. pagehide rather than beforeunload or unload: it is the
     * one that fires on mobile Safari and on a backgrounded tab being
     * discarded, which are precisely the cases where a viewer never comes back
     * to the page to trigger anything else. visibilitychange catches the tab
     * being switched away from, which is the same thing happening more slowly. */
    window.addEventListener('pagehide', function () {
      savePosition(video, { force: true, unloading: true });
    });
    document.addEventListener('visibilitychange', function () {
      if (document.visibilityState === 'hidden') {
        savePosition(video, { force: true, unloading: true });
      }
    });
    /* A <video> that fails reports almost nothing useful. Turning its numeric
     * code into a sentence is the difference between "it does not work" and
     * something a person can act on. */
    video.addEventListener('error', function () {
      var e = video.error;
      var why = 'this browser could not play the file';
      if (e) {
        if (e.code === 3) { why = 'this browser could not decode the file'; }
        else if (e.code === 4) { why = 'this browser refused the file\u2019s format'; }
        else if (e.code === 2) { why = 'the connection to the server dropped'; }
        else if (e.code === 1) { why = 'playback was stopped'; }
      }
      refuse(why + '. The file details below say what is in it.');
    });
  }

  // -------------------------------------------------------------------------
  // identify
  // -------------------------------------------------------------------------

  /* The review screen. Its whole job is to let somebody answer one question —
   * "is this the same film?" — so it shows the posters and the summaries rather
   * than the scores. The score orders the list; it is not an argument.
   *
   * Posters come from THIS instance (/api/v1/artwork/poster/...), never from
   * the provider: a page of provider URLs would make the operator's own browser
   * announce their library to a third party, once per candidate (ADR-0018). The
   * server fetches each one on first view, from the path it stored when the
   * search was made. */
  function renderCandidateCard(itemID, c, isTop) {
    var card = el('div', 'candidate' + (isTop ? ' candidate-top' : ''));

    var art = el('div', 'poster');
    if (c.poster) {
      var img = el('img');
      img.src = c.poster;
      img.alt = '';
      img.loading = 'lazy';
      /* A missing picture must not leave a broken-image icon in a screen whose
       * job is to be looked at. */
      img.addEventListener('error', function () {
        clear(art);
        art.appendChild(el('div', 'poster-none', 'no image'));
      });
      art.appendChild(img);
    } else {
      art.appendChild(el('div', 'poster-none', 'no image'));
    }
    card.appendChild(art);

    var body = el('div', 'candidate-body');
    var heading = c.title + (c.year ? ' (' + c.year + ')' : '');
    body.appendChild(el('div', 'title', heading));
    if (c.original_title) {
      body.appendChild(el('div', 'muted small', 'also known as ' + c.original_title));
    }
    /* Why this one is a candidate, in the words the server chose. The number
     * behind it is for ordering. */
    if (c.why) { body.appendChild(el('div', 'why', c.why)); }
    if (c.overview) { body.appendChild(el('div', 'overview', c.overview)); }

    var a = el('div', 'actions');
    a.appendChild(button('This one', 'primary', function (btn) {
      btn.disabled = true;
      api('POST', '/api/v1/identify/' + itemID + '/confirm',
        { provider_id: c.provider_id }).then(function (res) {
          if (res.status !== 200) {
            btn.disabled = false;
            return fail(res, 'could not confirm that');
          }
          ok((res.body && res.body.note) || ('Identified as ' + c.title + '.'));
          loaders.identify();
        });
    }));
    body.appendChild(a);

    card.appendChild(body);
    return card;
  }

  function renderIdentification(p) {
    var known = p.parsed_title + (p.parsed_year ? ' (' + p.parsed_year + ')' : '');
    var r = row(known, 'in your library as this');

    /* The verdict in words, first: it says what the software could not decide,
     * which is the context for everything below it. */
    if (p.why) { r.appendChild(el('div', 'why', p.why)); }

    var cands = p.candidates || [];
    if (!cands.length) {
      r.appendChild(el('div', 'empty', 'Nothing was found for this title.'));
    } else {
      var grid = el('div', 'candidates');
      cands.forEach(function (c, i) {
        grid.appendChild(renderCandidateCard(p.item_id, c, i === 0));
      });
      r.appendChild(grid);
    }

    var a = actions(r);
    a.appendChild(button('None of these', 'ghost', function (btn) {
      btn.disabled = true;
      api('POST', '/api/v1/identify/' + p.item_id + '/reject', {}).then(function (res) {
        if (res.status !== 200) { btn.disabled = false; return fail(res, 'could not record that'); }
        ok((res.body && res.body.note) || 'Recorded.');
        loaders.identify();
      });
    }));
    return r;
  }

  loaders.identify = function () {
    var list = $('identify-list');
    api('GET', '/api/v1/identify/pending').then(function (res) {
      if (res.status !== 200 || !res.body) {
        return fail(res, 'could not read what is waiting');
      }
      var items = res.body.items || [];
      $('identify-count').textContent = items.length
        ? items.length + ' item(s) waiting'
        : '';
      if (!items.length) {
        return empty(list, 'Nothing is waiting. Either everything matched, or ' +
          'no pass has run yet — there is a button for that above.');
      }
      clear(list);
      items.forEach(function (p) { list.appendChild(renderIdentification(p)); });
    });
  };

  function wireIdentify() {
    var btn = $('identify-run');
    /* Running a pass spends provider requests across the whole library, so it
     * is an administrative trigger and the button is simply not there for
     * somebody who cannot use it. */
    if (!can('admin.system')) {
      btn.hidden = true;
      return;
    }
    btn.addEventListener('click', function () {
      btn.disabled = true;
      btn.textContent = 'Running…';
      api('POST', '/api/v1/admin/identify/run', {}).then(function (res) {
        btn.disabled = false;
        btn.textContent = 'Run a pass now';
        if (res.status !== 200 || !res.body) {
          return fail(res, 'the pass could not run');
        }
        var b = res.body;
        ok(b.considered + ' examined — ' + b.accepted + ' identified, ' +
          b.proposed + ' need you, ' + b.nothing_found + ' found nothing' +
          (b.failed ? ', ' + b.failed + ' failed' : ''));
        loaders.identify();
      });
    });
  }

  // -------------------------------------------------------------------------
  // search and grab
  // -------------------------------------------------------------------------

  /* The quality profiles, and which one is the default (ADR-0027). Every
   * search is judged by the default unless the person picks another — or
   * none — so every search form offers the same choice, starting on the
   * default. Kept here once rather than fetched per panel. */
  var profileList = { profiles: [], defaultId: 0 };

  function loadProfiles() {
    return api('GET', '/api/v1/quality-profiles').then(function (res) {
      if (res.status === 200 && res.body) {
        profileList = { profiles: res.body.profiles || [], defaultId: res.body.default_id || 0 };
      }
      fillProfileSelect($('search-profile'), profileList.defaultId);
      showSearchDefault();
      return profileList;
    });
  }

  /* fillProfileSelect offers every profile, the default marked, and "no
   * profile", with `chosen` selected. The value is always explicit — a
   * profile's id, or 0 for none — so what is sent is what is shown. */
  function fillProfileSelect(sel, chosen) {
    clear(sel);
    profileList.profiles.forEach(function (p) {
      var o = el('option', null, p.name + (p.id === profileList.defaultId ? ' (default)' : ''));
      o.value = String(p.id);
      sel.appendChild(o);
    });
    var none = el('option', null, 'No profile — judge them yourself');
    none.value = '0';
    sel.appendChild(none);
    sel.value = String(chosen || 0);
    return sel;
  }

  /* Under the Search form: what the default is, and — for an administrator
   * whose chosen profile is not it — the button that makes it so. */
  function showSearchDefault() {
    var box = $('search-default');
    var sel = $('search-profile');
    clear(box);
    var current = profileList.profiles.filter(function (p) { return p.id === profileList.defaultId; })[0];
    box.appendChild(el('span', 'muted small', current
      ? 'Searches are judged by ' + current.name + ' unless you choose otherwise.'
      : 'No default profile is set: searches are unjudged unless you choose one.'));
    if (can('admin.system') && Number(sel.value) !== profileList.defaultId) {
      box.appendChild(button('Make this the default', 'ghost', function (b) {
        b.disabled = true;
        api('PUT', '/api/v1/admin/quality-profiles/default', { profile_id: Number(sel.value) })
          .then(function (res) {
            b.disabled = false;
            if (res.status !== 200) { return fail(res, 'could not change the default'); }
            ok((res.body && res.body.note) || 'Changed.');
            loadProfiles().then(function () { sel.value = String(profileList.defaultId); showSearchDefault(); });
          });
      }));
    }
    box.hidden = false;
  }

  /* askAgain is the form under a targeted search's results: the term and the
   * profile it was asked with, both changeable. A different term changes what
   * the indexers are asked; a different profile changes what is acceptable;
   * neither changes what can MATCH (ADR-0023, ADR-0026). */
  var askAgainForms = 0;
  function askAgain(b, rerun) {
    var form = el('form', 'inline-form ask-again');
    form.setAttribute('autocomplete', 'off');
    var n = ++askAgainForms;
    var term = el('input');
    term.type = 'text';
    term.maxLength = 200;
    term.required = true;
    term.value = b.term || '';
    labelled(form, 'ask-term-' + n, 'Ask for', term);
    /* An album is judged by the format ladder, not a profile (ADR-0046). */
    var judged = (b.album || b.book) ? null : labelled(form, 'ask-profile-' + n, 'Judged by',
      fillProfileSelect(el('select'), b.profile_id || 0));
    var go = el('button', null, 'Search again');
    go.type = 'submit';
    form.appendChild(go);
    submit(form, function () { return rerun(term.value, judged ? Number(judged.value) : undefined); });
    return form;
  }

  /* judgedLine says which profile judged a targeted search's results. */
  function judgedLine(b) {
    if (b.book) {
      return 'ranked by format, EPUB first; a name that does not say its format is refused';
    }
    if (b.album) {
      return 'ranked by format, FLAC first; a name that does not say its format is refused';
    }
    if (b.profile) {
      return 'judged by ' + b.profile + (b.profile_default ? ' (the default)' : '');
    }
    return 'judged by nobody: no profile';
  }

  /* noun is what a targeted search was for — "episode" or "film" — and only
   * words the refusal; the general search has none. */
  function renderCandidate(c, noun) {
    var r = row(c.title, c.indexer);
    r.className = 'item ' + (c.accepted ? 'accepted' : 'rejected');

    r.appendChild(facts([
      ['Quality', c.quality],
      ['Size', bytes(c.size)],
      ['Seeders', c.seeders],
      ['Score', c.accepted ? c.score : undefined],
      ['Seen on', (c.seen_on || []).join(', ')]
    ]));

    if (c.pack && c.seasons) {
      r.appendChild(el('div', 'note', 'Several seasons in one download (' + c.seasons + '). Each file is ' +
        'filed as the season and episode its own name says; one that is not better than what is here is left out.'));
    } else if (c.pack) {
      r.appendChild(el('div', 'note', 'The whole season in one download. Each file is filed ' +
        'as the episode its own name says; one that is not better than what is here is left out.'));
    } else if (c.episode && noun === 'season') {
      r.appendChild(el('div', 'muted small', 'One episode of the season: ' + c.episode));
    }
    if (!c.accepted) {
      /* The whole point of returning refusals: an operator can see why, rather
       * than concluding the release does not exist. */
      r.appendChild(el('div', 'why', c.rejected_because || 'refused by the profile'));
    }

    var parsed = c.parsed || {};
    if (parsed.unmatched && parsed.unmatched.length) {
      r.appendChild(el('div', 'muted small',
        'Not understood in the name: ' + parsed.unmatched.join(', ')));
    }

    var a = actions(r);
    if (c.ticket && can('acquisition.queue')) {
      a.appendChild(button('Grab', 'primary', function (btn) {
        btn.disabled = true;
        api('POST', '/api/v1/releases/grab', { ticket: c.ticket }).then(function (res) {
          if (res.status !== 202) { btn.disabled = false; return fail(res, 'could not grab that'); }
          /* Said, because it is the difference that matters: a grab FOR an
           * episode or a film will be filed under it whatever the release
           * calls itself. */
          var target = res.body && res.body['for'];
          ok('Queued' + (target ? ' for ' + (target.label || target.code) : '') + ': ' + c.title);
          btn.textContent = 'Queued';
        });
      }));
    } else if (!c.accepted) {
      /* A targeted search refuses for two different reasons, and the fix for
       * each is different: another release, or another profile. */
      var none = 'No grab button: the profile refused this release.';
      if (c.rejection_reason === 'unknown_format') {
        none = 'No grab button: the name does not say what format it is.';
      } else if (c.matches === false) {
        none = 'No grab button: this is not the ' + (noun || 'episode') + '.';
      }
      a.appendChild(el('span', 'muted small', none));
    }
    return r;
  }

  /* A targeted search's panel, shared by the episode and the film search: it
   * opens under the row that asked, says how many indexers answered, which
   * profile judged the results and how many ARE what was searched for, and
   * offers the term and the profile back to be changed (ADR-0027). Only a
   * release that is the episode or film can be grabbed, and a grab from here is
   * filed under it whatever the release calls itself — the server decided
   * both, and the grab ticket carries the decision. */
  function targetedSearch(kind, path, label, after, ask) {
    var cls = kind + '-search';
    var old = after.nextSibling;
    if (old && old.className && old.className.indexOf(cls) !== -1) {
      old.parentNode.removeChild(old);
    }
    var panel = el('div', 'list compact ' + cls);
    after.parentNode.insertBefore(panel, after.nextSibling);
    empty(panel, 'Asking every enabled indexer for ' + label + '\u2026');
    var body = {};
    if (ask && ask.term) { body.term = ask.term; }
    /* Absent means the default; a choice made in the panel is sent as made. */
    if (ask && ask.profile !== undefined && kind !== 'album' && kind !== 'book') { body.profile_id = ask.profile; }
    return api('POST', path, body).then(function (res) {
      clear(panel);
      if (res.status !== 200 || !res.body) {
        panel.appendChild(el('div', 'why', failure(res, 'the search failed')));
        return;
      }
      var b = res.body;
      var what = kind === 'episode' && b.episode ? b.episode.code : label;
      if (kind === 'season' && b.season) {
        what = b.season.code + ' or one of its episodes';
      }
      if (b.warning) { panel.appendChild(el('p', 'warn', b.warning)); }
      if (b.titles_note) { panel.appendChild(el('div', 'note', b.titles_note)); }
      if (b.profile_note) { panel.appendChild(el('div', 'note', b.profile_note)); }
      if (b.have_note) { panel.appendChild(el('div', 'note', b.have_note)); }
      var silent = (b.indexers || []).filter(function (o) { return o.error; });
      panel.appendChild(el('div', 'muted small',
        (b.queried || 0) + ' indexer(s) asked for \u201c' + (b.term || '') + '\u201d' +
        (silent.length ? ', ' + silent.length + ' did not answer' : '') +
        ' \u00b7 ' + judgedLine(b) +
        ' \u00b7 ' + (b.matches || 0) + ' of ' + (b.count || 0) + ' result(s) are ' + what));
      silent.forEach(function (o) { panel.appendChild(el('div', 'why', o.indexer + ': ' + o.error)); });
      if (b.note) { panel.appendChild(el('div', 'note', b.note)); }
      panel.appendChild(askAgain(b, function (term, profile) {
        return targetedSearch(kind, path, label, after, { term: term, profile: profile });
      }));
      var list = b.candidates || [];
      if (!list.length) {
        panel.appendChild(el('div', 'empty', 'No indexer returned anything for that.'));
        return;
      }
      list.forEach(function (c) { panel.appendChild(renderCandidate(c, kind)); });
    });
  }

  /* Searching for one episode (ADR-0023). */
  function searchForEpisode(episodeID, label, after) {
    return targetedSearch('episode', '/api/v1/episodes/' + episodeID + '/search', label, after);
  }

  /* Searching for one season (ADR-0033): its packs and its episodes. */
  function searchForSeason(itemID, season, label, after) {
    return targetedSearch('season', '/api/v1/media/' + itemID + '/seasons/' + season + '/search',
      label, after);
  }

  /* kindWord is what a library row calls its title: an artist is not a film. */
  function kindWord(kind) {
    return { series: 'series', artist: 'artist', book: 'book' }[kind] || 'film';
  }

  /* subtitleButtons offers one "Subtitles (en)" button per wanted language. */
  var subtitleLanguages = null;
  function subtitleButtons(acts, fileID) {
    var get = subtitleLanguages ? Promise.resolve(subtitleLanguages) :
      api('GET', '/api/v1/subtitles/languages').then(function (res) {
        subtitleLanguages = (res.status === 200 && res.body && res.body.languages) || [];
        return subtitleLanguages;
      });
    get.then(function (langs) {
      langs.forEach(function (lang) {
        acts.appendChild(button('Subtitles (' + lang + ')', 'ghost', function (btn) {
          btn.disabled = true;
          btn.textContent = 'Asking OpenSubtitles…';
          api('POST', '/api/v1/files/' + fileID + '/subtitles/fetch', { language: lang }).then(function (res) {
            btn.textContent = 'Subtitles (' + lang + ')';
            if (res.status !== 200) { btn.disabled = false; return fail(res, 'no subtitle was fetched'); }
            ok((res.body && res.body.note) || 'Fetched.');
            btn.textContent = 'Subtitles (' + lang + ') ✓';
          });
        }));
      });
    });
  }

  /* Searching for one book (ADR-0049): its author and title, ranked by
   * format. A grab from here is filed as the book's one file. */
  function searchForBook(itemID, label, after) {
    return targetedSearch('book', '/api/v1/media/' + itemID + '/search', label, after);
  }

  /* Searching for one album (ADR-0046): the artist and the album, ranked by
   * format. A grab from here is filed track by track into that album. */
  function searchForAlbum(albumID, label, after) {
    return targetedSearch('album', '/api/v1/albums/' + albumID + '/search', label, after);
  }

  /* Searching for one film (ADR-0026). The default asks for the title and the
   * year; a release named a year out is found by asking again without it. */
  function searchForFilm(itemID, label, after) {
    return targetedSearch('film', '/api/v1/media/' + itemID + '/search', label, after);
  }

  /* Search has no data of its own to load, but it still needs a loader:
   * without one, leaving the tab and coming back shows the PREVIOUS search's
   * results as though they were current. That is the same class of mistake as a
   * stale success banner — the screen says something true a minute ago and
   * false now — and it is worse here, because the results carry Grab buttons
   * whose tickets have been expiring the whole time. */
  loaders.search = function () {
    empty($('search-results'), 'Search for something to see what the indexers have.');
    clear($('search-indexers'));
    $('search-warning').hidden = true;
  };

  function wireSearch() {
    $('search-profile').addEventListener('change', showSearchDefault);
    submit($('search-form'), function () {
      var results = $('search-results');
      var indexers = $('search-indexers');
      /* Always explicit: the select shows what will judge the results, and
       * that is exactly what is sent — a profile's id, or 0 for none. */
      var body = { term: $('search-term').value, profile_id: Number($('search-profile').value || 0) };

      empty(results, 'Asking every enabled indexer…');
      clear(indexers);
      $('search-warning').hidden = true;

      return api('POST', '/api/v1/releases/search', body).then(function (res) {
        if (res.status !== 200 || !res.body) {
          empty(results, '');
          return fail(res, 'the search failed');
        }
        var b = res.body;

        /* Partial results that LOOK complete are worse than an error: an
         * operator who is not told three indexers timed out concludes the
         * release does not exist. */
        if (b.warning) {
          $('search-warning').textContent = b.warning;
          $('search-warning').hidden = false;
        }

        clear(indexers);
        (b.indexers || []).forEach(function (o) {
          var r = row(o.indexer, o.error ? 'did not answer' : o.results + ' result(s)');
          if (o.error) { r.className = 'item rejected'; r.appendChild(el('div', 'why', o.error)); }
          indexers.appendChild(r);
        });

        var list = b.candidates || [];
        if (!list.length) {
          empty(results, 'No indexer returned anything for that.');
          return;
        }
        clear(results);
        list.forEach(function (c) { results.appendChild(renderCandidate(c)); });
      });
    });
  }

  // -------------------------------------------------------------------------
  // queue
  // -------------------------------------------------------------------------

  loaders.queue = function () {
    api('GET', '/api/v1/queue').then(function (res) {
      var list = $('queue-list');
      var notes = $('queue-notes');
      clear(notes);

      if (res.status === 501) {
        return empty(list, (res.body && res.body.error) || 'The download engine is not running.');
      }
      if (res.status !== 200 || !res.body) { return fail(res, 'could not read the queue'); }

      /* The engine's notes belong beside the queue, not in a log: "DHT is off,
       * so magnets may never resolve" explains the stuck magnet above it. */
      (res.body.notes || []).forEach(function (n) {
        notes.appendChild(el('div', 'note', n));
      });
      /* Why a download sits at zero (ADR-0066): the library drops a failed
       * peer connection silently, so the engine's count is the only witness. */
      var c = res.body.connections;
      if (c && c.attempted > 0) {
        notes.appendChild(el('div', 'note' + (c.failed === c.attempted ? ' bad' : ''),
          'Peer connections: ' + c.attempted + ' tried, ' + c.failed + ' failed' +
          (c.last_error ? ' — last failure: ' + c.last_error + (c.last_error_at ? ' (' + when(c.last_error_at) + ')' : '') : '')));
      }

      var items = res.body.items || [];
      if (!items.length) { return empty(list, 'Nothing is downloading.'); }

      clear(list);
      items.forEach(function (t) {
        var r = row(t.title || t.name || t.info_hash, t.indexer);
        r.className = 'item ' + (t.status === 'unrecorded' ? 'rejected' : '');

        if (t.have_metadata) {
          r.appendChild(progress(t.percent));
        } else if (t.status === 'complete' || t.status === 'stopped') {
          /* A row the engine is no longer running has no live progress to
           * show. The magnet sentence below used to be shown for it too, and
           * said a finished download was waiting for metadata. */
          r.appendChild(el('div', 'muted small', t.status === 'complete'
            ? 'Finished, and no longer running here.'
            : 'Stopped. Its files, if any, are where they were.'));
        } else {
          r.appendChild(el('div', 'muted small',
            'Waiting for metadata — a magnet has no size or progress until peers supply it.'));
        }

        r.appendChild(facts([
          ['Status', t.status],
          ['Done', t.have_metadata ? bytes(t.completed) + ' of ' + bytes(t.bytes) : undefined],
          ['Peers', t.peers],
          ['Seeders', t.seeders],
          /* No person pressed Grab for an automatic one (ADR-0030); said in
           * words, not as the task's name. */
          ['Added by', t.automatic ? 'automatic acquisition' : t.added_by],
          ['Added', when(t.added_at)],
          /* An episode or film grab is filed under its item whatever the
           * release calls itself, so the queue says which that is, by name. */
          ['For', t['for'] ? (t['for'].label || t['for'].code) : undefined]
        ]));

        /* A download that stopped moving (ADR-0034): automatic acquisition's
         * was given up and what it was for is wanted again; a person's is
         * left to them, so the sentence says what removing it would do. */
        if (t.stalled) {
          var quiet = 'No progress since ' + when(t.stalled.since) + '. ';
          r.appendChild(el('div', 'why', t.stalled.given_up
            ? quiet + 'Given up: it is never grabbed again, and what it was for is searched for afresh.'
            : quiet + 'It is still running. Remove it to let another release be found.'));
        }

        if (t.status === 'unrecorded') {
          r.appendChild(el('div', 'why',
            'This transfer is running but was never written to the queue, so it ' +
            'will disappear on the next restart with its files still on disk.'));
        }

        var a = actions(r);
        a.appendChild(button('History', 'ghost', function (btn) {
          btn.disabled = true;
          api('GET', '/api/v1/queue/' + t.info_hash + '/history').then(function (h) {
            btn.disabled = false;
            if (h.status !== 200 || !h.body) { return fail(h, 'could not read the history'); }
            var box = el('div', 'history');
            if (!(h.body.history || []).length) {
              box.appendChild(el('div', 'muted small', h.body.note || 'Nothing yet.'));
            }
            (h.body.history || []).forEach(function (rec) {
              var line = el('div', 'history-line');
              line.appendChild(el('span', 'badge ' + rec.outcome, rec.outcome));
              line.appendChild(el('span', null, rec.detail));
              box.appendChild(line);
            });
            r.appendChild(box);
          });
        }));
        a.appendChild(button('Remove', 'danger', function (btn) {
          btn.disabled = true;
          api('POST', '/api/v1/queue/' + t.info_hash + '/remove').then(function (rm) {
            if (rm.status !== 200) { btn.disabled = false; return fail(rm, 'could not remove'); }
            ok((rm.body && rm.body.note) || 'Stopped.');
            loaders.queue();
          });
        }));
        list.appendChild(r);
      });
    });
  };

  // -------------------------------------------------------------------------
  // storage: root folders and the trash
  // -------------------------------------------------------------------------

  loaders.storage = function () {
    loadRoots();
    loadTrash();
  };

  function loadRoots() {
    var list = $('roots-list');
    api('GET', '/api/v1/admin/rootfolders').then(function (res) {
      if (res.status !== 200 || !res.body) { return fail(res, 'could not read the root folders'); }
      var roots = res.body.root_folders || [];
      if (!roots.length) {
        return empty(list, 'No root folders yet. Nothing can be imported until one exists.');
      }
      clear(list);
      roots.forEach(function (rf) {
        var r = row(rf.label || rf.path, rf.kind);
        r.appendChild(el('div', 'muted small', rf.path));
        r.appendChild(facts([
          ['Free', bytes(rf.free_bytes)],
          ['Hardlinks', rf.hardlinks ? 'yes' : 'NO — imports will copy']
        ]));
        /* Said in words, not just a boolean: "hardlinks: false" means every
         * import silently uses twice the disk an operator budgeted for. */
        if (!rf.hardlinks) { r.appendChild(el('div', 'why', rf.hardlink_note)); }

        var a = actions(r);
        a.appendChild(button('Scan', 'ghost', function (btn) {
          btn.disabled = true;
          btn.textContent = 'Scanning…';
          api('POST', '/api/v1/admin/rootfolders/' + rf.id + '/scan').then(function (s) {
            btn.disabled = false;
            btn.textContent = 'Scan';
            if (s.status === 409) {
              /* Not an error to shrug at. Most of a library vanishing at once
               * is almost always an unmounted disk. */
              return fail(s, 'the scan was refused');
            }
            if (s.status !== 200 || !s.body) { return fail(s, 'the scan failed'); }
            ok(s.body.summary);
            loadRoots();
          });
        }));
        a.appendChild(button('Re-check', 'ghost', function (btn) {
          btn.disabled = true;
          api('POST', '/api/v1/admin/rootfolders/' + rf.id + '/refresh').then(function () {
            loadRoots();
          });
        }));
        a.appendChild(button('Remove', 'danger', function (btn) {
          btn.disabled = true;
          api('DELETE', '/api/v1/admin/rootfolders/' + rf.id).then(function (d) {
            if (d.status !== 200) { btn.disabled = false; return fail(d, 'could not remove'); }
            ok((d.body && d.body.note) || 'Removed from the configuration.');
            loadRoots();
          });
        }));
        list.appendChild(r);
      });
    });
  }

  function loadTrash() {
    var list = $('trash-list');
    if (!can('library.delete')) {
      return empty(list, 'Your role cannot see the trash.');
    }
    api('GET', '/api/v1/admin/trash').then(function (res) {
      if (res.status !== 200 || !res.body) { return empty(list, 'The trash is empty.'); }
      var items = res.body.items || [];
      if (!items.length) { return empty(list, 'The trash is empty.'); }

      clear(list);
      list.appendChild(el('div', 'note',
        items.length + ' file(s), ' + bytes(res.body.bytes) +
        ' — kept for ' + (duration(res.body.retention_seconds) || res.body.retention) +
        ' after deletion.'));
      items.forEach(function (t) {
        var r = row(t.name, bytes(t.bytes));
        r.appendChild(facts([
          ['Trashed', when(t.trashed_at)],
          ['Unrecoverable after', when(t.purge_after)]
        ]));
        var tacts = actions(r);
        tacts.appendChild(button('Put back', 'ghost', function (btn) {
          btn.disabled = true;
          api('POST', '/api/v1/admin/trash/restore', { root_id: t.root_id, path: t.path })
            .then(function (rs) {
              if (rs.status !== 200) { btn.disabled = false; return fail(rs, 'could not restore'); }
              ok((rs.body && rs.body.note) || 'Restored.');
              loaders.storage();
            });
        }));
        /* The one button here that cannot be undone (ADR-0053): it asks
         * again, on itself, and forgets the question after five seconds. */
        var armed = null;
        tacts.appendChild(button('Purge now', 'danger', function (btn) {
          if (!armed) {
            btn.textContent = 'Click again to unlink for good';
            armed = setTimeout(function () { armed = null; btn.textContent = 'Purge now'; }, 5000);
            return;
          }
          clearTimeout(armed);
          armed = null;
          btn.disabled = true;
          api('POST', '/api/v1/admin/trash/purge', { root_id: t.root_id, path: t.path })
            .then(function (rs) {
              if (rs.status !== 200) { btn.disabled = false; btn.textContent = 'Purge now'; return fail(rs, 'could not purge'); }
              ok('Unlinked ' + t.name + ', ' + bytes(rs.body && rs.body.bytes) + ' freed.');
              loaders.storage();
            });
        }));
        list.appendChild(r);
      });
    });
  }

  function wireRootForm() {
    submit($('root-form'), function () {
      return api('POST', '/api/v1/admin/rootfolders', {
        path: $('root-path').value,
        kind: $('root-kind').value,
        label: $('root-label').value
      }).then(function (res) {
        if (res.status !== 201) { return fail(res, 'could not add that root folder'); }
        /* The hardlink warning arrives at the moment the decision is made,
         * where it can still be changed. */
        ok(res.body && res.body.warning ? res.body.warning : 'Added.');
        $('root-path').value = '';
        $('root-label').value = '';
        loadRoots();
      });
    });
  }

  // -------------------------------------------------------------------------
  // indexers
  // -------------------------------------------------------------------------

  loaders.indexers = function () {
    var list = $('indexers-list');
    api('GET', '/api/v1/admin/indexers').then(function (res) {
      if (res.status !== 200 || !res.body) { return fail(res, 'could not read the indexers'); }
      var rows = res.body.indexers || [];
      if (!rows.length) {
        return empty(list, 'No indexers yet. A search has nothing to ask until one exists.');
      }
      clear(list);
      rows.forEach(function (ix) {
        var r = row(ix.name, ix.enabled ? ix.kind : ix.kind + ' · disabled');
        r.className = 'item ' + (ix.enabled ? '' : 'rejected');
        r.appendChild(el('div', 'muted small', ix.base_url));
        r.appendChild(facts([
          ['Signs in', ix.has_settings ? 'with saved settings' : undefined],
          ['Seed ratio', ix.seed_ratio || undefined],
          ['Seed hours', ix.seed_hours || undefined],
          ['Last error', ix.last_error]
        ]));
        actions(r).appendChild(button('Remove', 'danger', function (btn) {
          btn.disabled = true;
          api('DELETE', '/api/v1/admin/indexers/' + ix.id).then(function (d) {
            if (d.status !== 200 && d.status !== 204) {
              btn.disabled = false;
              return fail(d, 'could not remove');
            }
            ok('Removed.');
            loaders.indexers();
          });
        }));
        list.appendChild(r);
      });
    });
  };

  /* The values a signing-in tracker's definition asks for (ADR-0059): only
   * those filled in, so a public tracker sends none. */
  function indexerSettings() {
    var out = {};
    if ($('indexer-kind').value !== 'cardigann') { return out; }
    [['username', 'indexer-username'], ['password', 'indexer-password'], ['cookie', 'indexer-cookie']]
      .forEach(function (p) { if ($(p[1]).value) { out[p[0]] = $(p[1]).value; } });
    return out;
  }

  function wireIndexerForm() {
    /* A Cardigann tracker is read from its pasted definition (ADR-0058). */
    $('indexer-kind').addEventListener('change', function () {
      var on = this.value === 'cardigann';
      document.querySelectorAll('#indexer-form .cardigann-only').forEach(function (n) { n.hidden = !on; });
    });
    submit($('indexer-form'), function () {
      return api('POST', '/api/v1/admin/indexers', {
        name: $('indexer-name').value,
        base_url: $('indexer-url').value,
        kind: $('indexer-kind').value,
        api_key: $('indexer-key').value,
        enabled: true,
        seed_ratio: Number($('indexer-ratio').value) || 0,
        seed_hours: Number($('indexer-hours').value) || 0,
        definition: $('indexer-kind').value === 'cardigann' ? $('indexer-definition').value : '',
        settings: indexerSettings()
      }).then(function (res) {
        if (res.status !== 201) { return fail(res, 'could not add that indexer'); }
        ok('Added.');
        $('indexer-name').value = '';
        $('indexer-url').value = '';
        /* Cleared immediately: a key sitting in a form field is one screenshot
         * or one shoulder away from being disclosed. */
        $('indexer-key').value = '';
        $('indexer-definition').value = '';
        ['indexer-username', 'indexer-password', 'indexer-cookie'].forEach(function (id) { $(id).value = ''; });
        loaders.indexers();
      });
    });
  }

  // -------------------------------------------------------------------------
  // metadata provider
  // -------------------------------------------------------------------------

  /* health(h) -> one line: working or not, in words, and when it was checked. */
  function healthLine(h) {
    if (!h) { return ''; }
    return (h.ok ? 'Working. ' : 'Not working. ') + (h.detail || '') +
      (h.checked_at ? ' (checked ' + when(h.checked_at) + ')' : '');
  }

  loaders.metadata = function () {
    loadSubtitleSettings();
    var box = $('metadata-status');
    var actions = $('metadata-actions');
    empty(box, 'Loading…');
    clear(actions);
    api('GET', '/api/v1/admin/metadata').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(box, failure(res, 'could not load the provider')); return; }
      var b = res.body;
      clear(box);
      var item = el('div', 'item');
      var title = el('div', 'title', b.configured ? (b.provider || 'provider').toUpperCase() : 'No provider');
      if (b.configured && !b.checked) {
        /* Loaded at startup and not asked anything yet: neither working nor
         * broken, and saying "not working" would send someone to replace a
         * key that is fine. */
        title.appendChild(el('span', 'badge', 'not checked yet'));
      } else if (b.configured) {
        title.appendChild(el('span', 'badge ' + (b.health && b.health.ok ? 'good' : 'bad'),
          b.health && b.health.ok ? 'working' : 'not working'));
      }
      item.appendChild(title);
      item.appendChild(el('div', 'meta', b.configured && b.checked ? healthLine(b.health) :
        (b.health && b.health.detail) || ''));
      box.appendChild(item);
      if (!b.configured) { return; }

      actions.appendChild(button('Check now', 'ghost', function (btn) {
        btn.disabled = true;
        api('POST', '/api/v1/admin/metadata/check').then(function (r) {
          btn.disabled = false;
          if (r.status !== 200 || !r.body) { fail(r, 'could not check the provider'); return; }
          if (r.body.health && r.body.health.ok) { ok(healthLine(r.body.health)); }
          else { refuse(healthLine(r.body.health)); }
          loaders.metadata();
        });
      }));
      /* Removing is reversible — paste the key again — so it asks nothing. */
      actions.appendChild(button('Remove the key', 'ghost', function (btn) {
        btn.disabled = true;
        api('PUT', '/api/v1/admin/metadata/token', { token: '' }).then(function (r) {
          btn.disabled = false;
          if (r.status !== 200) { fail(r, 'could not remove the key'); return; }
          ok('Removed. Titles are identified from release names alone until a key is set.');
          loaders.metadata();
        });
      }));
    });
  };

  /* OpenSubtitles (ADR-0055): whether a key, an account and which languages —
   * never the secrets, which the API does not return. */
  function loadSubtitleSettings() {
    var box = $('subtitles-status');
    if (!box) { return; }
    api('GET', '/api/v1/admin/subtitles').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(box, failure(res, 'could not read the subtitle settings')); return; }
      var b = res.body;
      clear(box);
      box.appendChild(row(b.configured ? 'OpenSubtitles: a key is set' : 'OpenSubtitles: no key',
        (b.username ? 'signed in as ' + b.username + (b.has_password ? '' : ' (no password set)') : 'no account') +
        ' · languages: ' + ((b.languages || []).join(', ') || 'none')));
      $('subtitles-user').value = b.username || '';
      $('subtitles-languages').value = (b.languages || []).join(', ');
    });
  }

  function wireSubtitleForm() {
    var form = $('subtitles-form');
    if (!form) { return; }
    submit(form, function () {
      var body = { username: $('subtitles-user').value,
        languages: $('subtitles-languages').value.split(',').map(function (l) { return l.trim(); })
          .filter(function (l) { return l; }) };
      /* A blank secret field keeps what is stored; the fields are cleared at
       * once, as the metadata key's is. */
      if ($('subtitles-key').value) { body.api_key = $('subtitles-key').value; }
      if ($('subtitles-password').value) { body.password = $('subtitles-password').value; }
      $('subtitles-key').value = '';
      $('subtitles-password').value = '';
      return api('PUT', '/api/v1/admin/subtitles', body).then(function (res) {
        if (res.status !== 200) { return fail(res, 'could not save the subtitle settings'); }
        ok('Saved.');
        loadSubtitleSettings();
      });
    });
  }

  function wireMetadataForm() {
    wireSubtitleForm();
    var form = $('metadata-form');
    if (!form) { return; }
    submit(form, function () {
      var field = $('metadata-token');
      var token = field.value;
      /* Cleared before the answer arrives, whatever it is: a key in a form
       * field is a screenshot away from being disclosed, and a rejected one is
       * pasted again rather than edited. */
      field.value = '';
      return api('PUT', '/api/v1/admin/metadata/token', { token: token }).then(function (res) {
        if (res.status === 200 && res.body) {
          ok(res.body.note || 'Stored and verified.');
        } else if (res.status === 400 && res.body) {
          refuse((res.body.error || 'The key was refused') + '. ' + (res.body.note || ''));
        } else {
          fail(res, 'the key could not be checked, so it was not stored');
        }
        loaders.metadata();
      });
    });
  }

  // -------------------------------------------------------------------------
  // notifications (ADR-0032)
  // -------------------------------------------------------------------------

  /* The webhook, whether delivery works, and what is sent. The link itself is
   * never here: the API does not return it, in any form. */
  loaders.notifications = function () {
    var box = $('notifications-status');
    var actions = $('notifications-actions');
    var cats = $('notifications-category-list');
    empty(box, 'Loading…');
    clear(actions);
    clear(cats);
    api('GET', '/api/v1/admin/notifications').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(box, failure(res, 'could not load notifications')); return; }
      var b = res.body;
      var d = b.delivery || {};
      clear(box);
      var item = el('div', 'item');
      var title = el('div', 'title', b.configured ? 'Discord' : 'No webhook');
      if (b.configured) {
        /* A wait Discord asked for is not a failure, though it is said as an
         * error too: it comes first. */
        var state = d.stopped ? ['bad', 'not sending'] : d.waiting_until ? ['warn', 'waiting'] :
          d.error ? ['bad', 'failing'] : ['good', 'set'];
        title.appendChild(el('span', 'badge ' + state[0], state[1]));
      }
      item.appendChild(title);
      if (!b.configured) {
        item.appendChild(el('div', 'meta', 'Nothing is sent. Paste a webhook link below to start.'));
      } else {
        var wh = b.webhook || {};
        item.appendChild(el('div', 'meta', 'Posts as ' + (wh.name || 'an unnamed webhook') +
          (wh.checked_at ? ' · confirmed by Discord ' + when(wh.checked_at) : '')));
        item.appendChild(el('div', 'meta', d.last_sent ?
          'Last sent ' + when(d.last_sent) + ' (' + d.last_lines + ' line(s)). Checked ' + every(b.every) + '.' :
          'Nothing sent since the server started. Checked ' + every(b.every) + '.'));
        if (d.error) {
          item.appendChild(el('div', 'why', d.error + (d.error_at ? ' (' + when(d.error_at) + ')' : '')));
        }
        if (d.stopped) {
          item.appendChild(el('div', 'why', 'Nothing is sent until the webhook is replaced.'));
        } else if (d.waiting_until) {
          item.appendChild(el('div', 'meta', 'Discord asked to wait until ' + when(d.waiting_until) + '.'));
        }
      }
      box.appendChild(item);

      (b.categories || []).forEach(function (c) {
        var row = el('div', 'item');
        var lab = el('label', 'checkbox');
        var tick = el('input');
        tick.type = 'checkbox';
        tick.value = c.id;
        tick.checked = !!c.on;
        lab.appendChild(tick);
        lab.appendChild(document.createTextNode(' ' + c.label + (c.default ? '' : ' (off by default)')));
        row.appendChild(lab);
        row.appendChild(el('div', 'meta', c.sends));
        if (c.titles) {
          row.appendChild(el('div', 'why', 'Sends titles — what the library holds, who asked for what — to Discord.'));
        }
        cats.appendChild(row);
      });

      if (!b.configured) { return; }
      actions.appendChild(button('Send a test message', 'ghost', function (btn) {
        btn.disabled = true;
        api('POST', '/api/v1/admin/notifications/test').then(function (r) {
          btn.disabled = false;
          if (r.status === 200 && r.body) { ok(r.body.note || 'Sent.'); }
          else if (r.status === 400 && r.body) { refuse(r.body.error || 'Discord refused the message'); }
          else { fail(r, 'the test message could not be sent'); }
          loaders.notifications();
        });
      }));
      /* Removing is reversible — paste the link again — so it asks nothing. */
      actions.appendChild(button('Remove the webhook', 'ghost', function (btn) {
        btn.disabled = true;
        api('PUT', '/api/v1/admin/notifications/webhook', { url: '' }).then(function (r) {
          btn.disabled = false;
          if (r.status !== 200 || !r.body) { fail(r, 'could not remove the webhook'); return; }
          ok(r.body.note || 'Removed.');
          loaders.notifications();
        });
      }));
    });
  };

  function wireNotifications() {
    var form = $('notifications-form');
    if (form) {
      submit(form, function () {
        var field = $('notifications-webhook');
        var link = field.value;
        /* Cleared before the answer arrives, as the metadata key is: the link
         * is the credential for posting into the operator's channel. */
        field.value = '';
        return api('PUT', '/api/v1/admin/notifications/webhook', { url: link }).then(function (res) {
          if (res.status === 200 && res.body) {
            ok(res.body.note || 'Stored.');
          } else if (res.status === 400 && res.body) {
            refuse((res.body.error || 'The link was refused') + '. ' + (res.body.note || ''));
          } else {
            fail(res, 'Discord could not be asked, so the link was not stored');
          }
          loaders.notifications();
        });
      });
    }
    var cats = $('notifications-categories');
    if (cats) {
      submit(cats, function () {
        var chosen = [];
        var boxes = $('notifications-category-list').querySelectorAll('input[type="checkbox"]');
        for (var i = 0; i < boxes.length; i++) {
          if (boxes[i].checked) { chosen.push(boxes[i].value); }
        }
        return api('PUT', '/api/v1/admin/notifications/categories', { categories: chosen }).then(function (res) {
          if (res.status !== 200) { fail(res, 'could not save what is sent'); return; }
          ok(chosen.length ? 'Saved. Sent from now on: ' + chosen.join(', ') + '.' : 'Saved. Nothing will be sent.');
          loaders.notifications();
        });
      });
    }
  }

  // -------------------------------------------------------------------------
  // network
  // -------------------------------------------------------------------------

  // -------------------------------------------------------------------------
  // SOCKS5 proxy from the web, and restarting (ADR-0065)
  // -------------------------------------------------------------------------

  var PROXY_PAIRED = ['metadata', 'subtitle'];

  function proxyBoxes() {
    return Array.prototype.slice.call($('proxy-profiles').querySelectorAll('input[type=checkbox]'));
  }

  /* Proxied searches with direct metadata is a combination the lint refuses,
   * so ticking searches ticks and holds the two that must go with it. */
  function syncProxyBoxes() {
    var boxes = proxyBoxes();
    var indexer = boxes.filter(function (b) { return b.value === 'indexer'; })[0];
    boxes.forEach(function (b) {
      if (PROXY_PAIRED.indexOf(b.value) >= 0) {
        if (indexer.checked) { b.checked = true; }
        b.disabled = indexer.checked;
      }
    });
  }

  function showProxy(p, problem) {
    var box = $('proxy-status');
    var actions = $('proxy-actions');
    if (!box) { return; }
    clear(box);
    clear(actions);
    if (problem) { box.appendChild(el('div', 'item why', problem)); return; }
    if (!p) { return; }
    var stored = p.stored || {};
    var inForce = p.in_force || {};
    if (p.problem) {
      box.appendChild(el('div', 'item why', 'The saved proxy fails the configuration check, so the traffic ' +
        'it would carry is BLOCKED until it is changed: ' + p.problem));
    }
    box.appendChild(row(inForce.address ? 'In use: ' + inForce.address : 'No proxy in use',
      inForce.address ? 'carrying ' + (inForce.profiles || []).join(', ') +
        (inForce.username ? ' · as ' + inForce.username : '') : 'traffic leaves by this machine\'s own address'));
    if ((p.file_set || []).length) {
      box.appendChild(row('Set in the configuration file', (p.file_set || []).join(', ') +
        ' keep the file\'s setting; the proxy does not change them'));
    }
    if (p.differs) {
      box.appendChild(el('div', 'item why', 'Saved' + (stored.address ? ' (' + stored.address + ')' : ' (no proxy)') +
        ' — it applies when the server restarts.'));
      actions.appendChild(button('Restart now', 'primary', restartServer));
    }
    $('proxy-address').value = stored.address || '';
    $('proxy-user').value = stored.username || '';
    var ticked = stored.address ? (stored.profiles || []) : ['download'];
    proxyBoxes().forEach(function (b) { b.checked = ticked.indexOf(b.value) >= 0; });
    syncProxyBoxes();
  }

  function restartServer(btn) {
    btn.disabled = true;
    api('POST', '/api/v1/admin/system/restart').then(function (r) {
      if (r.status !== 202) { btn.disabled = false; fail(r, 'could not restart'); return; }
      ok('Restarting…');
      var tries = 0;
      /* The server stops answering, then answers again: wait for that. */
      var poll = function () {
        tries++;
        api('GET', '/api/v1/me').then(function (res) {
          if (res.status === 200 && tries > 1) { ok('Restarted.'); loaders.network(); return; }
          if (tries < 60) { setTimeout(poll, 1000); } else { btn.disabled = false; refuse('It has not come back after a minute: look at the server\'s log.'); }
        }, function () { if (tries < 60) { setTimeout(poll, 1000); } });
      };
      setTimeout(poll, 1500);
    });
  }

  function wireProxyForm() {
    var form = $('proxy-form');
    if (!form) { return; }
    proxyBoxes().forEach(function (b) { b.addEventListener('change', syncProxyBoxes); });
    submit(form, function () {
      var body = {
        address: $('proxy-address').value.trim(),
        username: $('proxy-user').value.trim(),
        password: $('proxy-password').value,
        profiles: proxyBoxes().filter(function (b) { return b.checked; }).map(function (b) { return b.value; }),
        current_password: $('proxy-current').value,
        code: $('proxy-code').value.trim()
      };
      /* Secrets leave the form at once, whatever the answer. */
      $('proxy-password').value = '';
      $('proxy-current').value = '';
      $('proxy-code').value = '';
      return api('PUT', '/api/v1/admin/egress/proxy', body).then(function (res) {
        if (res.status !== 200) { return fail(res, 'could not save the proxy'); }
        ok(body.address ? 'Saved. It applies when the server restarts.' : 'Removed. That applies when the server restarts.');
        loaders.network();
      });
    });
  }

  // -------------------------------------------------------------------------
  // Getting started: a checklist over the settings that already exist
  // -------------------------------------------------------------------------

  var INSTALLER_FOLDERS = [
    { path: '/media/movies', kind: 'movies', label: 'Films' },
    { path: '/media/tv', kind: 'series', label: 'Series' },
    { path: '/media/music', kind: 'music', label: 'Music' },
    { path: '/media/books', kind: 'books', label: 'Books' }
  ];

  function addInstallerFolders(btn, have) {
    btn.disabled = true;
    var todo = INSTALLER_FOLDERS.filter(function (f) { return have.indexOf(f.path) < 0; });
    var refused = [];
    var next = function (i) {
      if (i >= todo.length) {
        btn.disabled = false;
        if (refused.length) { refuse(refused.join(' · ')); } else { ok('Added the library folders.'); }
        loaders.start();
        return;
      }
      api('POST', '/api/v1/admin/rootfolders', todo[i]).then(function (res) {
        if (res.status !== 201) { refused.push(todo[i].path + ': ' + failure(res, 'refused')); }
        next(i + 1);
      });
    };
    next(0);
  }

  loaders.start = function () {
    var box = $('start-steps');
    empty(box, 'Loading…');
    var get = function (path) {
      return api('GET', path).then(function (r) { return r.status === 200 ? r.body : null; },
        function () { return null; });
    };
    Promise.all([get('/api/v1/admin/metadata'), get('/api/v1/admin/subtitles'), get('/api/v1/admin/egress'),
      get('/api/v1/admin/rootfolders'), get('/api/v1/admin/indexers'), get('/api/v1/admin/notifications')])
      .then(function (r) {
        var roots = (r[3] && r[3].root_folders) || [];
        var proxy = r[2] && r[2].proxy && r[2].proxy.stored;
        var steps = [
          { title: 'TMDB', done: r[0] && r[0].configured, view: 'metadata',
            what: 'Films and series are identified, and a series knows what it is missing, with TMDB\'s API Read Access Token.' },
          { title: 'OpenSubtitles', done: r[1] && r[1].configured, view: 'metadata',
            what: 'Subtitles are fetched with an OpenSubtitles API key, and an account for more downloads a day.' },
          { title: 'SOCKS5 proxy', done: proxy && proxy.address, view: 'network',
            what: 'Without one, downloads leave by this machine\'s own address.' },
          { title: 'Libraries', done: roots.length > 0, view: 'storage', roots: roots,
            what: 'A root folder per library: where films, series, music and books live.' },
          { title: 'An indexer', done: r[4] && (r[4].indexers || []).length > 0, view: 'indexers',
            what: 'Where searches go: a Torznab or Newznab feed, a Prowlarr or Jackett, or a tracker definition.' },
          { title: 'Discord', done: r[5] && r[5].configured, view: 'notifications',
            what: 'Where this instance tells you that something went wrong.' }
        ];
        clear(box);
        steps.forEach(function (s, i) {
          var item = el('div', 'item');
          var head = el('div', 'title', (i + 1) + '. ' + s.title);
          head.appendChild(el('span', 'badge ' + (s.done ? 'good' : 'warn'), s.done ? 'done' : 'not yet'));
          item.appendChild(head);
          item.appendChild(el('div', 'meta', s.what));
          var actions = el('div', 'actions');
          actions.appendChild(button(s.done ? 'Change' : 'Set it up', s.done ? 'ghost' : 'primary', function () {
            window.location.hash = '#' + s.view;
          }));
          if (s.roots) {
            var have = s.roots.map(function (rf) { return rf.path; });
            if (INSTALLER_FOLDERS.some(function (f) { return have.indexOf(f.path) < 0; })) {
              actions.appendChild(button('Add the installer\'s folders', 'ghost', function (btn) {
                addInstallerFolders(btn, have);
              }));
            }
          }
          item.appendChild(actions);
          box.appendChild(item);
        });
      });
  };

  loaders.network = function () {
    var box = $('network-status');
    var actions = $('network-actions');
    var leak = $('network-leak');
    var profiles = $('network-profiles');
    empty(box, 'Loading…');
    clear(actions);
    clear(leak);
    clear(profiles);
    api('GET', '/api/v1/admin/egress').then(function (res) {
      if (res.status !== 200 || !res.body) { empty(box, failure(res, 'could not load the network policy')); return; }
      var b = res.body;
      clear(box);
      showProxy(b.proxy, b.proxy_error);
      if (b.warning) { box.appendChild(el('div', 'item why', b.warning)); }
      var item = el('div', 'item');
      /* With enforcement off the guard reports healthy because nothing is
       * required of it. A green "up" beside a tunnel's name would say the
       * downloads are tunnelled when nothing is checking — so the tunnel is
       * named, and judged, only when it is enforced. */
      var title = el('div', 'title', b.enforcing ?
        'Tunnel ' + (b.tunnel_interface || '(none named)') : 'No tunnel is required');
      if (b.enforcing) {
        title.appendChild(el('span', 'badge ' + (b.healthy ? 'good' : 'bad'),
          b.healthy ? 'up' : 'down — what pauses without it is paused'));
      } else {
        title.appendChild(el('span', 'badge warn', 'not enforced'));
      }
      item.appendChild(title);
      item.appendChild(el('div', 'meta', (b.detail || 'no probe has run yet') +
        (b.since ? ' · since ' + when(b.since) : '')));
      box.appendChild(item);

      actions.appendChild(button('Run the leak test', 'ghost', function (btn) {
        btn.disabled = true;
        api('POST', '/api/v1/admin/egress/leak-test').then(function (r) {
          btn.disabled = false;
          clear(leak);
          if (r.status !== 200 || !r.body) { fail(r, 'the leak test could not run'); return; }
          var t = r.body;
          var row = el('div', 'item');
          var head = el('div', 'title', t.routes_through_tunnel ? 'Routes through the tunnel' : 'Does NOT route through the tunnel');
          head.appendChild(el('span', 'badge ' + (t.routes_through_tunnel ? 'good' : 'bad'),
            t.routes_through_tunnel ? 'pass' : 'fail'));
          row.appendChild(head);
          row.appendChild(el('div', 'meta', 'expected ' + (t.expected_interface || '—') +
            ', actual ' + (t.actual_interface || '—') + (t.source_ip ? ', source ' + t.source_ip : '')));
          if (t.detail) { row.appendChild(el('div', 'meta', t.detail)); }
          if (t.caveat) { row.appendChild(el('div', 'meta', t.caveat)); }
          leak.appendChild(row);
        });
      }));

      (b.profiles || []).forEach(function (p) {
        var row = el('div', 'item');
        var head = el('div', 'title', p.subsystem);
        head.appendChild(el('span', 'badge' + (p.mode === 'blocked' ? ' warn' : ''), p.mode));
        if (p.pauses_without_tunnel && p.mode !== 'blocked') {
          head.appendChild(el('span', 'badge', 'pauses without the tunnel'));
        }
        row.appendChild(head);
        var facts = [];
        if (p.mode === 'blocked') {
          facts.push('nothing leaves by this route');
        } else {
          if (p.address) { facts.push('via ' + p.address + (p.username ? ' as ' + p.username : '')); }
          if (p.mode === 'socks5') { facts.push(p.remote_dns ? 'names resolved by the proxy' : 'names resolved HERE'); }
          facts.push(p.blocks_private_ips ? 'private addresses refused' : 'private addresses allowed');
        }
        row.appendChild(el('div', 'meta', facts.join(' · ')));
        profiles.appendChild(row);
      });
    });
  };

  function boot() {
    api('GET', '/api/v1/me').then(function (res) {
      if (res.status === 409) { window.location.assign('/enroll'); return; }
      if (res.status !== 200 || !res.body) { window.location.assign('/login'); return; }

      me = res.body;
      me.permissions = me.permissions || [];

      $('whoami').textContent = me.username + ' · ' + me.role;
      $('logout').addEventListener('click', function () {
        api('POST', '/api/v1/auth/logout').then(function () {
          window.location.assign('/login');
        });
      });

      buildNav();
      wireInviteForm();
      wireCreateUserForm();
      wireLogs();
      if ($('issues-all')) { $('issues-all').addEventListener('change', loadIssues); }
      if ($('discover-section')) { $('discover-section').addEventListener('change', loaders.discover); }
      wireTokenForm();
      wireSecurity();
      wireLibrary();
      wireAdd();
      wireIdentify();
      wireWatch();
      wireSearch();
      wireRootForm();
      wireIndexerForm();
      wireMetadataForm();
      wireProxyForm();
      wireNotifications();
      wireRequestForm();
      wireAudit();
      if (can('acquisition.search')) { loadProfiles(); }

      $('boot').hidden = true;
      $('app').hidden = false;

      window.addEventListener('hashchange', showView);
      showView();
    });
  }

  boot();
})();
