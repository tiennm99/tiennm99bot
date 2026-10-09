// Nối từ game page. Plain JavaScript, no build step. The server owns the game:
// this page only sends words and renders the state it gets back.
(function () {
  'use strict';

  /**
   * @typedef {{pos: string, gloss: string}} Sense
   * @typedef {{word: string, by: 'bot'|'player', points: number, meanings: Sense[]}} ChainEntry
   * @typedef {{
   *   session: string, player: string, difficulty: string,
   *   status: 'playing'|'won'|'lost', end_reason: string, current: string,
   *   chain: ChainEntry[], score: number, turn_limit_ms: number,
   *   deadline_ms: number, server_now_ms: number, suggestions: string[],
   *   score_reported: string
   * }} SessionView
   * @typedef {{accepted: boolean, reason: string, message: string, player_word: string, bot_word: string}} MoveResult
   */

  /** Grace the server allows after the deadline, plus a little slack. */
  var GRACE_MS = 2300;
  /** Shortest "bot đang nghĩ" pause, so the reply does not feel instant. */
  var THINK_MS = 350;
  var REPORT_POLL_MS = 1500;
  var REPORT_POLLS = 10;

  /** @param {string} id @returns {HTMLElement} */
  function $(id) {
    var el = document.getElementById(id);
    if (!el) throw new Error('missing #' + id);
    return el;
  }

  var screens = {
    missing: $('screen-missing'),
    start: $('screen-start'),
    game: $('screen-game'),
    end: $('screen-end')
  };
  var chainBox = $('chain-box');
  var chainList = $('chain');
  var wordInput = /** @type {HTMLInputElement} */ ($('word'));
  var sendBtn = /** @type {HTMLButtonElement} */ ($('send-btn'));
  var startBtn = /** @type {HTMLButtonElement} */ ($('start-btn'));
  var giveUpBtn = /** @type {HTMLButtonElement} */ ($('give-up'));
  var message = $('message');
  var timerBar = $('timer-bar');
  var timerText = $('timer-text');

  var TOKEN_KEY = 'noitu-token';
  var token = readToken();

  /**
   * Takes the signed token from the URL, then strips it from the address bar
   * so copying, sharing or opening the page elsewhere does not leak it. The
   * hash stays: Telegram passes its own parameters there. sessionStorage keeps
   * the token for a reload; the page still works when storage is blocked.
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

  /** @type {SessionView|null} */
  var view = null;
  /** Local wall-clock time at which the current turn ends. */
  var deadlineLocal = 0;
  var timerHandle = 0;
  var expiryHandle = 0;
  var reportPolls = 0;
  var busy = false;
  var giveUpArmed = 0;
  /** Words whose meanings are expanded, so a re-render keeps them open. */
  var expanded = Object.create(null);

  /** @param {keyof typeof screens} name */
  function show(name) {
    Object.keys(screens).forEach(function (key) {
      screens[/** @type {keyof typeof screens} */ (key)].hidden = key !== name;
    });
    chainBox.hidden = !(name === 'game' || name === 'end');
  }

  /** @param {number} ms @returns {Promise<void>} */
  function delay(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  /**
   * POSTs a JSON body to the game API. Rejects with {code, message} on a
   * non-2xx answer or a network failure.
   * @param {string} path @param {object} body @returns {Promise<any>}
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
          throw { code: data.error || 'http_' + res.status, message: data.message || 'Có lỗi xảy ra, thử lại sau.' };
        }
        return data;
      });
    }, function () {
      throw { code: 'network', message: 'Mất kết nối tới máy chủ. Kiểm tra mạng rồi thử lại.' };
    });
  }

  /** @param {string} text @param {string} [kind] */
  function say(text, kind) {
    message.textContent = text;
    message.className = 'message' + (kind ? ' ' + kind : '');
  }

  /** @param {ChainEntry} entry @param {boolean} fresh @returns {HTMLLIElement} */
  function chainItem(entry, fresh) {
    var li = document.createElement('li');
    li.className = entry.by + (fresh ? ' fresh' : '');

    var row = document.createElement('div');
    row.className = 'row';
    var who = document.createElement('span');
    who.className = 'who';
    who.textContent = entry.by === 'bot' ? 'Bot' : 'Bạn';
    var word = document.createElement('span');
    word.className = 'word';
    word.textContent = entry.word;
    row.appendChild(who);
    row.appendChild(word);
    if (entry.by === 'player' && entry.points > 0) {
      var pts = document.createElement('span');
      pts.className = 'points';
      pts.textContent = '+' + entry.points;
      row.appendChild(pts);
    }
    li.appendChild(row);

    if (!entry.meanings || entry.meanings.length === 0) {
      var none = document.createElement('span');
      none.className = 'no-meaning';
      none.textContent = 'Chưa có nghĩa';
      row.appendChild(none);
      return li;
    }

    var senses = document.createElement('ol');
    senses.className = 'senses';
    entry.meanings.forEach(function (sense) {
      var item = document.createElement('li');
      item.textContent = (sense.pos ? '(' + sense.pos + ') ' : '') + sense.gloss;
      senses.appendChild(item);
    });
    var toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'toggle';
    function sync() {
      var open = !!expanded[entry.word];
      senses.hidden = !open;
      toggle.textContent = open ? 'Ẩn nghĩa' : 'Xem nghĩa';
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      toggle.setAttribute('aria-label', (open ? 'Ẩn nghĩa của ' : 'Xem nghĩa của ') + entry.word);
    }
    toggle.addEventListener('click', function () {
      expanded[entry.word] = !expanded[entry.word];
      sync();
    });
    sync();
    row.appendChild(toggle);
    li.appendChild(senses);
    return li;
  }

  /** @param {ChainEntry[]} chain */
  function renderChain(chain) {
    var before = chainList.children.length;
    if (chain.length < before) {
      chainList.textContent = '';
      before = 0;
    }
    for (var i = before; i < chain.length; i++) {
      chainList.appendChild(chainItem(chain[i], before > 0));
    }
  }

  function stopTimers() {
    clearInterval(timerHandle);
    clearTimeout(expiryHandle);
    timerHandle = 0;
    expiryHandle = 0;
  }

  function tick() {
    if (!view) return;
    var left = Math.max(0, deadlineLocal - Date.now());
    var ratio = view.turn_limit_ms > 0 ? left / view.turn_limit_ms : 0;
    timerBar.style.transform = 'scaleX(' + Math.min(1, ratio).toFixed(3) + ')';
    var low = left <= 5000;
    timerBar.classList.toggle('low', low);
    timerText.classList.toggle('low', low);
    timerText.textContent = String(Math.ceil(left / 1000));
    if (left <= 0 && timerHandle) {
      clearInterval(timerHandle);
      timerHandle = 0;
      say('Hết giờ…', 'error');
    }
  }

  /** Starts the countdown from the server's deadline, not the local clock. */
  function startCountdown() {
    stopTimers();
    if (!view) return;
    var left = view.deadline_ms - view.server_now_ms;
    deadlineLocal = Date.now() + left;
    tick();
    timerHandle = window.setInterval(tick, 100);
    // Once the grace period has passed, ask the server to settle the timeout.
    expiryHandle = window.setTimeout(refresh, Math.max(0, left) + GRACE_MS);
  }

  /** @param {SessionView} next */
  function render(next) {
    view = next;
    $('player').textContent = next.player || '';
    $('score').textContent = String(next.score);
    $('current').textContent = next.current;
    renderChain(next.chain);
    if (next.status === 'playing') {
      show('game');
      startCountdown();
      return;
    }
    stopTimers();
    renderEnd(next);
  }

  var endText = {
    bot_stuck: ['Bạn thắng!', 'Bot hết từ để nối.'],
    max_moves: ['Bạn thắng!', 'Chuỗi đã đủ dài, bot chịu thua.'],
    timeout: ['Bot thắng', 'Bạn đã hết giờ.'],
    no_legal_move: ['Bot thắng', 'Không còn từ nào nối được — đây là ngõ cụt.'],
    gave_up: ['Bot thắng', 'Bạn đã chịu thua.']
  };

  var reportText = {
    pending: 'Đang lưu điểm lên bảng xếp hạng Telegram…',
    ok: 'Đã lưu điểm vào bảng xếp hạng Telegram.',
    failed: 'Không lưu được điểm lên Telegram.',
    skipped: 'Không có điểm để lưu.'
  };

  /** @param {SessionView} v */
  function renderEnd(v) {
    show('end');
    var text = endText[/** @type {keyof typeof endText} */ (v.end_reason)] ||
      (v.status === 'won' ? ['Bạn thắng!', ''] : ['Bot thắng', '']);
    $('result-title').textContent = text[0];
    $('result-detail').textContent = text[1];
    $('final-score').textContent = String(v.score);

    var report = $('report-status');
    report.textContent = reportText[/** @type {keyof typeof reportText} */ (v.score_reported)] || '';
    report.className = v.score_reported === 'ok' ? 'report-ok' : v.score_reported === 'failed' ? 'report-failed' : 'muted';

    var box = $('suggestions-box');
    var list = $('suggestions');
    list.textContent = '';
    if (v.status === 'lost' && v.suggestions.length > 0) {
      $('suggestions-label').textContent = 'Bạn có thể đã nối:';
      v.suggestions.forEach(function (w) {
        var li = document.createElement('li');
        li.textContent = w;
        list.appendChild(li);
      });
      box.hidden = false;
    } else {
      box.hidden = true;
    }

    $('share').hidden = !(window.TelegramGameProxy && typeof window.TelegramGameProxy.shareScore === 'function');

    if (v.score_reported === 'pending' && reportPolls < REPORT_POLLS) {
      reportPolls++;
      expiryHandle = window.setTimeout(refresh, REPORT_POLL_MS);
    }
  }

  /** @param {{code: string, message: string}} err */
  function handleError(err) {
    if (err.code === 'no_session') {
      stopTimers();
      view = null;
      chainList.textContent = '';
      expanded = Object.create(null);
      show('start');
      $('start-error').textContent = err.message;
      return;
    }
    if (err.code === 'game_over') {
      refresh();
      return;
    }
    say(err.message, 'error');
  }

  function refresh() {
    if (!view) return;
    api('state', { session: view.session }).then(render, handleError);
  }

  function startGame() {
    if (busy) return;
    busy = true;
    startBtn.disabled = true;
    $('start-error').textContent = '';
    var picked = /** @type {HTMLInputElement|null} */ (document.querySelector('input[name="difficulty"]:checked'));
    api('start', { token: token, difficulty: picked ? picked.value : 'medium' }).then(function (v) {
      chainList.textContent = '';
      expanded = Object.create(null);
      reportPolls = 0;
      say('');
      wordInput.value = '';
      render(v);
      wordInput.focus();
    }, function (err) {
      $('start-error').textContent = err.message;
    }).then(function () {
      busy = false;
      startBtn.disabled = false;
    });
  }

  /** @param {Event} event */
  function submitWord(event) {
    event.preventDefault();
    var word = wordInput.value.trim();
    if (!view || busy || word === '') return;
    busy = true;
    sendBtn.disabled = true;
    say('Bot đang nghĩ', 'thinking');
    Promise.all([api('move', { session: view.session, word: word }), delay(THINK_MS)]).then(function (out) {
      /** @type {{result: MoveResult, state: SessionView}} */
      var res = out[0];
      if (res.result.accepted) {
        wordInput.value = '';
        say(res.result.bot_word ? 'Bot nối: ' + res.result.bot_word : '');
      } else {
        say(res.result.message || 'Từ không hợp lệ.', 'error');
        wordInput.select();
      }
      render(res.state);
    }, handleError).then(function () {
      busy = false;
      sendBtn.disabled = false;
      if (view && view.status === 'playing') wordInput.focus();
    });
  }

  function giveUp() {
    if (!view || busy) return;
    if (!giveUpArmed) {
      giveUpBtn.textContent = 'Bấm lần nữa để chịu thua';
      giveUpArmed = window.setTimeout(function () {
        giveUpArmed = 0;
        giveUpBtn.textContent = 'Chịu thua';
      }, 3000);
      return;
    }
    clearTimeout(giveUpArmed);
    giveUpArmed = 0;
    giveUpBtn.textContent = 'Chịu thua';
    api('give-up', { session: view.session }).then(render, handleError);
  }

  function share() {
    try {
      if (window.TelegramGameProxy) window.TelegramGameProxy.shareScore();
    } catch (e) {
      $('share').hidden = true;
    }
  }

  startBtn.addEventListener('click', startGame);
  $('move-form').addEventListener('submit', submitWord);
  giveUpBtn.addEventListener('click', giveUp);
  $('replay').addEventListener('click', function () {
    stopTimers();
    show('start');
  });
  $('share').addEventListener('click', share);
  // A tab that slept (phone locked) has a stale countdown; resync on return.
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden && view && view.status === 'playing') refresh();
  });

  show(token ? 'start' : 'missing');
})();
