// Wordle Daily page. Plain JavaScript, no build step. The server owns the
// game: this page only sends guesses and renders the state it gets back. It
// never knows the answer until the game is over.
(function () {
  'use strict';

  /**
   * @typedef {{word: string, marks: string}} GuessView
   * @typedef {{played: number, win_pct: number, cur: number, max: number, dist: number[]}} StatsView
   * @typedef {{
   *   num: number, date: string, player: string, max: number, len: number,
   *   guesses: GuessView[], status: 'playing'|'won'|'lost', answer?: string,
   *   stats?: StatsView, next_at: number, share?: string
   * }} View
   * @typedef {{code: string, message: string}} ApiError
   */

  var TOKEN_KEY = 'wordledaily-token';
  var CONTRAST_KEY = 'wordledaily-contrast';
  var ROWS = 6;
  var COLS = 5;
  var FLIP_MS = 250;
  var KEY_ROWS = ['qwertyuiop', 'asdfghjkl', 'zxcvbnm'];
  var MARK_CLASS = { c: 'correct', p: 'present', w: 'absent' };
  var MARK_RANK = { absent: 1, present: 2, correct: 3 };
  /** One flip: half a turn per FLIP_MS. */
  var REVEAL_MS = FLIP_MS * 2;
  /** The last tile starts its flip this long after the first. */
  var STAGGER_MS = FLIP_MS / 2;
  var TILE_GAP = 5;
  var MAX_TILE = 62;
  var MIN_TILE = 24;
  var WIN_WORDS = ['Genius', 'Magnificent', 'Impressive', 'Splendid', 'Great', 'Phew'];

  /** @param {string} id @returns {HTMLElement} */
  function $(id) {
    var el = document.getElementById(id);
    if (!el) throw new Error('missing #' + id);
    return el;
  }

  var board = $('board');
  var keyboard = $('keyboard');
  var toast = $('toast');
  var endBox = $('end');
  var statsBtn = $('stats-btn');
  var contrastBtn = $('contrast-btn');

  /** @type {View|null} */
  var view = null;
  var typed = '';
  var busy = false;
  var toastTimer = 0;
  var countdownTimer = 0;
  /** The row whose marks are being revealed, and when that ends. */
  var revealRow = -1;
  var revealUntil = 0;
  var revealTimer = 0;
  /** @type {Object<string, HTMLButtonElement>} */
  var keys = {};

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

  /** @param {string} text @param {number} [ms] 0 keeps it shown */
  function say(text, ms) {
    toast.textContent = text;
    toast.hidden = false;
    clearTimeout(toastTimer);
    if (ms !== 0) {
      toastTimer = window.setTimeout(function () { toast.hidden = true; }, ms || 1800);
    }
  }

  function buildBoard() {
    for (var r = 0; r < ROWS; r++) {
      var row = document.createElement('div');
      row.className = 'row';
      row.setAttribute('role', 'group');
      row.setAttribute('aria-label', 'Guess ' + (r + 1));
      for (var c = 0; c < COLS; c++) {
        var tile = document.createElement('div');
        tile.className = 'tile';
        tile.setAttribute('role', 'img');
        tile.setAttribute('aria-label', 'empty');
        row.appendChild(tile);
      }
      board.appendChild(row);
    }
  }

  /**
   * Sizes the board from the space between the header and the keyboard, so
   * the whole game fits a short phone screen without scrolling.
   */
  function fitBoard() {
    var wrap = board.parentElement;
    if (!wrap || !wrap.clientHeight) return;
    var tile = Math.floor(Math.min(
      MAX_TILE,
      (wrap.clientWidth - (COLS - 1) * TILE_GAP) / COLS,
      (wrap.clientHeight - (ROWS - 1) * TILE_GAP) / ROWS
    ));
    tile = Math.max(MIN_TILE, tile);
    board.style.width = (tile * COLS + (COLS - 1) * TILE_GAP) + 'px';
    board.style.setProperty('--tile', tile + 'px');
  }

  /** @param {string} label @param {string} key @param {boolean} wide */
  function keyButton(label, key, wide) {
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'key' + (wide ? ' wide' : '');
    b.textContent = label;
    b.setAttribute('data-key', key);
    if (key === 'back') b.setAttribute('aria-label', 'Delete letter');
    if (key === 'enter') b.setAttribute('aria-label', 'Submit guess');
    return b;
  }

  function buildKeyboard() {
    KEY_ROWS.forEach(function (letters, i) {
      var row = document.createElement('div');
      row.className = 'krow';
      if (i === 1) row.appendChild(spacer());
      if (i === 2) row.appendChild(keyButton('Enter', 'enter', true));
      letters.split('').forEach(function (ch) {
        var b = keyButton(ch, ch, false);
        keys[ch] = b;
        row.appendChild(b);
      });
      if (i === 1) row.appendChild(spacer());
      if (i === 2) row.appendChild(keyButton('⌫', 'back', true));
      keyboard.appendChild(row);
    });
    keyboard.addEventListener('click', function (ev) {
      var t = /** @type {HTMLElement} */ (ev.target);
      var key = t && t.getAttribute('data-key');
      if (key) press(key);
    });
  }

  function spacer() {
    var s = document.createElement('div');
    s.className = 'spacer';
    return s;
  }

  /** @param {number} r @returns {HTMLElement} */
  function rowEl(r) { return /** @type {HTMLElement} */ (board.children[r]); }

  /**
   * Paints the board, the typed word and the keyboard colours. The row
   * being revealed keeps its flip classes until the reveal ends, so typing
   * meanwhile does not cut it off, and its letters colour the keyboard only
   * once it is revealed.
   */
  function render() {
    if (!view) return;
    $('num').textContent = '#' + view.num;
    $('subtitle').textContent = view.date + (view.player ? ' · ' + view.player : '');
    var revealing = Date.now() < revealUntil;
    var best = {};
    for (var r = 0; r < ROWS; r++) {
      var g = view.guesses[r];
      var row = rowEl(r);
      var flipping = revealing && r === revealRow;
      for (var c = 0; c < COLS; c++) {
        var tile = /** @type {HTMLElement} */ (row.children[c]);
        var letter = '';
        var cls = 'tile';
        var label = 'empty';
        var delay = '';
        if (g) {
          letter = g.word.charAt(c);
          var mark = MARK_CLASS[g.marks.charAt(c)] || 'absent';
          cls += ' ' + mark;
          label = letter + ' ' + mark;
          var k = letter.toLowerCase();
          if (!flipping && (!best[k] || MARK_RANK[mark] > MARK_RANK[best[k]])) best[k] = mark;
          if (flipping) {
            cls += ' flip';
            delay = (c * STAGGER_MS) + 'ms';
          }
        } else if (r === view.guesses.length && view.status === 'playing') {
          letter = typed.charAt(c).toUpperCase();
          if (letter) {
            cls += ' filled';
            label = letter;
          }
        }
        // Unchanged values leave a running animation alone.
        if (tile.className !== cls) tile.className = cls;
        if (tile.style.animationDelay !== delay) tile.style.animationDelay = delay;
        if (tile.textContent !== letter) tile.textContent = letter;
        if (tile.getAttribute('aria-label') !== label) tile.setAttribute('aria-label', label);
      }
    }
    Object.keys(keys).forEach(function (k) {
      keys[k].className = 'key' + (best[k] ? ' ' + best[k] : '');
      keys[k].setAttribute('aria-label', best[k] ? k + ', ' + best[k] : k);
    });
    statsBtn.hidden = view.status === 'playing';
  }

  /** @param {GuessView} g @returns {string} the row's result in words */
  function describe(g) {
    var parts = [];
    for (var c = 0; c < COLS; c++) {
      parts.push(g.word.charAt(c) + ' ' + (MARK_CLASS[g.marks.charAt(c)] || 'absent'));
    }
    return g.word + ': ' + parts.join(', ');
  }

  /**
   * @param {View} v
   * @param {boolean} fromGuess v answers a guess, so its new row flips
   */
  function apply(v, fromGuess) {
    if (view && view.num !== v.num) typed = '';
    var fresh = fromGuess && v.status !== 'playing';
    revealRow = -1;
    revealUntil = 0;
    clearTimeout(revealTimer);
    if (fromGuess && v.guesses.length > 0) {
      revealRow = v.guesses.length - 1;
      var ms = (COLS - 1) * STAGGER_MS + REVEAL_MS;
      revealUntil = Date.now() + ms;
      // Colours the keyboard once the row is revealed.
      revealTimer = window.setTimeout(render, ms + 20);
      $('announce').textContent = describe(v.guesses[revealRow]);
    }
    view = v;
    render();
    if (v.status !== 'playing') {
      window.setTimeout(showEnd, fresh ? FLIP_MS * 2 + 600 : 0);
      if (fresh) say(v.status === 'won' ? WIN_WORDS[v.guesses.length - 1] || 'Solved' : v.answer || '', 2500);
    } else {
      endBox.hidden = true;
    }
  }

  function showEnd() {
    if (!view || !view.stats) return;
    var won = view.status === 'won';
    $('end-title').textContent = won ? 'Solved in ' + view.guesses.length + '/' + view.max + '!' : 'Better luck tomorrow';
    $('end-answer').textContent = view.answer || '';
    var st = view.stats;
    $('st-played').textContent = String(st.played);
    $('st-pct').textContent = String(st.win_pct);
    $('st-cur').textContent = String(st.cur);
    $('st-max').textContent = String(st.max);
    var dist = $('dist');
    dist.textContent = '';
    var top = Math.max.apply(null, st.dist.concat([1]));
    st.dist.forEach(function (n, i) {
      var li = document.createElement('li');
      if (won && i === view.guesses.length - 1) li.className = 'today';
      var label = document.createElement('span');
      label.className = 'n';
      label.textContent = String(i + 1);
      var bar = document.createElement('span');
      bar.className = 'bar';
      bar.textContent = String(n);
      bar.style.width = Math.max(8, Math.round(n / top * 100)) + '%';
      li.appendChild(label);
      li.appendChild(bar);
      dist.appendChild(li);
    });
    updateShare();
    endBox.hidden = false;
    tick();
    clearInterval(countdownTimer);
    countdownTimer = window.setInterval(tick, 1000);
  }

  /**
   * Shows Share when Telegram's games.js is loaded. It loads async, so this
   * runs again on window load in case the results opened first.
   */
  function updateShare() {
    $('share-btn').hidden = !(window.TelegramGameProxy && typeof window.TelegramGameProxy.shareScore === 'function');
  }

  /** Counts down to the next puzzle; at zero it offers to load it. */
  function tick() {
    if (!view) return;
    var left = Math.max(0, Math.floor(view.next_at - Date.now() / 1000));
    var h = Math.floor(left / 3600);
    var m = Math.floor(left % 3600 / 60);
    var s = left % 60;
    $('countdown').textContent = pad(h) + ':' + pad(m) + ':' + pad(s);
    $('next-btn').hidden = left > 0;
  }

  /** @param {number} n */
  function pad(n) { return (n < 10 ? '0' : '') + n; }

  function shake() {
    if (!view) return;
    var row = rowEl(Math.min(view.guesses.length, ROWS - 1));
    row.classList.remove('shake');
    void row.offsetWidth;
    row.classList.add('shake');
  }

  /** @param {ApiError} err */
  function fail(err) {
    switch (err.code) {
      case 'new_puzzle':
        say('A new puzzle has started.', 2500);
        load();
        return;
      case 'finished':
        load();
        return;
      case 'bad_token':
      case 'expired':
        say('Open the game again from the chat.', 0);
        return;
      case 'length':
      case 'unknown':
        shake();
        say(err.message);
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
      fitBoard();
      apply(v, false);
    }, function (err) {
      busy = false;
      $('screen-game').hidden = false;
      fitBoard();
      fail(err);
    });
  }

  function submit() {
    if (!view || view.status !== 'playing') return;
    if (typed.length < COLS) {
      shake();
      say('Not enough letters');
      return;
    }
    busy = true;
    api('guess', { token: token, num: view.num, word: typed }).then(function (v) {
      busy = false;
      typed = '';
      apply(v, true);
    }, function (err) {
      busy = false;
      fail(err);
    });
  }

  /** @param {string} key a letter, 'enter' or 'back' */
  function press(key) {
    if (busy || !view || view.status !== 'playing') return;
    if (key === 'enter') {
      submit();
      return;
    }
    if (key === 'back') {
      typed = typed.slice(0, -1);
    } else if (/^[a-z]$/.test(key) && typed.length < COLS) {
      typed += key;
    } else {
      return;
    }
    render();
  }

  document.addEventListener('keydown', function (ev) {
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
    if (!endBox.hidden) {
      if (ev.key === 'Escape') endBox.hidden = true;
      return;
    }
    var k = ev.key;
    if (k === 'Enter') {
      ev.preventDefault();
      press('enter');
    } else if (k === 'Backspace') {
      press('back');
    } else if (k.length === 1 && /[a-z]/i.test(k)) {
      press(k.toLowerCase());
    }
  });

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

  function setContrast(on) {
    document.body.classList.toggle('contrast', on);
    contrastBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
  }

  contrastBtn.addEventListener('click', function () {
    var on = !document.body.classList.contains('contrast');
    setContrast(on);
    try { window.localStorage.setItem(CONTRAST_KEY, on ? '1' : '0'); } catch (e) { /* storage blocked */ }
  });
  statsBtn.addEventListener('click', showEnd);
  $('end-close').addEventListener('click', function () { endBox.hidden = true; });
  endBox.addEventListener('click', function (ev) { if (ev.target === endBox) endBox.hidden = true; });
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

  window.addEventListener('resize', fitBoard);
  window.addEventListener('load', updateShare);

  try { setContrast(window.localStorage.getItem(CONTRAST_KEY) === '1'); } catch (e) { /* storage blocked */ }
  buildBoard();
  buildKeyboard();
  if (!token) {
    $('screen-missing').hidden = false;
  } else {
    load();
  }
}());
