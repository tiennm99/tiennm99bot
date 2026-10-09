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
   * @typedef {{name: string, you: boolean, host: boolean, score: number, alive: boolean, out_reason: string}} RoomPlayer
   * @typedef {{name: string, rank: number, score: number, bonus: number, words: number, out_reason: string, you: boolean}} RoomStanding
   * @typedef {{winner: string, words: number, end_reason: string, standings: RoomStanding[], score_reported: string}} RoomResult
   * @typedef {ChainEntry & {seat: number, name?: string}} RoomEntry
   * @typedef {{
   *   member: string, version: number, status: 'lobby'|'playing', game: number,
   *   host: boolean, seat: number, turn: number, players: RoomPlayer[],
   *   watchers: number, min_players: number, max_players: number,
   *   current: string, chain: RoomEntry[], turn_limit_ms: number,
   *   deadline_ms: number, server_now_ms: number, result: RoomResult|null
   * }} RoomView
   */

  /** Grace the server allows after the deadline, plus a little slack. */
  var GRACE_MS = 2300;
  /** Shortest "bot đang nghĩ" pause, so the reply does not feel instant. */
  var THINK_MS = 350;
  var REPORT_POLL_MS = 1500;
  var REPORT_POLLS = 10;
  /** Room state polling while the page is visible, and while it is hidden. */
  var POLL_MS = 1000;
  var POLL_HIDDEN_MS = 4000;

  /** @param {string} id @returns {HTMLElement} */
  function $(id) {
    var el = document.getElementById(id);
    if (!el) throw new Error('missing #' + id);
    return el;
  }

  var screens = {
    missing: $('screen-missing'),
    start: $('screen-start'),
    lobby: $('screen-lobby'),
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
  /** A token from a /noitu card opens the card's room instead of a game vs the bot. */
  var roomMode = isRoomToken(token);

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

  /**
   * Reads the PvP flag from the token's payload. The payload is signed, not
   * secret; the server checks the flag again, so this only picks the screen.
   * @param {string} t @returns {boolean}
   */
  function isRoomToken(t) {
    try {
      var body = t.split('.')[0].replace(/-/g, '+').replace(/_/g, '/');
      while (body.length % 4) body += '=';
      return JSON.parse(window.atob(body)).p === true;
    } catch (e) {
      return false;
    }
  }

  /** @type {SessionView|null} */
  var view = null;
  /** @type {RoomView|null} */
  var room = null;
  var pollHandle = 0;
  var rejoined = false;
  /** Game number whose chain is on screen, so a new game clears it. */
  var shownGame = -1;
  /** Game and deadline of the room turn on screen, so a new turn clears the message. */
  var shownTurn = '';
  /** Set while a room error shows a lost connection, so the next good poll clears it. */
  var offlineShown = false;
  /** Turn length of the countdown on screen. */
  var turnLimitMs = 30000;
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
    chainBox.hidden = !(name === 'game' || name === 'end' || (name === 'lobby' && chainList.children.length > 0));
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

  /**
   * Who played a chain word: the bot or "Bạn" in a game vs the bot; in a
   * room, the opening, "Bạn", or the player's name.
   * @param {ChainEntry|RoomEntry} entry @returns {{label: string, cls: string}}
   */
  function author(entry) {
    if (!roomMode) return entry.by === 'bot' ? { label: 'Bot', cls: 'bot' } : { label: 'Bạn', cls: 'player' };
    var e = /** @type {RoomEntry} */ (entry);
    if (e.by === 'bot') return { label: 'Mở đầu', cls: 'bot' };
    if (room && room.seat >= 0 && e.seat === room.seat) return { label: 'Bạn', cls: 'player' };
    return { label: e.name || '', cls: 'other' };
  }

  /** @param {ChainEntry|RoomEntry} entry @param {boolean} fresh @returns {HTMLLIElement} */
  function chainItem(entry, fresh) {
    var li = document.createElement('li');
    var by = author(entry);
    li.className = by.cls + (fresh ? ' fresh' : '');

    var row = document.createElement('div');
    row.className = 'row';
    var who = document.createElement('span');
    who.className = 'who';
    who.textContent = by.label;
    who.title = by.label;
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

  /** @param {Array<ChainEntry|RoomEntry>} chain */
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
    var left = Math.max(0, deadlineLocal - Date.now());
    var ratio = turnLimitMs > 0 ? left / turnLimitMs : 0;
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

  /**
   * Starts the countdown from the server's deadline, not the local clock.
   * Once the grace period has passed, onExpire asks the server to settle it.
   * @param {{deadline_ms: number, server_now_ms: number, turn_limit_ms: number}} v
   * @param {() => void} onExpire
   */
  function startCountdown(v, onExpire) {
    stopTimers();
    var left = v.deadline_ms - v.server_now_ms;
    turnLimitMs = v.turn_limit_ms;
    deadlineLocal = Date.now() + left;
    tick();
    timerHandle = window.setInterval(tick, 100);
    expiryHandle = window.setTimeout(onExpire, Math.max(0, left) + GRACE_MS);
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
      startCountdown(next, refresh);
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
    if (roomMode) {
      submitRoomWord(word);
      return;
    }
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
    if ((roomMode ? !room : !view) || busy) return;
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
    if (roomMode) {
      if (room) api('room/give-up', { member: room.member }).then(renderRoom, roomError);
      return;
    }
    if (view) api('give-up', { session: view.session }).then(render, handleError);
  }

  // Room mode: the members of a chat play each other on one /noitu card.
  // The page polls the room's state; the server owns the turns and the clock.

  var roomStartBtn = /** @type {HTMLButtonElement} */ ($('room-start'));
  var turnLine = $('turn-line');

  var outText = {
    timeout: 'hết giờ',
    no_legal_move: 'hết từ để nối',
    gave_up: 'chịu thua'
  };

  /** @param {number} [ms] */
  function schedulePoll(ms) {
    clearTimeout(pollHandle);
    var wait = ms === undefined ? (document.hidden ? POLL_HIDDEN_MS : POLL_MS) : ms;
    pollHandle = window.setTimeout(poll, wait);
  }

  function poll() {
    if (!room) return;
    api('room/state', { member: room.member, version: room.version }).then(function (res) {
      if (offlineShown) {
        offlineShown = false;
        $('lobby-error').textContent = '';
        say('');
      }
      if (!res.unchanged) renderRoom(res);
    }, roomError).then(function () {
      if (room) schedulePoll();
    });
  }

  /** Joins the card's room with the page token; a reload rejoins the same seat. */
  function joinRoom() {
    stopTimers();
    clearTimeout(pollHandle);
    show('lobby');
    roomStartBtn.hidden = true;
    $('lobby-error').textContent = '';
    $('lobby-wait').textContent = 'Đang vào phòng…';
    return api('room/join', { token: token }).then(function (v) {
      rejoined = false;
      renderRoom(v);
      schedulePoll();
    }, function (err) {
      room = null;
      $('lobby-wait').textContent = '';
      $('lobby-error').textContent = err.message;
      roomStartBtn.textContent = 'Vào lại phòng';
      roomStartBtn.disabled = false;
      roomStartBtn.hidden = err.code === 'bad_token' || err.code === 'token_expired';
    });
  }

  /** @param {{code: string, message: string}} err */
  function roomError(err) {
    if (err.code === 'no_room' && !rejoined) {
      // The room expired or dropped this member; the token still names the card.
      rejoined = true;
      room = null;
      return joinRoom();
    }
    if (err.code === 'no_room') {
      room = null;
      stopTimers();
      show('lobby');
      $('lobby-wait').textContent = '';
      $('lobby-error').textContent = err.message;
      return;
    }
    // A rate-limited move retries on the next tap; a rate-limited start says so.
    if (err.code === 'too_fast' && room && room.status === 'playing') return;
    offlineShown = err.code === 'network';
    if (err.code === 'no_game' || err.code === 'game_running' || err.code === 'not_in_game') {
      schedulePoll(0);
    }
    if (room && room.status === 'playing') {
      say(err.message, 'error');
    } else {
      $('lobby-error').textContent = err.message;
    }
  }

  /**
   * @param {RoomPlayer} p @param {string} tag
   * @returns {HTMLLIElement}
   */
  function playerItem(p, tag) {
    var li = document.createElement('li');
    if (p.you) li.className = 'you';
    var name = document.createElement('span');
    name.className = 'name';
    name.textContent = p.name + (p.you ? ' (bạn)' : '');
    li.appendChild(name);
    if (tag) {
      var t = document.createElement('span');
      t.className = 'tag';
      t.textContent = tag;
      li.appendChild(t);
    }
    return li;
  }

  /** @param {RoomView} v */
  function renderRoom(v) {
    room = v;
    if (v.game !== shownGame) {
      chainList.textContent = '';
      expanded = Object.create(null);
      $('lobby-error').textContent = '';
      shownGame = v.game;
    }
    renderChain(v.chain);
    /** @type {RoomPlayer|null} */
    var me = null;
    v.players.forEach(function (p) { if (p.you) me = p; });
    $('player').textContent = me ? me.name : '';
    if (v.status === 'playing') {
      renderRoomGame(v, me);
    } else {
      renderLobby(v, me);
    }
  }

  /** @param {RoomView} v @param {RoomPlayer|null} me */
  function renderLobby(v, me) {
    stopTimers();
    show('lobby');
    var list = $('lobby-players');
    list.textContent = '';
    v.players.forEach(function (p) {
      list.appendChild(playerItem(p, p.host ? 'chủ phòng' : ''));
    });
    var watchers = $('lobby-watchers');
    watchers.hidden = v.watchers === 0;
    watchers.textContent = 'Thêm ' + v.watchers + ' người đang chờ: mỗi ván có tối đa ' + v.max_players + ' người chơi.';
    var enough = v.players.length >= v.min_players;
    roomStartBtn.textContent = 'Bắt đầu';
    roomStartBtn.hidden = !v.host;
    roomStartBtn.disabled = busy || !enough;
    var wait = '';
    if (v.host && !enough) {
      wait = 'Cần ít nhất ' + v.min_players + ' người. Mời mọi người bấm Chơi trên thẻ trò chơi.';
    } else if (!v.host) {
      wait = me ? 'Chờ chủ phòng bắt đầu…' : 'Phòng đã đủ người chơi; bạn sẽ được xem ván tiếp theo.';
    }
    $('lobby-wait').textContent = wait;
    renderResult(v.result);
  }

  /** @param {RoomResult|null} res */
  function renderResult(res) {
    var box = $('room-result');
    if (!res) {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    $('room-result-title').textContent = res.winner + ' thắng!';
    $('room-result-detail').textContent = 'Chuỗi có ' + res.words + ' từ.' +
      (res.end_reason === 'max_moves' ? ' Chuỗi đã đủ dài, người nhiều điểm nhất thắng.' : '');
    var list = $('room-standings');
    list.textContent = '';
    res.standings.forEach(function (st) {
      var li = document.createElement('li');
      if (st.you) li.className = 'you';
      var why = outText[/** @type {keyof typeof outText} */ (st.out_reason)];
      li.textContent = st.name + (st.you ? ' (bạn)' : '') + ': ' + st.score + ' điểm, ' + st.words + ' từ' +
        (st.bonus > 0 ? ' (thưởng ' + st.bonus + ')' : '') + (why ? ' — ' + why : '');
      list.appendChild(li);
    });
    var report = $('room-report');
    report.textContent = reportText[/** @type {keyof typeof reportText} */ (res.score_reported)] || '';
    report.className = res.score_reported === 'ok' ? 'report-ok' : res.score_reported === 'failed' ? 'report-failed' : 'muted';
  }

  /** @param {RoomView} v @param {RoomPlayer|null} me */
  function renderRoomGame(v, me) {
    var wasMine = !wordInput.disabled && !screens.game.hidden;
    show('game');
    var strip = $('room-players');
    strip.hidden = false;
    strip.textContent = '';
    v.players.forEach(function (p, i) {
      var li = playerItem(p, p.alive ? String(p.score) : (outText[/** @type {keyof typeof outText} */ (p.out_reason)] || 'bị loại'));
      if (i === v.turn) li.classList.add('turn');
      if (!p.alive) li.classList.add('out');
      strip.appendChild(li);
    });
    var mine = v.seat >= 0 && v.seat === v.turn;
    // A new turn clears the last one's message, such as "Hết giờ…"; a
    // re-render within the turn keeps it, such as a rejected word's reason.
    var turnKey = v.game + ':' + v.deadline_ms;
    if (turnKey !== shownTurn) {
      shownTurn = turnKey;
      say('');
    }
    var turnName = v.players[v.turn] ? v.players[v.turn].name : '';
    turnLine.hidden = false;
    turnLine.className = 'turn-line' + (mine ? ' mine' : '');
    if (mine) {
      turnLine.textContent = 'Đến lượt bạn!';
    } else if (!me) {
      turnLine.textContent = 'Bạn đang xem. Lượt của ' + turnName + '.';
    } else if (!me.alive) {
      turnLine.textContent = 'Bạn đã bị loại. Lượt của ' + turnName + '.';
    } else {
      turnLine.textContent = 'Lượt của ' + turnName + '.';
    }
    $('score').textContent = String(me ? me.score : 0);
    $('current').textContent = v.current;
    wordInput.disabled = !mine;
    sendBtn.disabled = busy || !mine;
    giveUpBtn.hidden = !(me && me.alive);
    if (mine && !wasMine) wordInput.focus();
    startCountdown(v, function () { schedulePoll(0); });
  }

  /**
   * Ends a room request: clears busy and re-enables the control the room now
   * needs, which a render during the request left disabled.
   */
  function finishRoomAction() {
    busy = false;
    if (!room) return;
    if (room.status === 'playing') {
      if (room.seat >= 0 && room.seat === room.turn) {
        sendBtn.disabled = false;
        wordInput.focus();
      }
      return;
    }
    roomStartBtn.disabled = room.players.length < room.min_players;
  }

  /** @param {string} word */
  function submitRoomWord(word) {
    if (!room || busy || word === '') return;
    busy = true;
    sendBtn.disabled = true;
    say('');
    api('room/move', { member: room.member, word: word }).then(function (res) {
      // Render first: a late word moves the turn on, and the new turn
      // clears the message.
      renderRoom(res.state);
      if (res.result.accepted) {
        wordInput.value = '';
      } else {
        say(res.result.message || 'Từ không hợp lệ.', 'error');
        wordInput.select();
      }
    }, roomError).then(finishRoomAction);
  }

  function startRoomGame() {
    if (busy) return;
    if (!room) {
      joinRoom();
      return;
    }
    busy = true;
    roomStartBtn.disabled = true;
    $('lobby-error').textContent = '';
    api('room/start', { member: room.member }).then(function (v) {
      say('');
      wordInput.value = '';
      renderRoom(v);
    }, roomError).then(finishRoomAction);
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
  roomStartBtn.addEventListener('click', startRoomGame);
  // A tab that slept (phone locked) has a stale countdown; resync on return.
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) return;
    if (roomMode && room) {
      schedulePoll(0);
    } else if (view && view.status === 'playing') {
      refresh();
    }
  });

  if (!token) {
    show('missing');
  } else if (roomMode) {
    joinRoom();
  } else {
    show('start');
  }
})();
