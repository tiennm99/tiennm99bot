// LoLdle page. Plain JavaScript, no build step. The server owns the game:
// this page only sends guesses and renders the rows it gets back. It never
// knows the answer, or any champion's attributes, until the game is over.
(function () {
  'use strict';

  /**
   * @typedef {{name: string, id: string}} Champ
   * @typedef {{key: string, value: string, result: 'correct'|'partial'|'wrong', dir?: 'up'|'down'}} Cell
   * @typedef {{name: string, id: string, marks: string, cells: Cell[]}} Row
   * @typedef {{key: string, label: string}} Column
   * @typedef {{played: number, win_pct: number, cur: number, max: number, dist: number[]}} StatsView
   * @typedef {{name: string, id: string, title?: string}} Answer
   * @typedef {{
   *   mode: 'daily'|'unlimited', num?: number, date?: string, seq?: number,
   *   player: string, max: number, columns: Column[], guesses: Row[],
   *   status: 'playing'|'won'|'lost', gave_up?: boolean, answer?: Answer,
   *   stats?: StatsView, next_at?: number, share?: string, abandoned?: Answer
   * }} View
   * @typedef {{code: string, message: string}} ApiError
   */

  var TOKEN_KEY = 'loldle-token';
  var ICON_BASE = 'https://ddragon.leagueoflegends.com/cdn/img/champion/tiles/';
  var MAX_OPTIONS = 8;
  var STAGGER_MS = 80;
  var CONFIRM_MS = 4000;
  var CHAMPIONS_RETRY_MS = 1000;
  var CHAMPIONS_RETRY_MAX_MS = 30000;

  /** @param {string} id @returns {HTMLElement} */
  function $(id) {
    var el = document.getElementById(id);
    if (!el) throw new Error('missing #' + id);
    return el;
  }

  var input = /** @type {HTMLInputElement} */ ($('search-input'));
  var list = $('search-list');
  var board = $('board');
  var toast = $('toast');
  var endBox = $('end');
  var newTop = /** @type {HTMLButtonElement} */ ($('new-top'));

  /** @type {View|null} */
  var view = null;
  /** @type {Champ[]} */
  var champions = [];
  var championsLoading = false;
  var championsTries = 0;
  /** @type {number|undefined} */
  var championsTimer;
  /** @type {Champ[]} */
  var matches = [];
  var active = -1;
  var busy = false;
  var toastTimer = 0;
  var countdownTimer = 0;
  var confirmTimer = 0;
  /** Guesses already shown, so only a new row animates. */
  var shownGuesses = -1;
  var shownKey = '';

  var token = readToken();

  /**
   * Takes the signed token from the URL, then strips it from the address bar
   * so copying or sharing the page does not leak it. sessionStorage keeps it
   * for a reload; the page still works when storage is blocked.
   * @returns {string}
   */
  function readToken() {
    var fromURL = new URLSearchParams(window.location.search).get('t') || '';
    if (fromURL) {
      try { window.sessionStorage.setItem(TOKEN_KEY, fromURL); } catch (e) { /* storage blocked */ }
      try {
        window.history.replaceState(null, '', window.location.pathname + window.location.hash);
      } catch (e) { /* history API unavailable */ }
      return fromURL;
    }
    try { return window.sessionStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; }
  }

  /**
   * @param {string} path
   * @param {Object} body
   * @returns {Promise<View>}
   */
  function api(path, body) {
    return fetch('api/' + path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      credentials: 'omit',
      cache: 'no-store'
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) {
          throw { code: data.error || 'http_' + res.status, message: data.message || 'Something went wrong. Try again later.' };
        }
        return data;
      });
    }, function () {
      throw { code: 'network', message: 'No connection to the server. Check your network and try again.' };
    });
  }

  /** @returns {Promise<Champ[]>} rejects when the list cannot be loaded */
  function fetchChampions() {
    return fetch('champions.json', { credentials: 'omit' }).then(function (res) {
      if (!res.ok) throw new Error('champions.json: ' + res.status);
      return res.json();
    }).then(function (list) {
      if (!Array.isArray(list) || !list.length) throw new Error('champions.json: empty');
      return list;
    });
  }

  /**
   * Loads the champion list the search needs, retrying with backoff on a
   * flaky network. A query typed meanwhile is searched again once it lands.
   */
  function loadChampions() {
    if (championsLoading || champions.length) return;
    championsLoading = true;
    clearTimeout(championsTimer);
    fetchChampions().then(function (list) {
      championsLoading = false;
      championsTries = 0;
      champions = list;
      if (document.activeElement === input && input.value.trim()) {
        matches = search(input.value);
        active = -1;
        renderList();
      }
    }, function () {
      championsLoading = false;
      championsTries++;
      championsTimer = window.setTimeout(loadChampions, Math.min(CHAMPIONS_RETRY_MS * Math.pow(2, championsTries - 1), CHAMPIONS_RETRY_MAX_MS));
    });
  }

  /** @param {string} text @param {number} [ms] 0 keeps it shown */
  function say(text, ms) {
    toast.textContent = text;
    toast.hidden = false;
    clearTimeout(toastTimer);
    if (ms !== 0) {
      toastTimer = window.setTimeout(function () { toast.hidden = true; }, ms || 2200);
    }
  }

  /** @param {string} s @returns {string} lowercase letters and digits only */
  function norm(s) {
    return s.toLowerCase().replace(/[^a-z0-9]/g, '');
  }

  /**
   * A champion's icon from Data Dragon, replaced by its initial when the
   * image cannot load. The handler is attached here: the CSP forbids inline
   * event attributes.
   * @param {string} id @param {string} name @returns {HTMLElement}
   */
  function icon(id, name) {
    var badge = function () {
      var b = document.createElement('span');
      b.className = 'icon badge';
      b.setAttribute('aria-hidden', 'true');
      b.textContent = name.charAt(0).toUpperCase();
      return b;
    };
    if (!id) return badge();
    var img = document.createElement('img');
    img.className = 'icon';
    img.alt = '';
    img.loading = 'lazy';
    img.decoding = 'async';
    img.referrerPolicy = 'no-referrer';
    img.addEventListener('error', function () {
      if (img.parentNode) img.parentNode.replaceChild(badge(), img);
    });
    img.src = ICON_BASE + encodeURIComponent(id) + '_0.jpg';
    return img;
  }

  // ---- Search ----

  /** @param {string} query @returns {Champ[]} */
  function search(query) {
    var q = norm(query);
    if (!q || !view) return [];
    var guessed = {};
    view.guesses.forEach(function (g) { guessed[g.name] = true; });
    var prefix = [];
    var inside = [];
    champions.forEach(function (c) {
      if (guessed[c.name]) return;
      var n = norm(c.name);
      var at = n.indexOf(q);
      if (at === 0) prefix.push(c);
      else if (at > 0) inside.push(c);
    });
    return prefix.concat(inside).slice(0, MAX_OPTIONS);
  }

  function closeList() {
    list.hidden = true;
    list.textContent = '';
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    matches = [];
    active = -1;
  }

  function renderList() {
    list.textContent = '';
    if (!matches.length) {
      closeList();
      return;
    }
    matches.forEach(function (c, i) {
      var li = document.createElement('li');
      li.id = 'champion-option-' + i;
      li.setAttribute('role', 'option');
      li.setAttribute('aria-selected', i === active ? 'true' : 'false');
      li.appendChild(icon(c.id, c.name));
      var label = document.createElement('span');
      label.textContent = c.name;
      li.appendChild(label);
      // mousedown keeps the input focused, so a tap picks the option.
      li.addEventListener('mousedown', function (ev) { ev.preventDefault(); });
      li.addEventListener('click', function () { pick(c); });
      list.appendChild(li);
    });
    list.hidden = false;
    input.setAttribute('aria-expanded', 'true');
    if (active >= 0) {
      input.setAttribute('aria-activedescendant', 'champion-option-' + active);
      var el = list.children[active];
      if (el && el.scrollIntoView) el.scrollIntoView({ block: 'nearest' });
    } else {
      input.removeAttribute('aria-activedescendant');
    }
  }

  /** @param {Champ} c */
  function pick(c) {
    input.value = '';
    closeList();
    submit(c.name);
  }

  input.addEventListener('input', function () {
    if (!champions.length) {
      // Typing retries a failed load at once instead of waiting out the backoff.
      clearTimeout(championsTimer);
      loadChampions();
    }
    matches = search(input.value);
    active = -1;
    renderList();
  });

  input.addEventListener('keydown', function (ev) {
    if (ev.key === 'ArrowDown' && matches.length) {
      ev.preventDefault();
      active = Math.min(active + 1, matches.length - 1);
      renderList();
    } else if (ev.key === 'ArrowUp' && matches.length) {
      ev.preventDefault();
      active = Math.max(active - 1, 0);
      renderList();
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      if (active >= 0 && matches[active]) {
        pick(matches[active]);
      } else if (matches.length === 1 || (matches.length && norm(matches[0].name) === norm(input.value))) {
        pick(matches[0]);
      } else if (input.value.trim() && !champions.length) {
        say(championsTries ? "Couldn't load the champion list. Retrying…" : 'Loading champions…');
      } else if (input.value.trim()) {
        say(matches.length ? 'Pick a champion from the list.' : 'No champion matches.');
      }
    } else if (ev.key === 'Escape') {
      closeList();
    }
  });

  document.addEventListener('mousedown', function (ev) {
    var t = /** @type {Node} */ (ev.target);
    if (!list.hidden && t !== input && !list.contains(t)) closeList();
  });

  // ---- Board ----

  /** @param {string} cls @param {string} text @returns {HTMLElement} */
  function cell(cls, text) {
    var d = document.createElement('div');
    d.className = 'cell' + (cls ? ' ' + cls : '');
    d.setAttribute('role', 'cell');
    if (text) d.textContent = text;
    return d;
  }

  /**
   * A cell as words for screen readers: its column, the guessed value, the
   * result and, for the year, which way the answer lies.
   * @param {Cell} c @param {Column[]} columns @returns {string}
   */
  function cellWords(c, columns) {
    var col = columns.filter(function (x) { return x.key === c.key; })[0];
    var words = (col ? col.label + ': ' : '') + c.value + ', ' + c.result;
    if (c.dir) words += c.dir === 'up' ? ', the answer is newer' : ', the answer is older';
    return words;
  }

  /** @param {Row} g @param {boolean} fresh @returns {HTMLElement} */
  function rowEl(g, fresh) {
    var r = document.createElement('div');
    r.className = 'grow' + (fresh ? ' fresh' : '');
    r.setAttribute('role', 'row');
    var name = cell('name', '');
    name.setAttribute('role', 'rowheader');
    name.appendChild(icon(g.id, g.name));
    var label = document.createElement('span');
    label.textContent = g.name;
    name.appendChild(label);
    var cells = [name];
    g.cells.forEach(function (c) {
      var el = cell(c.result, c.value);
      if (c.dir) {
        var arrow = document.createElement('span');
        arrow.className = 'dir';
        arrow.setAttribute('aria-hidden', 'true');
        arrow.textContent = c.dir === 'up' ? '↑' : '↓';
        el.appendChild(arrow);
      }
      el.setAttribute('aria-label', cellWords(c, view ? view.columns : []));
      cells.push(el);
    });
    cells.forEach(function (el, i) {
      if (fresh) el.style.animationDelay = (i * STAGGER_MS) + 'ms';
      r.appendChild(el);
    });
    return r;
  }

  function renderBoard() {
    if (!view) return;
    var key = view.mode + ':' + (view.num || view.seq);
    var fresh = key === shownKey && view.guesses.length === shownGuesses + 1;
    board.textContent = '';
    if (view.guesses.length) {
      var head = document.createElement('div');
      head.className = 'grow head';
      head.setAttribute('role', 'row');
      var first = cell('name', 'Champion');
      first.setAttribute('role', 'columnheader');
      head.appendChild(first);
      view.columns.forEach(function (c) {
        var h = cell('', c.label);
        h.setAttribute('role', 'columnheader');
        head.appendChild(h);
      });
      board.appendChild(head);
    }
    for (var i = view.guesses.length - 1; i >= 0; i--) {
      board.appendChild(rowEl(view.guesses[i], fresh && i === view.guesses.length - 1));
    }
    shownKey = key;
    shownGuesses = view.guesses.length;
  }

  function render() {
    if (!view) return;
    var daily = view.mode === 'daily';
    $('mode').textContent = daily ? 'Daily #' + view.num + ' · ' + view.date : 'Unlimited · Round ' + view.seq;
    $('player').textContent = view.player || '';
    $('counter').textContent = view.guesses.length + ' / ' + view.max + ' guesses';
    var playing = view.status === 'playing';
    input.disabled = !playing || busy;
    input.placeholder = playing ? 'Type a champion name...' : 'Game over';
    if (!playing) closeList();
    newTop.hidden = daily;
    resetConfirm();
    renderBoard();
    renderEnd();
  }

  // ---- End of game ----

  function renderEnd() {
    if (!view || view.status === 'playing' || !view.answer || !view.stats) {
      endBox.hidden = true;
      clearInterval(countdownTimer);
      return;
    }
    var won = view.status === 'won';
    var title = $('end-title');
    title.textContent = won ? 'You got it!' : 'Game Over';
    title.className = won ? 'won' : 'lost';
    var ans = view.answer;
    var iconBox = $('end-icon');
    iconBox.textContent = '';
    iconBox.appendChild(icon(ans.id, ans.name));
    var text = $('end-text');
    text.textContent = '';
    var strong = document.createElement('strong');
    strong.textContent = ans.name;
    if (won) {
      var n = view.guesses.length;
      text.appendChild(document.createTextNode('You found '));
      text.appendChild(strong);
      text.appendChild(document.createTextNode(' in ' + n + ' guess' + (n > 1 ? 'es' : '') + '!'));
    } else {
      text.appendChild(document.createTextNode(view.gave_up ? 'You gave up. The champion was ' : 'The champion was '));
      text.appendChild(strong);
    }
    if (ans.title) {
      var sub = document.createElement('span');
      sub.className = 'title';
      sub.textContent = ans.title;
      text.appendChild(sub);
    }
    var st = view.stats;
    $('st-played').textContent = String(st.played);
    $('st-pct').textContent = String(st.win_pct);
    $('st-cur').textContent = String(st.cur);
    $('st-max').textContent = String(st.max);
    renderDist(st, won);
    var daily = view.mode === 'daily';
    $('end-daily').hidden = !daily;
    $('end-unlimited').hidden = daily;
    $('daily-hint').hidden = daily;
    if (daily) {
      updateShare();
      tick();
      clearInterval(countdownTimer);
      countdownTimer = window.setInterval(tick, 1000);
    } else {
      clearInterval(countdownTimer);
    }
    endBox.hidden = false;
  }

  /** @param {StatsView} st @param {boolean} won */
  function renderDist(st, won) {
    var dist = $('dist');
    dist.textContent = '';
    // Rows up to the round's budget, or further when an older, longer round
    // won there.
    var rows = view ? view.max : st.dist.length;
    st.dist.forEach(function (n, i) { if (n > 0) rows = Math.max(rows, i + 1); });
    rows = Math.min(rows, st.dist.length);
    var top = Math.max.apply(null, st.dist.concat([1]));
    for (var i = 0; i < rows; i++) {
      var li = document.createElement('li');
      if (won && view && i === view.guesses.length - 1) li.className = 'today';
      var label = document.createElement('span');
      label.className = 'n';
      label.textContent = String(i + 1);
      var bar = document.createElement('span');
      bar.className = 'bar';
      bar.textContent = String(st.dist[i]);
      bar.style.width = Math.max(8, Math.round(st.dist[i] / top * 100)) + '%';
      li.appendChild(label);
      li.appendChild(bar);
      dist.appendChild(li);
    }
  }

  /**
   * Shows Share when Telegram's games.js is loaded. It loads async, so this
   * runs again on window load in case the results showed first.
   */
  function updateShare() {
    $('share-btn').hidden = !(window.TelegramGameProxy && typeof window.TelegramGameProxy.shareScore === 'function');
  }

  /** Counts down to the next daily champion; at zero it offers to load it. */
  function tick() {
    if (!view || !view.next_at) return;
    var left = Math.max(0, Math.floor(view.next_at - Date.now() / 1000));
    var h = Math.floor(left / 3600);
    var m = Math.floor(left % 3600 / 60);
    var s = left % 60;
    $('countdown').textContent = pad(h) + ':' + pad(m) + ':' + pad(s);
    $('next-btn').hidden = left > 0;
  }

  /** @param {number} n */
  function pad(n) { return (n < 10 ? '0' : '') + n; }

  // ---- Requests ----

  /** @param {View} v */
  function apply(v) {
    if (view && v.guesses.length > view.guesses.length && v.mode === view.mode) {
      var g = v.guesses[v.guesses.length - 1];
      $('announce').textContent = g.name + ': ' + g.cells.map(function (c) { return cellWords(c, v.columns); }).join('; ');
    }
    view = v;
    render();
  }

  /** @param {ApiError} err */
  function fail(err) {
    switch (err.code) {
      case 'new_puzzle':
      case 'new_round':
        say(err.message, 3000);
        load();
        return;
      case 'finished':
        load();
        return;
      case 'bad_token':
      case 'expired':
        say('Open the game again from the chat.', 0);
        return;
    }
    say(err.message, 3000);
  }

  function load() {
    if (busy) return;
    busy = true;
    api('state', { token: token }).then(function (v) {
      busy = false;
      $('screen-game').hidden = false;
      apply(v);
    }, function (err) {
      busy = false;
      $('screen-game').hidden = false;
      fail(err);
    });
  }

  /** @param {string} name */
  function submit(name) {
    if (busy || !view || view.status !== 'playing') return;
    busy = true;
    input.disabled = true;
    var body = { token: token, name: name };
    if (view.mode === 'daily') body.num = view.num; else body.seq = view.seq;
    api('guess', body).then(function (v) {
      busy = false;
      apply(v);
      if (v.status === 'won') say('You got it!', 2000);
      if (v.status === 'playing') input.focus();
    }, function (err) {
      busy = false;
      if (view) input.disabled = view.status !== 'playing';
      fail(err);
    });
  }

  function newGame() {
    if (busy || !view || view.mode !== 'unlimited') return;
    busy = true;
    resetConfirm();
    api('new', { token: token, seq: view.seq }).then(function (v) {
      busy = false;
      endBox.hidden = true;
      apply(v);
      window.scrollTo(0, 0);
      if (v.abandoned) say('Round given up. The champion was ' + v.abandoned.name + '.', 4000);
    }, function (err) {
      busy = false;
      fail(err);
    });
  }

  /** The header button asks once before giving up a round with guesses. */
  function resetConfirm() {
    clearTimeout(confirmTimer);
    newTop.classList.remove('confirm');
    newTop.textContent = 'New game';
  }

  newTop.addEventListener('click', function () {
    if (!view) return;
    var risky = view.status === 'playing' && view.guesses.length > 0;
    if (risky && !newTop.classList.contains('confirm')) {
      newTop.classList.add('confirm');
      newTop.textContent = 'Give up this round?';
      clearTimeout(confirmTimer);
      confirmTimer = window.setTimeout(resetConfirm, CONFIRM_MS);
      return;
    }
    newGame();
  });
  $('new-end').addEventListener('click', newGame);

  /** Copies text, falling back to a hidden textarea where the clipboard API is blocked. */
  function copy(text) {
    var done = function () { say('Copied results to clipboard'); };
    var fallback = function () {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.className = 'offscreen';
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      if (ok) done(); else say('Copy failed. Select and copy the result by hand.', 3000);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fallback);
    } else {
      fallback();
    }
  }

  $('share-btn').addEventListener('click', function () {
    if (window.TelegramGameProxy) window.TelegramGameProxy.shareScore();
  });
  $('copy-btn').addEventListener('click', function () {
    if (view && view.share) copy(view.share);
  });
  $('next-btn').addEventListener('click', function () {
    endBox.hidden = true;
    load();
  });
  window.addEventListener('load', updateShare);

  if (!token) {
    $('screen-missing').hidden = false;
  } else {
    loadChampions();
    load();
  }
}());
