// Airwaves channel admin. Plain JS, no build step. Talks to /admin/api.
'use strict';

(() => {
  const API = '/admin/api';
  const KINDS = ['series', 'movies', 'collections', 'playlists'];
  const KIND = {
    series: { label: 'Series', one: 'series', many: 'series', empty: 'Shows added to Jellyfin show up here.' },
    movies: { label: 'Movies', one: 'movie', many: 'movies', empty: 'Movies added to Jellyfin show up here.' },
    collections: { label: 'Collections', one: 'collection', many: 'collections', empty: 'Collections made in Jellyfin show up here.' },
    playlists: { label: 'Playlists', one: 'playlist', many: 'playlists', empty: 'Playlists made in Jellyfin show up here.' },
  };
  const CHANNEL_KIND = { weather: 'Weather', folder: 'Folder', jellyfin: 'Jellyfin', youtube: 'YouTube' };
  // Channel details: the server's categories (vchan.Categories) and limits (internal/admin).
  const CATEGORIES = ['Kids', 'Family', 'Movies', 'Sports', 'News', 'Music', 'Pets', 'Gaming', 'Documentary', 'Weather', 'Other'];
  const MAX_CALL = 8;
  const MAX_DESC = 500;
  const CALL_RE = /^[\p{L}\p{N}&+!.'-]+( [\p{L}\p{N}&+!.'-]+)*$/u;
  const LOGO_MAX = 2 << 20;
  const LOGO_TYPES = ['image/png', 'image/jpeg', 'image/svg+xml', 'image/webp'];
  const LOGO_HINT = 'A PNG, JPEG, SVG or WebP image, up to 2 MB. You can also drop one on the tile.';
  // YouTube channels: the server's defaults and limits (internal/admin/youtube.go).
  const YT_DEFAULTS = { minMinutes: 3, maxMinutes: 240, repeatDays: 30, maxHeight: 720, rerunMix: 'balanced', maxAgeDays: 0, deadAir: false };
  const YT_MAX_SOURCES = 10;
  const YT_HEIGHTS = [480, 720, 1080];
  const YT_RERUNS = [
    { v: 'recent', label: 'Mostly recent', help: 'About 70% of reruns come from the last six months of uploads.' },
    { v: 'balanced', label: 'Balanced', help: 'Reruns lean toward recent uploads, with plenty of older ones.' },
    { v: 'any', label: 'Anything', help: 'Every upload is as likely to rerun as any other.' },
  ];
  // What a YouTube channel does once every video has aired within the repeat window.
  const YT_RUNOUT = [
    { v: false, label: 'Cycle reruns', help: 'When every video has aired lately, the one aired longest ago goes next.' },
    { v: true, label: 'Dead air', help: 'No video airs twice within the repeat window. When everything has, the channel stands by until a new upload.' },
  ];
  // yt-dlp lists about 75 videos a second (Northernlion's 22,000 took five minutes).
  const YT_LIST_RATE = 75;
  const YT_POLL = 5000;
  const YT_LINK = /^(https?:\/\/)?((www|m)\.)?(youtube\.com|youtu\.be)\/|^@[^\s/]+$|^UC[\w-]{22}$/i;
  // What a YouTube channel plays: its channels' uploads, or a playlist in its order.
  const YT_MODES = [
    { v: 'uploads', label: 'Uploads from channels', help: "The channels' uploads take turns, new uploads first, with no repeats for a while." },
    { v: 'playlist', label: 'A playlist, in order', help: "Plays the playlist from the top in its order, then again. Airs below can give it a schedule." },
  ];
  // A playlist's link (or a video's in it) has its ID after list=; an ID alone will do too (internal/vchan/ytplaylist.go).
  const YT_PLAYLIST = /[?&]list=[\w-]{2,}|^(PL|UU|UL|FL|OLAK5uy_)[\w-]{10,}$/;
  const ytListId = (s) => { const m = /[?&]list=([\w-]+)/.exec(s) || /^([\w-]{12,})$/.exec(String(s || '').trim()); return m ? m[1] : ''; };
  const ORDERS = [
    { v: 'shuffle', label: 'Shuffle', help: 'Mixes everything picked into one random rotation.' },
    { v: 'aired', label: 'In order', help: "Plays each show's episodes in season order." },
  ];
  // maxBitrate is a cap in Mbps (the server accepts up to 100); 0 means the original file.
  const BITRATES = [0, 4, 8, 12];
  const CHUNK = 120;
  const TOP_GENRES = 10;
  const SCHEDULE_POLLS = [1500, 4000, 8000, 15000];

  const S = {
    state: null, stateErr: null,
    sel: null,          // what the URL points at: a channel number, 'new', or null
    ch: null,           // the channel being shown (null for a new one)
    origNumber: null,   // number used in the PUT path; 'new' until created
    draft: null, base: '',
    touched: {}, serverErr: {}, saveMsg: null, saving: false,
    tab: 'series', filters: null, genresOpen: false,
    lib: {}, view: null,
    sched: { number: null, status: 'idle', programs: [], error: '' },
    items: null,        // a channel's videos in the order they air, for its schedule ({ number, status, list })
    schedToken: 0, refreshing: false,
    acctOpen: false, acctMode: 'code', acctDraft: null, acctMsg: null, acctNote: '', acctBusy: false,
    qc: null, qcToken: 0,
    detailSig: '',
    logoBusy: '', logoMsg: false, // a logo change under way ('upload', 'source', 'remove'), and whether its message shows
    // YouTube: channels found so far, by lowercased page ({ status, data, error }),
    // searches by query, playlists looked up by link as typed, and what the results pane shows.
    yt: { info: new Map(), searches: new Map(), pls: new Map(), view: null, token: 0, timer: 0, wasBusy: false },
  };
  let E = {};

  // ---------- Helpers ----------
  const $ = (sel, root = document) => root.querySelector(sel);

  function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    if (props) {
      for (const [k, v] of Object.entries(props)) {
        if (v == null) continue;
        if (k.startsWith('aria-')) { el.setAttribute(k, String(v)); continue; }
        if (v === false) continue;
        if (k === 'class') el.className = v;
        else if (k === 'text') el.textContent = v;
        else if (k === 'style') Object.assign(el.style, v);
        else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
        else if (k === 'value' || k === 'checked' || k === 'disabled' || k === 'hidden') el[k] = v;
        else el.setAttribute(k, v === true ? '' : v);
      }
    }
    for (const kid of kids.flat(Infinity)) {
      if (kid == null || kid === false || kid === '') continue;
      el.append(kid instanceof Node ? kid : String(kid));
    }
    return el;
  }

  // A Blob body (a logo) goes as it is, with its type; anything else as JSON.
  async function api(method, path, body) {
    const opt = { method, headers: { Accept: 'application/json' } };
    if (body instanceof Blob) {
      opt.headers['Content-Type'] = body.type || 'application/octet-stream';
      opt.body = body;
    } else if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetch(API + path, opt);
    } catch {
      throw Object.assign(new Error("Can't reach the Airwaves server."), { status: 0 });
    }
    if (res.status === 204) return null;
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = null; }
    if (!res.ok) {
      const msg = (data && data.error) || text.trim().slice(0, 200) || `${res.status} ${res.statusText}`;
      throw Object.assign(new Error(msg), { status: res.status });
    }
    return data;
  }

  const enc = (s) => encodeURIComponent(s);
  const fold = (s) => String(s).normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
  const plural = (n, one, many) => `${Number(n).toLocaleString()} ${n === 1 ? one : many}`;
  const numParts = (n) => String(n).split('.').map((x) => parseInt(x, 10) || 0);
  const cmpNum = (a, b) => {
    const x = numParts(a), y = numParts(b);
    return (x[0] - y[0]) || ((x[1] || 0) - (y[1] || 0)) || String(a).localeCompare(String(b));
  };
  const findCh = (n) => (S.state ? S.state.channels.find((c) => c.number === n) : null) || null;
  // A channel number as written ("104.0" stays "104.0"), or '' when it isn't one: 1 to 999, a dot, 0 to 999.
  function parseNumber(n) {
    const m = /^(\d{1,3})\.(\d{1,3})$/.exec(String(n).trim());
    return m && +m[1] >= 1 ? `${m[1]}.${m[2]}` : '';
  }
  // A number's value, to tell whether two are the same however written ("1.05" is 1.5).
  const numKey = (n) => numParts(n).join('.');
  const squash = (s) => String(s || '').trim().replace(/\s+/g, ' ');
  const antennaAt = (n) => ((S.state && S.state.antenna) || []).find((a) => numKey(a.number) === numKey(n)) || null;
  // '#new' asks which kind; '#new-youtube' and '#new-jellyfin' are the editors.
  const isNewTarget = (t) => t === 'new' || t === 'new-youtube' || t === 'new-jellyfin';
  const compact = (n) => Number(n).toLocaleString(undefined, { notation: 'compact', maximumFractionDigits: 2 });

  function fmtTime(t) {
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d)) return '';
    return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  }
  function fmtRuntime(min) {
    min = Math.round(min);
    if (!min) return '';
    const hr = Math.floor(min / 60), m = min % 60;
    return hr ? `${hr}h ${String(m).padStart(2, '0')}m` : `${m} min`;
  }
  function pct(now) {
    const s = Date.parse(now.start), e = Date.parse(now.end), t = Date.now();
    if (!(e > s)) return 0;
    return Math.max(0, Math.min(100, ((t - s) / (e - s)) * 100));
  }
  function hostOf(url) {
    try { return new URL(url).host; } catch { return url || ''; }
  }
  function serverLabel() {
    const a = S.state && S.state.account;
    if (a && a.serverName) return a.serverName;
    if (a && a.server) return hostOf(a.server);
    return 'Jellyfin';
  }
  // The machine the admin page is served from, for "on nas" style hints.
  function hostLabel() {
    const n = location.hostname;
    if (!n || n === 'localhost' || /^[\d.]+$/.test(n) || n.includes(':')) return '';
    return n.split('.')[0];
  }
  const isTyping = (el) => !!(el && el.closest && el.closest('input:not([type=radio]):not([type=checkbox]), textarea, select, [contenteditable]'));

  let toastTimer = 0;
  function toast(msg, err = false) {
    const t = $('#toast');
    t.textContent = msg;
    t.classList.toggle('err', err);
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 3200);
  }

  function confirmDialog({ title, body, ok = 'OK', cancel = 'Cancel', danger = false }) {
    const dlg = $('#dlg');
    $('#dlg-title').textContent = title;
    $('#dlg-body').textContent = body;
    $('#dlg-ok').textContent = ok;
    $('#dlg-cancel').textContent = cancel;
    $('#dlg-ok').className = 'btn' + (danger ? ' danger' : '');
    dlg.classList.toggle('danger', danger);
    dlg.returnValue = '';
    const prev = document.activeElement;
    return new Promise((resolve) => {
      dlg.addEventListener('close', () => {
        const yes = dlg.returnValue === 'ok';
        if (!yes && prev && prev.isConnected) prev.focus();
        resolve(yes);
      }, { once: true });
      dlg.showModal();
      $('#dlg-cancel').focus();
    });
  }

  // ---------- Drafts ----------
  function suggestNumber() {
    const used = new Set((S.state ? S.state.channels : []).map((c) => numKey(c.number)));
    for (let n = 1; n < 1000; n++) if (!used.has(`1.${n}`)) return `1.${n}`;
    return '';
  }
  // Every channel's details: call sign, category, description, and whether it's on the air.
  function metaFrom(ch, kind) {
    return {
      callSign: (ch && ch.callSign) || '',
      category: (ch && ch.category) || (kind === 'weather' ? 'Weather' : 'Other'),
      description: (ch && ch.description) || '',
      enabled: !ch || ch.enabled !== false,
      sched: schedFrom(ch),
    };
  }
  // The schedule rides with the details (it's in channel.json too), on the channels that can have one.
  const metaSig = (d) => [squash(d.callSign), d.category, squash(d.description), d.enabled, schedulable(d) ? schedBody(d.sched) : null];
  function metaBody(d) {
    const b = { callSign: squash(d.callSign), category: d.category, description: squash(d.description), enabled: d.enabled };
    if (schedulable(d)) b.schedule = schedBody(d.sched);
    return b;
  }

  function draftFrom(ch) {
    const c = (ch && ch.config) || {};
    const d = {
      kind: 'jellyfin',
      number: ch ? ch.number : suggestNumber(),
      name: ch ? ch.name || '' : '',
      order: c.order === 'aired' ? 'aired' : 'shuffle',
      maxBitrate: Number(c.maxBitrate) || 0,
      ...metaFrom(ch, 'jellyfin'),
    };
    for (const k of KINDS) d[k] = new Set(Array.isArray(c[k]) ? c[k] : []);
    return d;
  }
  // Folder and weather channels: only the number, name and details are theirs to edit.
  function metaDraftFrom(ch) {
    return { kind: ch.kind, number: ch.number, name: ch.name || '', ...metaFrom(ch, ch.kind) };
  }
  function sig(d) {
    if (d.kind === 'youtube') return ytSig(d);
    if (d.kind === 'folder' || d.kind === 'weather') return JSON.stringify([d.kind, d.number.trim(), d.name.trim(), metaSig(d)]);
    return JSON.stringify([d.number.trim(), d.name.trim(), d.order, d.maxBitrate, KINDS.map((k) => [...d[k]].sort()), metaSig(d)]);
  }

  // A YouTube draft keeps its numbers as typed; sources are channel pages, in order.
  function ytDraftFrom(ch) {
    const y = { ...YT_DEFAULTS, ...((ch && ch.youtube) || {}) };
    return {
      kind: 'youtube',
      number: ch ? ch.number : suggestNumber(),
      name: ch ? ch.name || '' : '',
      sources: Array.isArray(y.channels) ? [...y.channels] : [],
      minMinutes: String(Number(y.minMinutes) || 0),
      maxMinutes: Number(y.maxMinutes) > 0 ? String(y.maxMinutes) : '',
      repeatDays: String(Number(y.repeatDays) || 0),
      maxHeight: Number(y.maxHeight) || YT_DEFAULTS.maxHeight,
      rerunMix: y.rerunMix || YT_DEFAULTS.rerunMix,
      maxAgeDays: Number(y.maxAgeDays) > 0 ? String(y.maxAgeDays) : '',
      deadAir: !!y.deadAir,
      // A playlist instead of sources: playlist is the link in playlist mode, '' otherwise; plLink
      // keeps what's typed across a switch of mode.
      ytMode: y.playlist ? 'playlist' : 'uploads',
      playlist: y.playlist || '',
      plLink: y.playlist || '',
      ...metaFrom(ch, 'youtube'),
    };
  }
  const ytNum = (s) => (String(s).trim() === '' ? NaN : Number(s));
  function ytSig(d) {
    return JSON.stringify(['youtube', d.number.trim(), d.name.trim(), d.ytMode, d.playlist, d.sources.map((s) => s.toLowerCase()),
      ytNum(d.minMinutes), ytNum(d.maxMinutes) || 0, ytNum(d.repeatDays), d.maxHeight, d.rerunMix, ytNum(d.maxAgeDays) || 0, d.deadAir, metaSig(d)]);
  }
  function ytBody(d) {
    // A playlist takes only the quality; the settings for picking uploads stay as they are.
    const youtube = d.ytMode === 'playlist' ? { channels: [], playlist: d.playlist, maxHeight: d.maxHeight } : {
      channels: [...d.sources], playlist: '', minMinutes: ytNum(d.minMinutes), maxMinutes: ytNum(d.maxMinutes) || 0,
      repeatDays: ytNum(d.repeatDays), maxHeight: d.maxHeight, rerunMix: d.rerunMix, maxAgeDays: ytNum(d.maxAgeDays) || 0,
      deadAir: d.deadAir,
    };
    return { kind: 'youtube', number: d.number.trim(), name: d.name.trim(), youtube, ...metaBody(d) };
  }
  // What keeps a YouTube draft from saving: no sources or playlist, or settings out of range.
  function ytProblems(d) {
    const p = { sources: '', settings: '' };
    if (d.ytMode === 'playlist') {
      if (!d.playlist) p.sources = "Paste the playlist's link.";
      else if (!YT_PLAYLIST.test(d.playlist)) p.sources = "That isn't a playlist's link: it has list= in it.";
      return p;
    }
    if (!d.sources.length) p.sources = 'Add a YouTube channel to play.';
    else if (d.sources.length > YT_MAX_SOURCES) p.sources = `Up to ${YT_MAX_SOURCES} YouTube channels.`;
    const lo = ytNum(d.minMinutes), hi = ytNum(d.maxMinutes), rep = ytNum(d.repeatDays);
    if (!(lo >= 0 && lo <= 600)) p.settings = 'The shortest is 0 to 600 minutes.';
    else if (String(d.maxMinutes).trim() !== '' && !(hi > lo && hi <= 1440)) p.settings = 'The longest should be more than the shortest, up to 1,440 minutes.';
    else if (!(rep >= 0 && rep <= 365)) p.settings = 'Repeats are 0 to 365 days apart.';
    else if (String(d.maxAgeDays).trim() !== '' && !(ytNum(d.maxAgeDays) >= 0 && ytNum(d.maxAgeDays) <= 3650)) p.settings = 'Only videos from the last 1 to 3,650 days; leave it empty for any age.';
    return p;
  }
  function bodyOf(d) {
    if (d.kind === 'youtube') return ytBody(d);
    if (d.kind === 'folder' || d.kind === 'weather') return { kind: d.kind, number: d.number.trim(), name: d.name.trim(), ...metaBody(d) };
    const b = { number: d.number.trim(), name: d.name.trim() };
    for (const k of KINDS) b[k] = [...d[k]];
    b.order = d.order;
    b.maxBitrate = d.maxBitrate;
    return Object.assign(b, metaBody(d));
  }
  const isNew = () => S.origNumber === 'new';
  const isDirty = () => !!S.draft && sig(S.draft) !== S.base;
  const newFilters = () => Object.fromEntries(KINDS.map((k) => [k, { q: '', genres: new Set(), onlySel: false }]));

  function problems() {
    const d = S.draft, n = d.number.trim(), num = parseNumber(n), p = { number: '', name: '', taken: false, badName: false, meta: '', antenna: null };
    if (!n) p.number = 'Give it a number, like 1.6.';
    else if (!num) p.number = 'Use a channel number like 1.6, from 1.0 to 999.999.';
    else {
      const other = S.state.channels.find((c) => numKey(c.number) === numKey(num) && c.number !== S.origNumber);
      if (other) { p.number = `${num} is taken by ${other.name}.`; p.taken = true; }
      p.antenna = antennaAt(num);
    }
    const cs = squash(d.callSign);
    if (cs && ([...cs].length > MAX_CALL || !CALL_RE.test(cs))) p.meta = `The call sign is a short label of up to ${MAX_CALL} letters and digits, like TOON.`;
    else if ([...squash(d.description)].length > MAX_DESC) p.meta = `Keep the description to ${MAX_DESC.toLocaleString()} characters.`;
    const name = d.name.trim();
    if (!name) p.name = 'Give the channel a name.';
    else if (/[\/\\]/.test(name) || name.startsWith('.')) { p.name = 'Leave slashes out of the name, and don\'t start it with a dot.'; p.badName = true; }
    if (d.kind === 'youtube') p.yt = ytProblems(d);
    p.sched = schedulable(d) ? schedProblem(d) : '';
    return p;
  }

  // ---------- Boot and state ----------
  function normalizeState(st) {
    st = st || {};
    st.account = st.account || { server: '', user: '', hasPassword: false, ok: false, error: '' };
    st.channels = Array.isArray(st.channels) ? st.channels : [];
    st.channels.sort((a, b) => cmpNum(a.number, b.number));
    st.categories = Array.isArray(st.categories) && st.categories.length ? st.categories : CATEGORIES;
    st.antenna = Array.isArray(st.antenna) ? st.antenna : [];
    return st;
  }

  async function boot() {
    S.stateErr = null;
    renderChannels();
    try {
      S.state = normalizeState(await api('GET', '/state'));
    } catch (e) {
      S.stateErr = e;
      renderChannels();
      $('#main').replaceChildren(h('div', { class: 'empty' },
        h('p', { class: 'empty-line err' }, "Can't load the channels."),
        h('p', { class: 'help' }, e.message),
        h('button', { class: 'btn', type: 'button', onclick: boot }, 'Try again')));
      return;
    }
    renderAccount();
    let t = hashTarget();
    if (!t) {
      // The first channel there is to edit; Jellyfin ones once there's an account.
      const first = S.state.channels.find((c) => (c.kind === 'jellyfin' && S.state.account.server) || c.kind === 'youtube');
      if (first) { t = first.number; history.replaceState(null, '', '#' + enc(t)); }
    }
    openTarget(t);
    if (!boot.timers) {
      boot.timers = true;
      setInterval(refreshState, 60000);
      setInterval(tick, 20000);
      document.addEventListener('visibilitychange', () => { if (!document.hidden && S.state) refreshState(); });
    }
  }

  let stateReq = null;
  function refreshState() {
    if (stateReq) return stateReq;
    stateReq = (async () => {
      try {
        const prevAcct = JSON.stringify(S.state && S.state.account);
        const prevMissing = JSON.stringify(S.ch && S.ch.unmatched);
        S.state = normalizeState(await api('GET', '/state'));
        S.stateErr = null;
        if (S.origNumber && !isNew()) S.ch = findCh(S.origNumber) || S.ch;
        else if (S.sel && !isNewTarget(S.sel)) S.ch = findCh(S.sel);
        renderChannels();
        if (JSON.stringify(S.state.account) !== prevAcct && !S.qc && !acctFormInUse()) renderAccount();
        if (E.yt) {
          ytStateChanged();
        } else if (E.kicker) {
          updateKicker();
          if (E.grid && JSON.stringify(S.ch && S.ch.unmatched) !== prevMissing) renderPicks();
        } else if (S.ch && !isNewTarget(S.sel)) {
          renderDetail(S.ch);
        }
        if (E.logo) { renderLogo(); updateMeta(); }
      } catch {
        // Keep showing what we have; the next refresh tries again.
      } finally {
        stateReq = null;
      }
    })();
    return stateReq;
  }

  // refreshFromElsewhere takes in a change made outside the page (an agent's tool, another tab):
  // the state again, the open channel followed to a new number, and its editor rebuilt from what's
  // saved, unless it has changes of its own, which are kept. hint is { from, to }, the numbers a
  // change moved a channel between, when known; without one, a renamed channel is found as the one
  // of its kind that appeared as it went.
  let elsewhereReq = Promise.resolve();
  function refreshFromElsewhere(hint) {
    elsewhereReq = elsewhereReq.then(() => followElsewhere(hint || {}), () => followElsewhere(hint || {}));
    return elsewhereReq;
  }

  async function followElsewhere(hint) {
    if (!S.state) return refreshState();
    await stateReq;
    const before = S.state.channels;
    const open = S.origNumber && !isNew() ? S.origNumber : S.sel && !isNewTarget(S.sel) ? S.sel : null;
    const was = open ? before.find((c) => c.number === open) : null;
    let st;
    try {
      st = normalizeState(await api('GET', '/state'));
    } catch {
      return; // the next refresh tries again
    }
    const prevAcct = JSON.stringify(S.state.account);
    S.state = st;
    S.stateErr = null;
    if (JSON.stringify(st.account) !== prevAcct && !S.qc && !acctFormInUse()) renderAccount();
    if (!open) {
      renderChannels();
      if (!S.sel) renderEmpty(null);
      if (E.save) updateDirty(); // a new channel's number may be taken now
      return;
    }
    let now = findCh(open) ? open : null;
    if (!now && was) now = movedTo(was, before, hint);
    if (!now) {
      // Gone: deleted elsewhere, or a number the page didn't know yet.
      if (!was) { renderChannels(); if (findCh(open)) openTarget(open); return; }
      S.draft = null;
      history.replaceState(null, '', hashFor(null));
      openTarget(null);
      toast(`${was.number} ${was.name} was deleted elsewhere`);
      return;
    }
    if (now !== open) history.replaceState(null, '', hashFor(now));
    if (S.draft && isDirty()) {
      // Keep the edits, over what was saved elsewhere: what wasn't edited
      // here (the number, say) takes the new value, and the editor is built
      // again from the result, saving to where the channel is now.
      const ch = findCh(now);
      const old = draftFor(was), fresh = draftFor(ch), d = S.draft;
      for (const k of Object.keys(fresh)) if (sameValue(d[k], old[k])) d[k] = fresh[k];
      S.base = sig(fresh);
      S.origNumber = now;
      S.sel = now;
      S.ch = ch;
      if (d.kind === 'youtube') buildYtEditor();
      else if (d.kind === 'jellyfin') buildEditor();
      else buildMetaEditor();
      renderChannels();
      setTitle();
      loadSchedule(++S.schedToken);
      toast(now !== open ? `${open} moved to ${now} elsewhere; your unsaved changes are kept` : 'Changed elsewhere; your unsaved changes are kept');
      return;
    }
    const tab = S.tab;
    openTarget(now);
    if (E.tabs && tab && S.draft && S.draft.kind === 'jellyfin') setTab(tab);
  }

  // draftFor is the draft a saved channel opens with.
  const draftFor = (ch) => (ch.kind === 'youtube' ? ytDraftFrom(ch) : ch.kind === 'jellyfin' ? draftFrom(ch) : metaDraftFrom(ch));
  const sameValue = (a, b) => JSON.stringify(a instanceof Set ? [...a].sort() : a) === JSON.stringify(b instanceof Set ? [...b].sort() : b);

  // movedTo finds where a channel went: the hint's number, or the one channel of its kind that's
  // new to the state (the weather channel by its kind alone).
  function movedTo(was, before, hint) {
    if (hint.to && hint.to !== was.number && (!hint.from || hint.from === was.number) && findCh(hint.to)) return hint.to;
    const known = new Set(before.map((c) => c.number));
    const fresh = S.state.channels.filter((c) => c.kind === was.kind && (was.kind === 'weather' || !known.has(c.number)));
    if (fresh.length === 1) return fresh[0].number;
    const named = fresh.filter((c) => c.name === was.name);
    return named.length === 1 ? named[0].number : null;
  }

  // For the agent tools' page script (webmcp.js), and anything else that changes channels.
  window.airwavesAdmin = { refresh: refreshFromElsewhere };

  function tick() {
    let ended = false;
    for (const i of document.querySelectorAll('.ch-prog i')) {
      const now = { start: i.dataset.start, end: i.dataset.end };
      i.style.width = pct(now) + '%';
      if (Date.parse(now.end) <= Date.now()) ended = true;
    }
    if (ended) refreshState();
    if (E.sched) renderSchedule();
  }

  // ---------- Navigation ----------
  function hashTarget() {
    try { return decodeURIComponent(location.hash.slice(1)) || null; } catch { return null; }
  }
  const hashFor = (t) => (t ? '#' + enc(t) : location.pathname + location.search);

  async function leaveOK() {
    if (!isDirty()) return true;
    const what = isNew() ? 'the new channel' : `${S.ch ? S.ch.number + ' ' + S.ch.name : S.origNumber}`;
    return confirmDialog({
      title: 'Discard changes?',
      body: `Your changes to ${what} haven't been saved.`,
      ok: 'Discard changes', cancel: 'Keep editing', danger: true,
    });
  }

  async function go(t) {
    if (t === S.sel) return;
    if (!(await leaveOK())) return;
    history.pushState(null, '', hashFor(t));
    openTarget(t);
  }

  function linkNav(e) {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    go(e.currentTarget.dataset.number);
  }

  window.addEventListener('popstate', async () => {
    if (!S.state) return;
    const t = hashTarget();
    if (t === S.sel) return;
    if (!(await leaveOK())) { history.pushState(null, '', hashFor(S.sel)); return; }
    openTarget(t);
  });

  window.addEventListener('beforeunload', (e) => {
    if (isDirty()) { e.preventDefault(); e.returnValue = ''; }
  });

  function openTarget(t) {
    S.schedToken++;
    S.refreshing = false;
    S.sel = t;
    E = {};
    S.view = null;
    S.touched = {}; S.serverErr = {}; S.saveMsg = null; S.saving = false;
    S.detailSig = '';
    S.logoMsg = false; S.logoBusy = '';
    clearTimeout(S.yt.timer);
    S.yt.view = null;
    S.yt.wasBusy = false;
    if (t === 'new') {
      S.ch = null;
      S.origNumber = null;
      S.draft = null;
      renderChooser();
    } else if (t === 'new-jellyfin') {
      S.ch = null;
      S.origNumber = 'new';
      S.draft = draftFrom(null);
      S.base = sig(S.draft);
      S.filters = newFilters();
      buildEditor();
    } else if (t === 'new-youtube') {
      S.ch = null;
      S.origNumber = 'new';
      S.draft = ytDraftFrom(null);
      S.base = sig(S.draft);
      buildYtEditor();
    } else if (t === 'backups') {
      S.ch = null;
      S.origNumber = null;
      S.draft = null;
      renderBackupsPage();
    } else {
      const ch = t ? findCh(t) : null;
      S.ch = ch;
      if (ch && ch.kind === 'jellyfin') {
        S.origNumber = ch.number;
        S.draft = draftFrom(ch);
        S.base = sig(S.draft);
        S.filters = newFilters();
        S.tab = KINDS.find((k) => S.draft[k].size) || 'series';
        buildEditor();
        loadSchedule(S.schedToken);
      } else if (ch && ch.kind === 'youtube') {
        S.origNumber = ch.number;
        S.draft = ytDraftFrom(ch);
        S.base = sig(S.draft);
        S.yt.wasBusy = ytBusy(ch);
        buildYtEditor();
        loadSchedule(S.schedToken);
        ytWatch();
      } else if (ch && (ch.kind === 'folder' || ch.kind === 'weather')) {
        S.origNumber = ch.number;
        S.draft = metaDraftFrom(ch);
        S.base = sig(S.draft);
        buildMetaEditor();
        loadSchedule(S.schedToken);
      } else {
        S.origNumber = null;
        S.draft = null;
        if (ch) renderDetail(ch);
        else renderEmpty(t);
      }
    }
    renderChannels();
    setTitle();
  }

  function setTitle() {
    let t = 'Airwaves channels';
    if (isNewTarget(S.sel)) t = 'New channel - ' + t;
    else if (S.sel === 'backups') t = 'Backups - ' + t;
    else if (S.ch) t = `${S.ch.number} ${S.ch.name} - ${t}`;
    document.title = t;
  }

  // ---------- Side: channel list ----------
  function renderChannels() {
    renderBackupLine();
    const nav = $('#channels');
    if (!S.state) {
      if (S.stateErr) {
        nav.replaceChildren(h('div', { class: 'side-msg err' }, "Can't load the channels: ", S.stateErr.message));
      } else {
        nav.replaceChildren(h('p', { class: 'side-msg' }, 'Loading channels'));
      }
      return;
    }
    const focused = document.activeElement && document.activeElement.closest && document.activeElement.closest('#channels [data-number]');
    const focusNum = focused ? focused.dataset.number : null;
    const items = S.state.channels.map(chItem);
    if (isNewTarget(S.sel) && S.draft) items.push(newDraftItem());
    else {
      items.push(h('a', { class: 'ch-add', href: '#new', 'data-number': 'new', 'aria-current': S.sel === 'new' ? 'page' : null, onclick: linkNav },
        h('b', { 'aria-hidden': 'true' }, '+'), 'New channel'));
    }
    nav.replaceChildren(
      h('div', { class: 'chlist-head' }, h('span', { class: 'label' }, 'Lineup'),
        h('span', { class: 'label', text: plural(S.state.channels.length, 'channel', 'channels') })),
      ...items);
    if (focusNum) {
      const el = nav.querySelector(`[data-number="${CSS.escape(focusNum)}"]`);
      if (el) el.focus();
    }
  }

  function chItem(c) {
    const cur = c.number === S.sel;
    const meta = [h('span', { class: 'kind k-' + c.kind, text: CHANNEL_KIND[c.kind] || c.kind })];
    if (c.enabled === false) meta.push(h('span', { class: 'off', title: 'Off the air: not in the lineup or the app', text: 'off' }));
    if (c.antenna) meta.push(h('span', { class: 'ant', title: `Also ${c.number} ${c.antenna} on the antenna; this channel takes its place for HDHomeRun players`, text: `antenna ${c.antenna}` }));
    if (c.kind === 'folder') meta.push(h('span', { text: plural(c.videos || 0, 'video', 'videos') }));
    if (c.kind === 'jellyfin') {
      if (c.items == null && !c.error) meta.push(h('span', { class: 'wait', text: 'loading' }));
      else if (c.items != null) meta.push(h('span', { text: plural(c.items, 'video', 'videos') }));
      if (c.unmatched && c.unmatched.length) meta.push(h('span', { class: 'miss', text: `${c.unmatched.length} missing` }));
    }
    if (c.kind === 'youtube') {
      const busy = ytBusy(c);
      if (!busy || c.videos) meta.push(h('span', { text: plural(c.videos || 0, 'video', 'videos') }));
      if (busy) meta.push(h('span', { class: 'wait', text: 'listing' }));
      const failed = (c.sources || []).filter((s) => s.failed).length;
      if (failed) meta.push(h('span', { class: 'miss', text: `${failed} not listed` }));
    }
    const body = [h('span', { class: 'ch-name' }, c.callSign ? h('b', { class: 'ch-cs', text: c.callSign }) : null, c.name), h('span', { class: 'ch-meta' }, meta)];
    if (c.error) {
      body.push(h('span', { class: 'ch-err', title: c.error, text: c.error }));
    } else if (c.now && c.now.title) {
      body.push(h('span', { class: 'ch-now', title: [c.now.title, c.now.subtitle].filter(Boolean).join(': ') },
        c.now.title, c.now.subtitle ? h('em', null, ', ' + c.now.subtitle) : null));
      if (c.now.start && c.now.end) {
        body.push(h('span', { class: 'ch-prog', 'aria-hidden': 'true' },
          h('i', { style: { width: pct(c.now) + '%' }, 'data-start': c.now.start, 'data-end': c.now.end })));
      }
    }
    return h('a', {
      class: 'ch' + (cur && isDirty() ? ' dirty' : '') + (c.enabled === false ? ' is-off' : ''), href: '#' + enc(c.number), 'data-number': c.number,
      'aria-current': cur ? 'page' : null, onclick: linkNav,
    },
    h('span', { class: 'ch-num', text: c.number }),
    h('span', { class: 'ch-body' }, body),
    h('span', { class: 'ch-dirty', title: 'Unsaved changes' }));
  }

  function newDraftItem() {
    return h('a', { class: 'ch', href: '#' + S.sel, 'data-number': S.sel, 'aria-current': 'page', onclick: linkNav },
      h('span', { class: 'ch-num', text: S.draft.number.trim() || '?' }),
      h('span', { class: 'ch-body' },
        h('span', { class: 'ch-name', text: S.draft.name.trim() || 'New channel' }),
        h('span', { class: 'ch-meta' }, h('span', { class: 'kind k-new' }, 'Not created yet'))));
  }

  $('#channels').addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const links = [...$('#channels').querySelectorAll('a[data-number]')];
    const i = links.indexOf(document.activeElement);
    if (i < 0) return;
    e.preventDefault();
    const n = links[Math.max(0, Math.min(links.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))];
    n.focus();
  });

  // ---------- Side: account ----------
  function acctFormInUse() {
    const box = $('#account');
    return box.contains(document.activeElement) && isTyping(document.activeElement);
  }

  function renderAccount() {
    const box = $('#account');
    if (!S.state) { box.replaceChildren(); return; }
    const a = S.state.account;
    const configured = !!a.server;
    const failing = configured && !a.ok;
    const open = S.acctOpen || !configured || failing || !!S.qc;
    const hadFocus = box.contains(document.activeElement);

    if (!open) {
      box.replaceChildren(h('div', { class: 'acct-line' },
        h('span', { class: 'dot', 'aria-hidden': 'true' }),
        h('span', { class: 'who' },
          h('b', { text: a.serverName || hostOf(a.server) }),
          h('span', { text: `${a.user} on ${hostOf(a.server)}` })),
        h('button', { class: 'btn ghost small', type: 'button', id: 'acct-change', onclick: () => openAccount() }, 'Change'),
        S.acctNote ? h('p', { class: 'acct-note', role: 'status', text: S.acctNote }) : null));
      if (hadFocus) $('#acct-change').focus();
      return;
    }

    const d = S.acctDraft || (S.acctDraft = { server: a.server || '', user: a.user || '' });
    let cls = 'acct-box', title = 'Jellyfin account', lead;
    if (!configured) {
      cls += ' setup';
      title = 'Connect Jellyfin';
      lead = h('p', { class: 'lead' }, 'Sign in to a Jellyfin server to build channels from its library.');
    } else if (failing) {
      cls += ' fail';
      title = "Can't sign in";
      lead = h('p', { class: 'lead err' }, h('b', { text: a.error || 'The saved sign-in stopped working.' }),
        ' Sign in again to keep the Jellyfin channels playing.');
    } else {
      lead = h('p', { class: 'lead' }, `Signed in to ${a.serverName || hostOf(a.server)} as ${a.user}.`);
    }

    const msg = h('p', { class: 'acct-msg' + (S.acctMsg ? ' ' + S.acctMsg.cls : ''), id: 'acct-msg', role: 'status', text: S.acctMsg ? S.acctMsg.text : '' });
    const cancel = configured && a.ok
      ? h('button', { class: 'btn ghost', type: 'button', onclick: closeAccount }, 'Cancel')
      : null;

    let body;
    if (S.qc) {
      body = qcView();
    } else {
      const server = h('label', { class: 'field' }, h('span', null, 'Server'),
        h('input', {
          class: 'input', name: 'server', id: 'acct-server', value: d.server, placeholder: 'https://jellyfin.example.com',
          inputmode: 'url', autocomplete: 'url', spellcheck: 'false', autocapitalize: 'off',
          oninput: (e) => { d.server = e.target.value; },
        }));
      if (S.acctMode === 'code') {
        body = h('form', { class: 'acct-form', onsubmit: (e) => { e.preventDefault(); startQC(); } },
          server,
          h('div', { class: 'acct-actions' },
            h('button', { class: 'btn', type: 'submit', id: 'acct-submit', disabled: S.acctBusy }, S.acctBusy ? 'Getting a code' : 'Sign in with a code'),
            cancel),
          msg,
          h('p', { class: 'help' }, 'Or ', h('button', { class: 'linkish', type: 'button', onclick: () => setAcctMode('password') }, 'use a password instead'), '.'));
      } else {
        body = h('form', { class: 'acct-form', onsubmit: saveAccount },
          server,
          h('label', { class: 'field' }, h('span', null, 'User'),
            h('input', {
              class: 'input', name: 'user', id: 'acct-user', value: d.user, autocomplete: 'username', spellcheck: 'false', autocapitalize: 'off',
              oninput: (e) => { d.user = e.target.value; },
            })),
          h('label', { class: 'field' }, h('span', null, 'Password'),
            h('input', {
              class: 'input', name: 'password', id: 'acct-password', type: 'password', autocomplete: 'current-password',
              placeholder: a.hasPassword ? 'saved' : '', 'aria-describedby': a.hasPassword ? 'acct-pw-help' : null,
            })),
          a.hasPassword ? h('p', { class: 'help', id: 'acct-pw-help' }, 'Leave it empty to keep the saved one.') : null,
          h('div', { class: 'acct-actions' },
            h('button', { class: 'btn', type: 'submit', id: 'acct-submit', disabled: S.acctBusy }, S.acctBusy ? 'Testing' : 'Save and test'),
            cancel),
          msg,
          h('p', { class: 'help' }, 'Or ', h('button', { class: 'linkish', type: 'button', onclick: () => setAcctMode('code') }, 'sign in with a code'), '.'));
      }
    }
    box.replaceChildren(h('div', { class: cls }, h('h2', null, title), lead, body));
    if (hadFocus) {
      const f = box.querySelector('#qc-code, #acct-submit:not([disabled]), input');
      if (f) f.focus();
    }
  }

  function qcView() {
    const q = S.qc;
    const kids = [
      h('span', { class: 'label' }, `Code for ${hostOf(q.server)}`),
      h('div', { class: 'qc-code', id: 'qc-code', tabindex: '-1', 'aria-label': 'Code ' + q.code.split('').join(' ') }, q.code),
      h('p', { class: 'help' }, "In a Jellyfin app you're signed in to, open Quick Connect (in your user settings) and enter this code."),
    ];
    const actions = [];
    if (q.state === 'waiting') {
      kids.push(h('p', { class: 'qc-wait', role: 'status' }, 'Waiting for approval'));
    } else if (q.state === 'expired') {
      kids.push(h('p', { class: 'acct-msg err', role: 'status' }, 'That code expired.'));
      actions.push(h('button', { class: 'btn', type: 'button', onclick: () => startQC(q.server) }, 'Get a new code'));
    } else {
      kids.push(h('p', { class: 'acct-msg err', role: 'status' }, `Lost track of the sign-in: ${q.error}`));
      actions.push(h('button', { class: 'btn', type: 'button', onclick: () => startQC(q.server) }, 'Get a new code'));
    }
    actions.push(h('button', { class: 'btn ghost', type: 'button', onclick: cancelQC }, 'Cancel'));
    return h('div', { class: 'qc' + (q.state === 'waiting' ? '' : ' stale') }, kids, h('div', { class: 'acct-actions' }, actions));
  }

  function openAccount(mode) {
    S.acctOpen = true;
    if (mode) S.acctMode = mode;
    S.acctNote = '';
    renderAccount();
    const el = $('#account input, #account button');
    if (el) { el.focus(); $('#account').scrollIntoView({ block: 'nearest' }); }
  }
  function closeAccount() {
    S.acctOpen = false; S.acctDraft = null; S.acctMsg = null;
    cancelQC(true);
    renderAccount();
  }
  function setAcctMode(m) {
    S.acctMode = m; S.acctMsg = null;
    renderAccount();
    const el = m === 'password' ? ($('#acct-server').value ? $('#acct-user') : $('#acct-server')) : $('#acct-submit');
    if (el) el.focus();
  }
  function setAcctMsg(text, cls) {
    S.acctMsg = text ? { text, cls } : null;
    const m = $('#acct-msg');
    if (m) { m.textContent = text || ''; m.className = 'acct-msg' + (cls ? ' ' + cls : ''); }
  }

  async function startQC(server) {
    server = (server || (S.acctDraft && S.acctDraft.server) || '').trim();
    if (!server) {
      setAcctMsg('Enter the server address first.', 'err');
      const f = $('#acct-server');
      if (f) f.focus();
      return;
    }
    S.acctBusy = true;
    S.acctMsg = null;
    if (!S.qc) renderAccount();
    try {
      const r = await api('POST', '/account/quickconnect', { server });
      S.qc = { id: r.id, code: String(r.code), server, state: 'waiting', fails: 0, token: ++S.qcToken };
      if (S.acctDraft) S.acctDraft.server = server;
      S.acctBusy = false;
      renderAccount();
      const c = $('#qc-code');
      if (c) c.focus();
      pollQC(S.qc.token);
    } catch (e) {
      S.acctBusy = false;
      S.qc = null;
      S.acctMsg = { text: e.message, cls: 'err' };
      renderAccount();
    }
  }

  function pollQC(token) {
    setTimeout(async () => {
      const q = S.qc;
      if (!q || q.token !== token || q.state !== 'waiting') return;
      try {
        const r = await api('GET', `/account/quickconnect/${enc(q.id)}`);
        if (S.qc !== q) return;
        q.fails = 0;
        if (r && r.state === 'done' && r.account) { accountSaved(r.account); return; }
        if (r && r.state === 'expired') { q.state = 'expired'; renderAccount(); return; }
        pollQC(token);
      } catch (e) {
        if (S.qc !== q) return;
        if (e.status === 404 || e.status === 410) { q.state = 'expired'; renderAccount(); return; }
        if (++q.fails >= 4) { q.state = 'error'; q.error = e.message; renderAccount(); return; }
        pollQC(token);
      }
    }, 2000);
  }

  function cancelQC(quiet) {
    S.qc = null;
    S.qcToken++;
    if (quiet !== true) renderAccount();
  }

  async function saveAccount(e) {
    e.preventDefault();
    const f = e.currentTarget;
    const body = { server: f.server.value.trim(), user: f.user.value.trim(), password: f.password.value };
    if (!body.server || !body.user) {
      setAcctMsg(!body.server ? 'Enter the server address.' : 'Enter the user name.', 'err');
      (body.server ? f.user : f.server).focus();
      return;
    }
    S.acctBusy = true;
    const btn = $('#acct-submit');
    btn.disabled = true;
    btn.textContent = 'Testing';
    setAcctMsg('Signing in to ' + hostOf(body.server), '');
    try {
      const acct = await api('PUT', '/account', body);
      S.acctBusy = false;
      accountSaved(acct);
    } catch (err) {
      S.acctBusy = false;
      btn.disabled = false;
      btn.textContent = 'Save and test';
      setAcctMsg(err.message, 'err');
    }
  }

  let noteTimer = 0;
  function accountSaved(acct) {
    const before = S.state.account;
    S.state.account = acct || before;
    S.qc = null;
    S.qcToken++;
    if (acct && acct.ok) {
      S.acctOpen = false;
      S.acctDraft = null;
      S.acctMsg = null;
      S.acctNote = `Signed in to ${acct.serverName || hostOf(acct.server)} as ${acct.user}.`;
      clearTimeout(noteTimer);
      noteTimer = setTimeout(() => { S.acctNote = ''; if (!S.acctOpen && !S.qc) renderAccount(); }, 15000);
      renderAccount();
      toast(S.acctNote);
      // A different server or user means a different library.
      S.lib = {};
      if (E.grid) { loadLib(S.tab); renderFilters(); renderGrid(); updateLibStatus(); renderPicks(); }
      else if (!S.sel) renderEmpty(null);
      refreshState();
    } else {
      // The box headline already shows the account's error; only fill in when there isn't one.
      S.acctMsg = acct && acct.error ? null : { text: "Couldn't sign in.", cls: 'err' };
      renderAccount();
    }
  }

  // ---------- Main: empty and read-only views ----------
  function renderEmpty(missing) {
    const a = S.state.account;
    const kids = [];
    if (missing) {
      kids.push(h('p', { class: 'empty-line' }, `There's no channel ${missing} in the lineup.`));
    } else if (!a.server) {
      kids.push(h('p', { class: 'empty-line' }, 'Connect a Jellyfin account, then build channels from its library.'),
        h('p', { class: 'help' }, 'Sign in on the left. A Quick Connect code is the fastest way. Channels of YouTube uploads need no account.'));
    } else if (!S.state.channels.some((c) => c.kind === 'jellyfin' || c.kind === 'youtube')) {
      kids.push(h('p', { class: 'empty-line' }, `No channels of your own yet. Make one from YouTube channels or ${serverLabel()}'s library.`));
    } else {
      kids.push(h('p', { class: 'empty-line' }, 'Pick a channel on the left, or make a new one.'));
    }
    kids.push(!a.server && !missing
      ? h('div', { class: 'row' },
        h('button', { class: 'btn', type: 'button', onclick: () => openAccount() }, 'Sign in'),
        h('button', { class: 'btn ghost', type: 'button', onclick: () => go('new-youtube') }, 'New YouTube channel'))
      : h('button', { class: 'btn', type: 'button', onclick: () => go('new') }, 'New channel'));
    $('#main').replaceChildren(h('div', { class: 'empty' }, kids));
  }

  // New channel: which kind.
  function renderChooser() {
    const a = S.state.account;
    const card = (t, kind, title, body, note) => h('a', { class: 'kindcard', href: '#' + t, 'data-number': t, onclick: linkNav },
      h('span', { class: 'kind k-' + kind, text: CHANNEL_KIND[kind] }),
      h('b', null, title), h('p', null, body), note ? h('small', null, note) : null);
    $('#main').replaceChildren(h('div', { class: 'chooser' },
      h('h1', null, 'New channel'),
      h('p', { class: 'help' }, 'What should it play?'),
      h('div', { class: 'kinds' },
        card('new-youtube', 'youtube', 'Uploads from YouTube channels',
          'Pick one or more channels, like Boiler Room for DJ sets. Their uploads take turns, and new ones air first. Or play a playlist in order.',
          'Streams from YouTube as it airs; nothing is downloaded.'),
        card('new-jellyfin', 'jellyfin', `From ${a.server ? serverLabel() + "'s" : 'a Jellyfin'} library`,
          'Series, movies, collections and playlists, shuffled or in order.',
          a.server ? '' : 'Needs a Jellyfin account; connect one on the left.'))));
    const first = $('#main .kindcard');
    if (first) first.focus();
  }

  // A channel of a kind this page doesn't know.
  function renderDetail(ch) {
    const sigNow = JSON.stringify(ch);
    if (sigNow === S.detailSig) return;
    S.detailSig = sigNow;
    $('#main').replaceChildren(h('article', { class: 'detail' },
      h('header', { class: 'detail-head' },
        h('div', { class: 'detail-num', text: ch.number }),
        h('h1', { class: 'detail-name', text: ch.name }),
        h('div', { class: 'detail-kicker' }, h('span', { class: 'kind k-' + ch.kind, text: CHANNEL_KIND[ch.kind] || ch.kind }))),
      h('p', null, 'This channel has nothing to edit here.')));
  }

  // ---------- Main: Jellyfin editor ----------
  function radios(name, opts, cur, onchange, label) {
    return h('div', { class: 'seg', role: 'radiogroup', 'aria-label': label },
      opts.map((o) => h('label', null,
        h('input', { type: 'radio', name, value: String(o.v), checked: o.v === cur, onchange }),
        h('span', { text: o.label }))));
  }
  function bitrateOpts(cur) {
    const list = [...BITRATES];
    if (cur && !list.includes(cur)) list.push(cur);
    list.sort((a, b) => a - b);
    return list.map((v) => ({ v, label: v ? `${v} Mbps` : 'Original' }));
  }

  // The number, name, actions and kicker both editors share.
  function editorHead() {
    const d = S.draft;
    const fresh = isNew();
    E.num = h('input', {
      class: 'in-num', id: 'f-number', value: d.number, autocomplete: 'off', spellcheck: 'false', inputmode: 'decimal',
      'aria-describedby': 'f-number-err', oninput: onField, onblur: () => { S.touched.number = true; updateDirty(); },
    });
    E.name = h('input', {
      class: 'in-name', id: 'f-name', value: d.name, placeholder: 'Name this channel', autocomplete: 'off', maxlength: '48',
      'aria-describedby': 'f-name-err', oninput: onField, onblur: () => { S.touched.name = true; updateDirty(); },
    });
    E.numErr = h('p', { class: 'f-err', id: 'f-number-err' });
    E.nameErr = h('p', { class: 'f-err', id: 'f-name-err' });
    E.status = h('span', { class: 'ed-status', role: 'status' });
    E.del = h('button', { class: 'btn ghost danger', type: 'button', hidden: fresh || d.kind === 'folder' || d.kind === 'weather', onclick: deleteChannel }, 'Delete');
    E.save = h('button', { class: 'btn', type: 'button', onclick: save, 'aria-keyshortcuts': 'Meta+S Control+S' }, fresh ? 'Create channel' : 'Save changes');
    E.kicker = h('div', { class: 'ed-kicker' });

    return h('header', { class: 'ed-head' },
      h('div', { class: 'f-num' }, h('label', { class: 'label', for: 'f-number' }, 'Number'), E.num, E.numErr),
      h('div', { class: 'f-name' }, h('label', { class: 'label', for: 'f-name' }, 'Name'), E.name, E.nameErr),
      h('div', { class: 'ed-actions' }, E.status, E.del, E.save),
      E.kicker);
  }

  // ---------- Main: details (every kind) ----------
  // Call sign, category, description, on the air, and the logo, at the top of every channel's page.
  function metaSection() {
    const d = S.draft;
    const field = (label, id, input, cls) => h('div', { class: 'mfield ' + cls }, h('label', { class: 'label', for: id }, label), input);
    E.call = h('input', {
      class: 'input', id: 'f-call', value: d.callSign, autocomplete: 'off', spellcheck: 'false', maxlength: String(MAX_CALL + 4),
      placeholder: d.kind === 'weather' ? 'WX' : 'TOON', 'aria-describedby': 'f-meta-err',
      oninput: (e) => { S.draft.callSign = e.target.value; S.serverErr.meta = ''; changed(); renderLogo(); },
    });
    const cats = (S.state && S.state.categories) || CATEGORIES;
    E.cat = h('select', {
      class: 'input select', id: 'f-cat', onchange: (e) => { S.draft.category = e.target.value; changed(); },
    }, cats.map((c) => h('option', { value: c }, c)));
    E.cat.value = cats.includes(d.category) ? d.category : 'Other';
    E.on = h('input', {
      type: 'checkbox', role: 'switch', id: 'f-on', checked: d.enabled, 'aria-label': 'On the air', 'aria-describedby': 'f-on-help',
      onchange: (e) => { S.draft.enabled = e.target.checked; changed(); updateMeta(); },
    });
    E.onHelp = h('span', { class: 'sw-help', id: 'f-on-help' });
    E.desc = h('textarea', {
      class: 'input', id: 'f-desc', rows: '2', maxlength: String(MAX_DESC + 50), 'aria-describedby': 'f-meta-err',
      placeholder: 'What the channel plays. Shown in guides, and when nothing is listed.',
      oninput: (e) => { S.draft.description = e.target.value; S.serverErr.meta = ''; changed(); fitDesc(); },
    });
    E.desc.value = d.description;
    E.metaErr = h('p', { class: 'f-err meta-err', id: 'f-meta-err', role: 'alert' });
    E.antenna = h('p', { class: 'meta-warn', id: 'f-antenna', role: 'status' });

    E.logo = h('div', {
      class: 'logo-tile', id: 'logo-tile', role: 'img', title: LOGO_HINT,
      ondragover: (e) => { if (canLogo()) { e.preventDefault(); E.logo.classList.add('drop'); } },
      ondragleave: () => E.logo.classList.remove('drop'),
      ondrop: (e) => {
        E.logo.classList.remove('drop');
        if (!canLogo()) return;
        e.preventDefault();
        const f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
        if (f) uploadLogo(f);
      },
    });
    E.logoFile = h('input', {
      type: 'file', id: 'logo-file', class: 'visually-hidden', accept: LOGO_TYPES.join(','), tabindex: '-1',
      onchange: (e) => { const f = e.target.files[0]; e.target.value = ''; if (f) uploadLogo(f); },
    });
    E.logoUp = h('button', { class: 'btn small ghost', type: 'button', id: 'logo-upload', title: LOGO_HINT, onclick: () => E.logoFile.click() }, 'Upload');
    E.logoSrc = h('button', { class: 'linkish', type: 'button', id: 'logo-source', onclick: logoFromSource });
    E.logoDel = h('button', { class: 'linkish', type: 'button', id: 'logo-remove', onclick: removeLogo }, 'Remove');
    E.logoMsg = h('p', { class: 'logo-msg', role: 'status' });
    const box = h('div', { class: 'logo-box' }, E.logo,
      h('div', { class: 'logo-actions' }, h('span', { class: 'label' }, 'Logo'), E.logoUp, E.logoSrc, E.logoDel, E.logoFile, E.logoMsg));

    const sec = h('section', { class: 'ed-meta', 'aria-label': 'Channel details' }, box,
      h('div', { class: 'mfields' },
        field('Call sign', 'f-call', E.call, 'call'),
        field('Category', 'f-cat', E.cat, 'cat'),
        h('div', { class: 'mfield on' }, h('span', { class: 'label' }, 'On the air'),
          h('label', { class: 'switch' }, E.on, h('i', { 'aria-hidden': 'true' }), E.onHelp)),
        field('Description', 'f-desc', E.desc, 'desc'),
        E.metaErr, E.antenna));
    S.logoBusy = '';
    renderLogo();
    updateMeta();
    requestAnimationFrame(fitDesc);
    return sec;
  }

  // The description grows to three lines as it's typed.
  function fitDesc() {
    if (!E.desc || !E.desc.isConnected) return;
    E.desc.style.height = 'auto';
    E.desc.style.height = Math.min(E.desc.scrollHeight + 2, 84) + 'px';
  }

  // The channel's logo as saved; a new channel gets one once it's created.
  const canLogo = () => !!(S.draft && !isNew() && S.ch && !S.logoBusy);
  function renderLogo() {
    if (!E.logo) return;
    const ch = S.ch, d = S.draft;
    const label = squash(d.callSign) || squash(d.name) || '?';
    E.logo.replaceChildren();
    E.logo.classList.toggle('has', !!(ch && ch.logo));
    if (ch && ch.logo) {
      E.logo.append(h('img', { src: ch.logo, alt: '', draggable: 'false', onerror: (e) => e.target.remove() }));
      E.logo.setAttribute('aria-label', `${ch.name}'s logo`);
    } else {
      E.logo.append(h('span', { class: 'ph-call', 'aria-hidden': 'true', text: [...label].slice(0, MAX_CALL).join('') }));
      E.logo.setAttribute('aria-label', 'No logo yet');
    }
    const fresh = isNew(), busy = !!S.logoBusy;
    E.logoUp.disabled = fresh || busy;
    E.logoUp.textContent = S.logoBusy === 'upload' ? 'Uploading' : (ch && ch.logo ? 'Replace' : 'Upload');
    E.logoDel.hidden = fresh || !(ch && ch.logo);
    E.logoDel.disabled = busy;
    let src = '';
    if (!fresh && d.kind === 'youtube' && ch && ch.youtube && ((ch.youtube.channels || []).length || ch.youtube.playlist)) src = 'Use the YouTube avatar';
    if (!fresh && d.kind === 'jellyfin' && S.state.account.server && ch && ch.config && KINDS.some((k) => (ch.config[k] || []).length)) src = `Use a poster from ${serverLabel()}`;
    E.logoSrc.hidden = !src;
    E.logoSrc.disabled = busy;
    E.logoSrc.textContent = S.logoBusy === 'source' ? 'Fetching the picture' : src;
    if (fresh) setLogoMsg('Add a logo once the channel is created.');
    else if (!S.logoMsg) setLogoMsg('');
  }
  function setLogoMsg(text, cls) {
    if (!E.logoMsg) return;
    E.logoMsg.textContent = text || '';
    E.logoMsg.className = 'logo-msg' + (cls ? ' ' + cls : '');
  }

  async function logoCall(kind, method, path, body) {
    if (!canLogo()) return;
    const num = S.origNumber;
    S.logoBusy = kind;
    S.logoMsg = true;
    setLogoMsg(kind === 'source' ? 'Fetching the picture' : kind === 'upload' ? 'Uploading' : 'Removing', '');
    renderLogo();
    try {
      const ch = await api(method, `/channels/${enc(num)}${path}`, body);
      S.logoBusy = '';
      if (S.origNumber !== num || !ch) return;
      logoSaved(ch);
      setLogoMsg(kind === 'remove' ? 'Logo removed.' : 'Logo saved.', 'ok');
    } catch (e) {
      S.logoBusy = '';
      if (S.origNumber !== num) return;
      setLogoMsg(e.message, 'err');
    }
    renderLogo();
  }
  // The server's answer to a logo change: the channel as saved, so only the logo is taken from it.
  function logoSaved(ch) {
    const list = S.state.channels;
    const i = list.findIndex((c) => c.number === S.origNumber);
    const merged = Object.assign({}, S.ch, { logo: ch.logo || undefined });
    if (!ch.logo) delete merged.logo;
    if (i >= 0) list[i] = merged;
    S.ch = merged;
    renderChannels();
  }
  function uploadLogo(file) {
    if (file.type && !LOGO_TYPES.includes(file.type)) { setLogoMsg('The logo should be a PNG, JPEG, SVG or WebP image.', 'err'); S.logoMsg = true; return; }
    if (file.size > LOGO_MAX) { setLogoMsg('The logo should be under 2 MB.', 'err'); S.logoMsg = true; return; }
    logoCall('upload', 'PUT', '/logo', file);
  }
  const logoFromSource = () => logoCall('source', 'POST', '/logo/source');
  async function removeLogo() {
    const ok = await confirmDialog({
      title: 'Remove the logo?', body: 'Guides show the call sign or name in its place.', ok: 'Remove logo', danger: true,
    });
    if (ok) logoCall('remove', 'DELETE', '/logo');
  }

  // What the details say beside the fields: on the air or not, and an antenna channel with the same number.
  function updateMeta() {
    if (!E.onHelp) return;
    const d = S.draft;
    E.onHelp.textContent = d.enabled ? 'In the lineup and the app' : 'Off: hidden from the lineup and the app';
    const p = problems();
    const a = p.antenna;
    E.antenna.textContent = a
      ? `${a.number} is also ${a.name} on the antenna. This channel takes its place for HDHomeRun players.`
      : '';
  }

  function syncMetaInputs() {
    const d = S.draft;
    if (E.call.value !== d.callSign) E.call.value = d.callSign;
    if (E.desc.value !== d.description) E.desc.value = d.description;
    E.cat.value = d.category;
    E.on.checked = d.enabled;
    updateMeta();
    renderLogo();
  }

  // ---------- Main: when it airs (folder, Jellyfin and YouTube playlist channels) ----------
  // A channel that plays in order loops around the clock, or airs on a schedule (vchan.Schedule,
  // in channel.json): from a start date, beginning with one of its videos, in daily blocks of so
  // many videos or until a time, off the air between.
  const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
  const DAY_NAMES = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
  const MAX_BLOCKS = 12;
  const AIRS = [
    { v: false, label: 'Around the clock' },
    { v: true, label: 'On a schedule' },
  ];
  const schedulable = (d) => !!d && (d.kind === 'folder' || d.kind === 'jellyfin' || (d.kind === 'youtube' && d.ytMode === 'playlist'));
  const isoDay = (t) => `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')}`;
  const CLOCK_RE = /^([01]?\d|2[0-3]):([0-5]\d)$/;
  const minutesOf = (hm) => { const m = CLOCK_RE.exec(hm || ''); return m ? +m[1] * 60 + +m[2] : -1; };

  // The draft's schedule from a channel's: null around the clock. Each block keeps both ends as typed.
  function schedFrom(ch) {
    const s = ch && ch.schedule;
    if (!s) return null;
    const [date, time] = String(s.start || '').split('T');
    return {
      start: date || '', time: time || '', first: Math.max(1, Math.floor(Number(s.first) || 1)),
      blocks: (Array.isArray(s.blocks) ? s.blocks : []).map((b) => {
        const days = (Array.isArray(b.days) ? b.days : []).map((x) => String(x).trim().toLowerCase());
        return {
          days: DAYS.map((x) => !days.length || days.includes(x)),
          at: b.at || '', end: b.until ? 'until' : 'count', count: b.until ? '4' : String(b.count || ''), until: b.until || '',
        };
      }),
    };
  }
  // A draft's schedule as the server takes it: every day is no days, and first 1 is left out.
  function schedBody(s) {
    if (!s) return null;
    const out = { start: s.time ? `${s.start}T${s.time}` : s.start };
    if (s.first > 1) out.first = s.first;
    out.blocks = s.blocks.map((b) => {
      const o = {};
      if (!b.days.every(Boolean)) o.days = DAYS.filter((_, k) => b.days[k]);
      o.at = b.at;
      if (b.end === 'until') o.until = b.until;
      else o.count = String(b.count).trim() === '' ? 0 : Number(b.count);
      return o;
    });
    return out;
  }
  function newSched(d) {
    const many = schedNoun(d) === 'movie' ? '2' : '4';
    return { start: isoDay(new Date()), time: '', first: 1, blocks: [{ days: DAYS.map(() => true), at: '20:00', end: 'count', count: many, until: '' }] };
  }

  // What keeps a schedule from saving, as the server would say it (vchan.Schedule.Check), or ''.
  function schedProblem(d) {
    const s = d.sched;
    if (!s) return '';
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s.start || '');
    const day = m && new Date(+m[1], +m[2] - 1, +m[3]);
    if (!day || day.getMonth() !== +m[2] - 1) return 'Pick the day it starts.';
    if (s.time && minutesOf(s.time) < 0) return 'Give the start a time like 20:00, or none.';
    if (s.blocks.length > MAX_BLOCKS) return `Up to ${MAX_BLOCKS} blocks a day.`;
    const many = schedNoun(d) + 's';
    for (const [i, b] of s.blocks.entries()) {
      const n = s.blocks.length > 1 ? `Block ${i + 1}` : 'The block';
      if (!b.days.some(Boolean)) return `${n} needs a day to air.`;
      if (minutesOf(b.at) < 0) return `${n} needs a time to start, like 20:00.`;
      if (b.end === 'until' && minutesOf(b.until) < 0) return `${n} needs a time to stop, like 23:30.`;
      const c = Number(b.count);
      if (b.end !== 'until' && !(Number.isInteger(c) && c >= 1 && c <= 100)) return `${n} airs 1 to 100 ${many}.`;
    }
    return '';
  }

  // The videos as listed for this channel, once loaded.
  const loadedItems = () => (S.items && S.items.number === S.origNumber && S.items.status === 'ok' ? S.items.list : null);
  function loadItems() {
    const num = S.origNumber;
    if (!num || num === 'new' || !E.schedEd || !S.draft || !S.draft.sched) return;
    if (S.items && S.items.number === num && S.items.status !== 'error') return;
    S.items = { number: num, status: 'loading', list: [] };
    renderFirstPicker();
    api('GET', `/channels/${enc(num)}/items`).then((r) => {
      if (S.items && S.items.number === num) S.items = { number: num, status: 'ok', list: Array.isArray(r && r.items) ? r.items : [] };
    }, (e) => {
      if (S.items && S.items.number === num) S.items = { number: num, status: 'error', list: [], error: e.message };
    }).then(() => {
      if (S.origNumber !== num || !E.schFirst) return;
      renderFirstPicker();
      updateSched();
    });
  }

  // What the channel's videos are called: episodes, movies, or videos.
  function schedNoun(d) {
    const list = loadedItems();
    if (list && list.length) {
      if (list.every((it) => it.episode)) return 'episode';
      if (d.kind === 'jellyfin' && list.every((it) => !it.episode)) return 'movie';
      return 'video';
    }
    if (d.kind === 'jellyfin') {
      const only = (k) => d[k] && d[k].size && KINDS.every((o) => o === k || !d[o].size);
      if (only('series')) return 'episode';
      if (only('movies')) return 'movie';
    }
    return 'video';
  }
  // A video as the picker and the summary name it: "S1 E4", with its series when the channel
  // has more than one, else its title.
  function itemName(it, oneShow, full) {
    const se = it.season && it.episode ? `S${it.season} E${it.episode}` : '';
    if (!se) return it.title || 'Untitled';
    const head = oneShow ? se : `${it.title} ${se}`;
    return full && it.subtitle ? `${head}: ${it.subtitle}` : head;
  }
  const oneShow = (list) => list.every((it) => it.title === list[0].title);

  function fmtClock(hm) {
    const m = minutesOf(hm);
    return m < 0 ? '' : fmtTime(new Date(2000, 0, 1, Math.floor(m / 60), m % 60));
  }
  function fmtDay(iso) {
    const [y, mo, d] = iso.split('-').map(Number);
    const t = new Date(y, mo - 1, d);
    return t.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric', year: y === new Date().getFullYear() ? undefined : 'numeric' });
  }
  const listJoin = (l) => (l.length < 2 ? l.join('') : `${l.slice(0, -1).join(', ')} and ${l[l.length - 1]}`);
  function daysPhrase(days, at) {
    const on = DAYS.filter((_, k) => days[k]);
    const evening = at >= 17 * 60 || at < 4 * 60;
    if (on.length === 7) return evening ? 'nightly' : 'daily';
    if (on.join() === 'mon,tue,wed,thu,fri') return evening ? 'weeknights' : 'weekdays';
    if (on.join() === 'sat,sun') return 'on weekends';
    if (on.length === 1) return `on ${DAY_NAMES[DAYS.indexOf(on[0])]}s`;
    return 'on ' + listJoin(on.map((x) => DAY_NAMES[DAYS.indexOf(x)].slice(0, 3)));
  }

  // The schedule in a line: "4 episodes nightly at 8:00 PM, starting Sun, Oct 4 with S1 E1".
  function schedSummary(d) {
    const s = d.sched;
    if (!s) return 'Loops day and night on a fixed clock; tuning in joins whatever is on.';
    const noun = schedNoun(d);
    const list = loadedItems();
    const it = list && list[s.first - 1];
    const first = it ? itemName(it, oneShow(list), false) : s.first === 1 ? `the first ${noun}` : `${noun} ${s.first}`;
    const started = s.start < isoDay(new Date()) ? 'started' : 'starting';
    const from = `${started} ${fmtDay(s.start)}${s.time ? ' at ' + fmtClock(s.time) : ''} with ${first}`;
    if (!s.blocks.length) return `Around the clock, ${from}.`;
    const blocks = s.blocks.map((b) => {
      const days = daysPhrase(b.days, minutesOf(b.at));
      return b.end === 'until'
        ? `${days} from ${fmtClock(b.at)} to ${fmtClock(b.until)}`
        : `${plural(Number(b.count), noun, noun + 's')} ${days} at ${fmtClock(b.at)}`;
    });
    const text = `${blocks.join('; ')}, ${from}.`;
    return text[0].toUpperCase() + text.slice(1);
  }

  // The Airs row: around the clock or on a schedule, and the schedule's editor. Nothing for a
  // channel that can't have one.
  function scheduleSection() {
    const d = S.draft;
    if (!schedulable(d)) return null;
    E.schedSum = h('p', { class: 'sch-sum', id: 'sch-sum', 'aria-live': 'polite' });
    E.schedEd = h('div', { class: 'sch-ed' });
    E.schedHint = h('p', { class: 'sch-hint', hidden: true },
      'Shuffled, its episodes air in no order. For a series, ',
      h('button', { class: 'linkish', type: 'button', onclick: playInOrder }, 'In order'),
      ' plays them as they aired.');
    E.schedErr = h('p', { class: 'f-err sch-err', id: 'sch-err', role: 'alert' });
    E.schedStash = null;
    const sec = h('section', { class: 'ed-sched', 'aria-labelledby': 'airs-l' },
      h('span', { class: 'opt-label', id: 'airs-l' }, 'Airs'),
      h('div', { class: 'sch-main' },
        h('div', { class: 'sch-top' }, radios('airs', AIRS, !!d.sched, onAirs, 'When it airs'), E.schedSum),
        E.schedEd, E.schedHint, E.schedErr));
    renderSchedEditor();
    loadItems();
    return sec;
  }

  function onAirs(e) {
    const d = S.draft;
    if (e.target.value === 'true') d.sched = E.schedStash || newSched(d);
    else { E.schedStash = d.sched; d.sched = null; }
    S.serverErr.sched = '';
    renderSchedEditor();
    loadItems();
    changed();
  }
  function schedChanged() {
    S.serverErr.sched = '';
    changed();
  }
  function playInOrder() {
    S.draft.order = 'aired';
    const r = $('input[name=order][value=aired]');
    if (r) r.checked = true;
    if (E.orderHelp) updateHelp();
    changed();
  }

  // Builds the schedule's fields from the draft, keeping focus on the field that had it.
  function renderSchedEditor() {
    if (!E.schedEd) return;
    const s = S.draft.sched;
    const focused = document.activeElement && E.schedEd.contains(document.activeElement) ? document.activeElement.id : '';
    E.schedEd.hidden = !s;
    if (!s) { E.schedEd.replaceChildren(); E.schFirst = null; updateSched(); return; }
    const sc = () => S.draft.sched;
    E.schStart = h('input', {
      type: 'date', class: 'input sch-date', id: 'sch-start', value: s.start, 'aria-label': 'Starts on', required: true,
      oninput: (e) => { sc().start = e.target.value; schedChanged(); },
    });
    E.schTime = h('input', {
      type: 'time', class: 'input sch-time', id: 'sch-time', value: s.time, 'aria-label': 'Starts at (optional; empty starts with the first block that day)',
      title: 'Optional: empty starts with the first block that day', oninput: (e) => { sc().time = e.target.value; schedChanged(); },
    });
    E.schFirst = h('select', {
      class: 'input select sch-first', id: 'sch-first', 'aria-label': 'Begins with',
      onchange: (e) => { sc().first = Math.max(1, Number(e.target.value) || 1); schedChanged(); },
    });
    renderFirstPicker();
    E.schNouns = [];
    const rows = s.blocks.map((b, i) => blockRow(b, i));
    E.schedEd.replaceChildren(
      h('div', { class: 'sch-line' },
        h('label', { class: 'sch-l', for: 'sch-start' }, 'Starts'), E.schStart, h('label', { class: 'sch-l', for: 'sch-time' }, 'at'), E.schTime,
        h('label', { class: 'sch-l', for: 'sch-first' }, 'with'), E.schFirst),
      rows.length ? h('ol', { class: 'sch-blocks', 'aria-label': 'When it airs each day' }, rows) : null,
      h('div', { class: 'sch-line' },
        h('button', { class: 'btn small ghost', type: 'button', id: 'sch-add', disabled: s.blocks.length >= MAX_BLOCKS, onclick: addBlock }, 'Add a block'),
        h('span', { class: 'help' }, s.blocks.length
          ? 'Off the air between blocks. A block that runs into the next pushes it back.'
          : 'With no blocks it airs around the clock from the start.')));
    if (focused) { const el = document.getElementById(focused); if (el) el.focus(); }
    updateSched();
  }

  // One daily block: its days, when it starts, and how it ends.
  function blockRow(b, i) {
    const n = i + 1;
    const bk = () => S.draft.sched.blocks[i];
    const days = h('div', { class: 'sch-days', role: 'group', 'aria-label': `Block ${n}: days` },
      DAYS.map((x, k) => h('button', {
        class: 'gchip', type: 'button', id: `sch-${i}-${x}`, 'aria-pressed': b.days[k], title: DAY_NAMES[k] + 's',
        onclick: (e) => { const on = !bk().days[k]; bk().days[k] = on; e.currentTarget.setAttribute('aria-pressed', String(on)); schedChanged(); },
      }, DAY_NAMES[k].slice(0, 3))));
    const at = h('input', {
      type: 'time', class: 'input sch-time', id: `sch-${i}-at`, value: b.at, 'aria-label': `Block ${n}: starts at`,
      oninput: (e) => { bk().at = e.target.value; schedChanged(); },
    });
    const end = h('select', {
      class: 'input select sch-end', id: `sch-${i}-end`, 'aria-label': `Block ${n}: ends after a number, or at a time`,
      onchange: (e) => { bk().end = e.target.value; renderSchedEditor(); schedChanged(); },
    }, h('option', { value: 'count' }, 'for'), h('option', { value: 'until' }, 'until'));
    end.value = b.end;
    const stop = b.end === 'until'
      ? h('input', {
        type: 'time', class: 'input sch-time', id: `sch-${i}-until`, value: b.until, 'aria-label': `Block ${n}: stops at`,
        title: 'Nothing new starts from then; what is on finishes. Earlier than the start is the next day.',
        oninput: (e) => { bk().until = e.target.value; schedChanged(); },
      })
      : [h('input', {
        class: 'input n', id: `sch-${i}-count`, value: b.count, inputmode: 'numeric', autocomplete: 'off', 'aria-label': `Block ${n}: how many`,
        oninput: (e) => { bk().count = e.target.value; schedChanged(); },
      }), (E.schNouns[i] = h('span', { class: 'sch-noun' }))];
    return h('li', { class: 'sch-block' }, days, h('span', { class: 'sch-l' }, 'at'), at, end, stop,
      h('button', { class: 'x', type: 'button', 'aria-label': `Remove block ${n}`, title: 'Remove', onclick: () => removeBlock(i) }, '×'));
  }

  function addBlock() {
    const s = S.draft.sched;
    if (s.blocks.length >= MAX_BLOCKS) return;
    const last = s.blocks[s.blocks.length - 1];
    const at = last && minutesOf(last.at) >= 0 ? (minutesOf(last.at) + 180) % 1440 : 20 * 60;
    s.blocks.push({
      days: last ? [...last.days] : DAYS.map(() => true), at: `${String(Math.floor(at / 60)).padStart(2, '0')}:${String(at % 60).padStart(2, '0')}`,
      end: 'count', count: last && last.end === 'count' ? last.count : '2', until: '',
    });
    renderSchedEditor();
    const el = document.getElementById(`sch-${s.blocks.length - 1}-at`);
    if (el) el.focus();
    schedChanged();
  }
  function removeBlock(i) {
    S.draft.sched.blocks.splice(i, 1);
    renderSchedEditor();
    const el = document.getElementById('sch-add');
    if (el) el.focus();
    schedChanged();
  }

  // The begin-with picker: every video in the order it airs, once they're known. A new channel
  // begins with its first.
  function renderFirstPicker() {
    const sel = E.schFirst, s = S.draft && S.draft.sched;
    if (!sel || !s) return;
    const noun = schedNoun(S.draft), list = loadedItems();
    const opts = [];
    if (list && list.length) {
      const one = oneShow(list);
      for (const it of list) {
        const name = itemName(it, one, true);
        opts.push(h('option', { value: String(it.n) }, `${it.n}. ${name.length > 90 ? name.slice(0, 89) + '…' : name}`));
      }
    }
    if (s.first > opts.length) {
      const why = isNew() ? '' : S.items && S.items.status === 'loading' ? ' (loading the list)' : '';
      if (!opts.length && s.first > 1) opts.push(h('option', { value: '1' }, `The first ${noun}`));
      opts.push(h('option', { value: String(s.first) }, s.first === 1 ? `The first ${noun}${why}` : `${noun[0].toUpperCase() + noun.slice(1)} ${s.first}${why}`));
    }
    sel.replaceChildren(...opts);
    sel.value = String(s.first);
    sel.disabled = isNew();
    sel.title = isNew() ? `A new channel begins with its first ${noun}; pick another once it's created.` : '';
  }

  // The summary, the nouns after counts, the order hint and what's wrong, as the draft has them.
  function updateSched(p) {
    if (!E.schedSum || !S.draft) return;
    const d = S.draft;
    const msg = S.serverErr.sched || (p ? p.sched : schedProblem(d));
    E.schedSum.textContent = msg && d.sched ? '' : schedSummary(d);
    E.schedSum.classList.toggle('on', !!d.sched);
    E.schedErr.textContent = d.sched ? msg : '';
    // Shuffled episodes on a schedule are rarely what's meant.
    E.schedHint.hidden = !(d.kind === 'jellyfin' && d.sched && d.order === 'shuffle' && d.series && d.series.size);
    if (d.sched) {
      const noun = schedNoun(d);
      d.sched.blocks.forEach((b, i) => {
        const el = E.schNouns && E.schNouns[i];
        if (el) el.textContent = Number(b.count) === 1 ? noun : noun + 's';
      });
    }
  }

  // After a save: the fields as the server has them, and the list again, which the save may change.
  function schedSaved() {
    if (!E.schedEd) return;
    S.items = null;
    renderSchedEditor();
    loadItems();
  }

  // Folder and weather channels: the number, name and details, what plays, and the next airings.
  function buildMetaEditor() {
    E = { metaOnly: true };
    const head = editorHead();
    const meta = metaSection();
    E.info = h('section', { class: 'meta-info', 'aria-label': 'What plays' });
    E.schedHead = h('span');
    E.sched = h('div', { class: 'sched-body' });
    const aside = h('aside', { class: 'aside', 'aria-label': 'Schedule' },
      h('section', { class: 'sched', 'aria-labelledby': 'sched-h' },
        h('h2', { id: 'sched-h' }, 'Next airings', E.schedHead), E.sched));
    $('#main').replaceChildren(h('div', { class: 'ed meta' }, head, meta, scheduleSection(), h('div', { class: 'ed-body' }, E.info, aside)));
    renderMetaInfo();
    sizeNum();
    updateKicker();
    renderSchedule();
    updateDirty();
  }

  // What a folder or the weather channel plays, and where its files are.
  function renderMetaInfo() {
    const d = S.draft, ch = S.ch;
    const host = hostLabel();
    const on = host ? ` on ${host}` : '';
    const info = [];
    if (d.kind === 'folder') {
      const dir = (S.state.channelsDir || '/channels').replace(/\/+$/, '') + '/' + (ch.folder || `${ch.number} ${ch.name}`);
      info.push(
        h('p', null, 'Plays the videos in its folder in file name order, looping. To change what\'s on, add or remove videos in this folder', on, ':'),
        h('code', { class: 'path', title: 'Click to select' }, dir),
        h('p', { class: 'help' }, 'New files are picked up within about a minute. Saving a new number or name renames the folder; the details and logo are kept in it.'),
        h('details', null,
          h('summary', null, 'Fill it with yt-dlp'),
          h('code', { class: 'path' }, `cd "${dir}"\nyt-dlp -S "res:1080" --merge-output-format mp4 --write-info-json \\\n  -o "%(title)s [%(id)s].%(ext)s" "<video or playlist URL>"`)));
    } else {
      const dir = S.state.musicDir || '/music';
      info.push(
        h('p', null, 'The local forecast in WeatherStar 4000 style, over looping music. Its number, name and details are kept in the channels folder, in .weather.json.'),
        h('p', null, 'To play your own music under it, put MP3s in this folder', on, ':'),
        h('code', { class: 'path', title: 'Click to select' }, dir));
    }
    E.info.replaceChildren(...info);
  }

  function metaKicker() {
    const k = E.kicker, ch = S.ch;
    k.replaceChildren(h('span', { class: 'kind k-' + S.draft.kind, text: CHANNEL_KIND[S.draft.kind] }));
    if (!ch) return;
    if (ch.kind === 'folder') k.append(h('span', { text: plural(ch.videos || 0, 'video', 'videos') }));
    else k.append(h('span', null, 'Built in'));
  }

  function buildEditor() {
    const d = S.draft;
    const fresh = isNew();
    E = {};
    const head = editorHead();
    const meta = metaSection();

    E.orderHelp = h('p', { class: 'help', id: 'order-help' });
    E.rateHelp = h('p', { class: 'help', id: 'rate-help' });
    const opts = h('div', { class: 'ed-opts' },
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label', id: 'order-l' }, 'Order'),
        radios('order', ORDERS, d.order, (e) => { S.draft.order = e.target.value; changed(); updateHelp(); }, 'Order'),
        E.orderHelp),
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label', id: 'rate-l' }, 'Bitrate'),
        radios('rate', bitrateOpts(d.maxBitrate), d.maxBitrate, (e) => { S.draft.maxBitrate = Number(e.target.value); changed(); updateHelp(); }, 'Bitrate'),
        E.rateHelp));

    E.tabs = {};
    const tablist = h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Library', onkeydown: tabKeys },
      KINDS.map((k) => (E.tabs[k] = h('button', {
        class: 'tab', role: 'tab', type: 'button', id: 'tab-' + k, 'aria-controls': 'lib-panel', onclick: () => setTab(k),
      }, KIND[k].label, h('span', { class: 'n' })))));
    E.libStatus = h('span', { class: 'lib-status', 'aria-live': 'polite' });

    E.search = h('input', {
      class: 'input', type: 'search', id: 'search', autocomplete: 'off', spellcheck: 'false', 'aria-keyshortcuts': '/',
      oninput: onSearch, onkeydown: searchKeys,
    });
    E.picked = h('button', {
      class: 'gchip sel-only', type: 'button', 'aria-pressed': false, title: 'Show only what this channel plays',
      onclick: () => { const f = S.filters[S.tab]; f.onlySel = !f.onlySel; renderFilters(); renderGrid(); },
    }, 'Picked', h('span', { class: 'n' }));
    E.clear = h('button', { class: 'gchip clear', type: 'button', hidden: true, onclick: clearFilters }, 'Clear filters');
    E.chips = h('div', { class: 'gchips', role: 'group', 'aria-label': 'Genres' });
    E.fcount = h('span', { class: 'fcount', 'aria-live': 'polite' });
    E.grid = h('div', { class: 'grid', onclick: gridClick, onkeydown: gridKeys, onfocusin: gridFocus });
    E.grid.addEventListener('error', (e) => { if (e.target.tagName === 'IMG') e.target.remove(); }, true);
    E.gmsg = h('div', { class: 'gmsg' });
    E.sentinel = h('div', { class: 'sentinel', 'aria-hidden': 'true' });
    E.scroller = h('div', { class: 'scroller', id: 'lib-panel', role: 'tabpanel', onscroll: queueMore }, E.gmsg, E.grid, E.sentinel);

    E.picksCount = h('span', { class: 'n' });
    E.picks = h('div', { class: 'picks-body' });
    E.schedHead = h('span');
    E.sched = h('div', { class: 'sched-body' });
    const aside = h('aside', { class: 'aside', 'aria-label': 'Picks and schedule' },
      h('section', { class: 'picks', 'aria-labelledby': 'picks-h' },
        h('h2', { id: 'picks-h' }, 'On this channel', E.picksCount), E.picks),
      h('section', { class: 'sched', 'aria-labelledby': 'sched-h' },
        h('h2', { id: 'sched-h' }, 'Next airings', E.schedHead), E.sched));

    const ed = h('div', { class: 'ed' }, head, meta, opts, scheduleSection(),
      h('div', { class: 'ed-tabs' }, tablist, E.libStatus),
      h('div', { class: 'ed-body' },
        h('section', { class: 'lib', 'aria-label': 'Library' },
          h('div', { class: 'fbar' },
            h('div', { class: 'fbar-row' },
              h('div', { class: 'search' }, E.search, h('kbd', { 'aria-hidden': 'true' }, '/')),
              E.picked, E.clear, E.fcount),
            E.chips),
          E.scroller),
        aside));
    $('#main').replaceChildren(ed);

    sizeNum();
    updateHelp();
    updateKicker();
    updateTabs();
    setTab(S.tab);
    renderPicks();
    renderSchedule();
    updateDirty();
    if (fresh) E.name.focus();
  }

  function sizeNum() {
    E.num.style.width = (Math.max(3, E.num.value.length) + 0.4) + 'ch';
  }

  function updateHelp() {
    const d = S.draft;
    const o = ORDERS.find((x) => x.v === d.order) || ORDERS[0];
    E.orderHelp.textContent = o.help;
    E.rateHelp.textContent = d.maxBitrate
      ? `${serverLabel()} transcodes anything above ${d.maxBitrate} Mbps before sending it.`
      : 'Sends the original files as they are; nothing is transcoded on Jellyfin.';
  }

  function updateKicker() {
    const k = E.kicker;
    if (!k) return;
    if (E.yt) { ytKicker(); return; }
    if (E.metaOnly) { metaKicker(); return; }
    k.replaceChildren();
    if (isNew()) {
      k.append(h('span', { class: 'kind k-new' }, 'New Jellyfin channel'), h('span', null, 'Goes on the air when you create it'));
      return;
    }
    const ch = S.ch;
    k.append(h('span', { class: 'kind k-jellyfin' }, 'Jellyfin'));
    if (!ch) return;
    if (ch.error) k.append(h('span', { class: 'err', title: ch.error, text: ch.error }));
    else if (ch.items == null) k.append(h('span', null, 'Loading from Jellyfin'));
    else k.append(h('span', { text: plural(ch.items, 'video', 'videos') + ' in rotation' }));
    if (ch.now && ch.now.title) {
      k.append(h('span', { class: 'now', title: [ch.now.title, ch.now.subtitle].filter(Boolean).join(': ') },
        h('b', null, 'Now'), ch.now.title,
        ch.now.subtitle ? h('em', null, ', ' + ch.now.subtitle) : null,
        ch.now.end ? ` until ${fmtTime(ch.now.end)}` : null));
    }
  }

  function onField(e) {
    const d = S.draft;
    if (e.target === E.num) {
      d.number = E.num.value;
      S.serverErr.number = '';
      sizeNum();
      updateMeta();
    } else {
      d.name = E.name.value;
      S.serverErr.name = '';
      if (!squash(d.callSign)) renderLogo();
    }
    if (isNew()) {
      const li = $(`#channels .ch[data-number="${S.sel}"]`);
      if (li) {
        $('.ch-num', li).textContent = d.number.trim() || '?';
        $('.ch-name', li).textContent = d.name.trim() || 'New channel';
      }
    }
    changed();
  }

  function changed() {
    S.saveMsg = null;
    updateDirty();
  }

  function updateDirty() {
    if (!E.save) return;
    const d = S.draft, fresh = isNew(), dirty = isDirty(), p = problems();
    const numMsg = S.serverErr.number || ((S.touched.number || p.taken) ? p.number : '');
    const nameMsg = S.serverErr.name || ((S.touched.name || p.badName) ? p.name : '');
    E.numErr.textContent = numMsg;
    E.nameErr.textContent = nameMsg;
    E.num.setAttribute('aria-invalid', String(!!numMsg));
    E.name.setAttribute('aria-invalid', String(!!nameMsg));
    const yt = p.yt || { sources: '', settings: '' };
    if (E.optErr) E.optErr.textContent = yt.settings;
    const metaMsg = S.serverErr.meta || p.meta;
    if (E.metaErr) {
      E.metaErr.textContent = metaMsg;
      E.call.setAttribute('aria-invalid', String(!!metaMsg && /call sign/i.test(metaMsg)));
      E.desc.setAttribute('aria-invalid', String(!!metaMsg && /description/i.test(metaMsg)));
    }
    updateSched(p);
    E.save.disabled = S.saving || !!p.number || !!p.name || !!p.meta || !!p.sched || !!yt.sources || !!yt.settings || (!dirty && !fresh);

    let text = '', cls = '';
    if (S.saving) text = fresh ? 'Creating' : 'Saving';
    else if (S.saveMsg) ({ text, cls } = S.saveMsg);
    else if (fresh && !d.name.trim()) text = 'Name the channel to create it.';
    else if (yt.sources) text = fresh && d.ytMode !== 'playlist' ? 'Add a YouTube channel to create it.' : yt.sources;
    else if (fresh) { text = 'Not created yet'; cls = 'dirty'; }
    else if (dirty) { text = 'Unsaved changes'; cls = 'dirty'; }
    E.status.textContent = text;
    E.status.className = 'ed-status' + (cls ? ' ' + cls : '');

    if (!fresh) {
      const li = $(`#channels .ch[data-number="${CSS.escape(S.origNumber)}"]`);
      if (li) li.classList.toggle('dirty', dirty);
    }
  }

  // ---------- Tabs, library, filters ----------
  function tabKeys(e) {
    const i = KINDS.indexOf(S.tab);
    let n = -1;
    if (e.key === 'ArrowRight') n = (i + 1) % KINDS.length;
    else if (e.key === 'ArrowLeft') n = (i + KINDS.length - 1) % KINDS.length;
    else if (e.key === 'Home') n = 0;
    else if (e.key === 'End') n = KINDS.length - 1;
    if (n < 0) return;
    e.preventDefault();
    setTab(KINDS[n]);
    E.tabs[KINDS[n]].focus();
  }

  function setTab(k) {
    S.tab = k;
    for (const kk of KINDS) {
      const on = kk === k;
      E.tabs[kk].setAttribute('aria-selected', String(on));
      E.tabs[kk].tabIndex = on ? 0 : -1;
    }
    E.scroller.setAttribute('aria-labelledby', 'tab-' + k);
    E.search.value = S.filters[k].q;
    E.search.placeholder = `Search ${KIND[k].many}`;
    E.search.setAttribute('aria-label', `Search ${KIND[k].many}`);
    const L = S.lib[k];
    if (!L || L.status === 'error') loadLib(k);
    renderFilters();
    renderGrid();
    updateLibStatus();
  }

  function updateTabs() {
    for (const k of KINDS) {
      const n = S.draft[k].size;
      const s = $('.n', E.tabs[k]);
      s.textContent = n;
      s.classList.toggle('some', n > 0);
      E.tabs[k].setAttribute('aria-label', `${KIND[k].label}, ${n} picked`);
    }
    $('.n', E.picked).textContent = S.draft[S.tab].size;
  }

  function updateLibStatus() {
    const k = S.tab, L = S.lib[k];
    let t = '';
    if (!L || L.status === 'loading') t = `Loading ${KIND[k].many} from ${serverLabel()}`;
    else if (L.status === 'error') t = `Couldn't load ${KIND[k].many}`;
    else t = `${plural(L.items.length, KIND[k].one, KIND[k].many)} on ${serverLabel()}`;
    E.libStatus.textContent = t;
  }

  function loadLib(k, force) {
    const cur = S.lib[k];
    if (cur && !force && cur.status !== 'error') return cur.promise;
    const L = { status: 'loading', items: [], byKey: new Map(), genres: [], error: null };
    S.lib[k] = L;
    L.promise = api('GET', `/library/${k}`).then((r) => {
      const items = Array.isArray(r && r.items) ? r.items : [];
      const counts = new Map();
      for (const it of items) {
        it.genres = Array.isArray(it.genres) ? it.genres : [];
        it.name = it.name || it.key || '';
        it._s = fold([it.name, it.key, it.year].filter(Boolean).join(' '));
        for (const g of it.genres) counts.set(g, (counts.get(g) || 0) + 1);
      }
      L.items = items;
      L.byKey = new Map(items.map((it) => [it.key, it]));
      L.genres = [...counts].map(([name, count]) => ({ name, count }))
        .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
      L.status = 'ok';
    }, (err) => {
      L.status = 'error';
      L.error = err;
    }).then(() => {
      if (S.lib[k] !== L || !E.grid) return;
      if (k === S.tab) { renderFilters(); renderGrid(); }
      updateLibStatus();
      renderPicks();
    });
    return L.promise;
  }

  function renderFilters() {
    const k = S.tab, L = S.lib[k], f = S.filters[k];
    const ready = !!(L && L.status === 'ok');
    E.picked.hidden = !ready;
    E.picked.setAttribute('aria-pressed', String(f.onlySel));
    $('.n', E.picked).textContent = S.draft[k].size;
    E.clear.hidden = !(f.q.trim() || f.genres.size || f.onlySel);
    if (E.clear.hidden && document.activeElement === E.clear) E.search.focus();
    const focused = document.activeElement && E.chips.contains(document.activeElement) ? document.activeElement.dataset.f : null;
    E.chips.replaceChildren();
    E.chips.hidden = !ready || !L.genres.length;
    if (!ready) return;
    const shown = S.genresOpen ? L.genres : L.genres.filter((g, i) => i < TOP_GENRES || f.genres.has(g.name));
    for (const g of shown) {
      E.chips.append(h('button', {
        class: 'gchip', type: 'button', 'data-f': 'g:' + g.name, 'aria-pressed': f.genres.has(g.name),
        onclick: () => toggleGenre(g.name),
      }, g.name, h('span', { class: 'n', text: g.count.toLocaleString() })));
    }
    if (L.genres.length > TOP_GENRES) {
      E.chips.append(h('button', {
        class: 'gchip more', type: 'button', 'data-f': ':more', 'aria-expanded': S.genresOpen,
        onclick: () => { S.genresOpen = !S.genresOpen; renderFilters(); },
      }, S.genresOpen ? 'Fewer genres' : `${L.genres.length - TOP_GENRES} more genres`));
    }
    if (focused) {
      const el = E.chips.querySelector(`[data-f="${CSS.escape(focused)}"]`);
      if (el) el.focus();
    }
  }

  function toggleGenre(g) {
    const set = S.filters[S.tab].genres;
    if (set.has(g)) set.delete(g); else set.add(g);
    renderFilters();
    renderGrid();
  }

  function clearFilters() {
    const f = S.filters[S.tab];
    f.q = ''; f.genres.clear(); f.onlySel = false;
    E.search.value = '';
    E.search.focus();
    renderFilters();
    renderGrid();
  }

  let searchTimer = 0;
  function onSearch() {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      const f = S.filters[S.tab];
      const had = !!f.q.trim();
      f.q = E.search.value;
      if (had !== !!f.q.trim()) renderFilters();
      renderGrid();
    }, 90);
  }

  function searchKeys(e) {
    if (e.key === 'Escape') {
      if (E.search.value) {
        e.preventDefault();
        E.search.value = '';
        onSearch();
      } else {
        E.search.blur();
      }
    } else if (e.key === 'ArrowDown') {
      const c = E.grid.querySelector('.card[tabindex="0"]') || E.grid.querySelector('.card');
      if (c) { e.preventDefault(); c.focus(); }
    } else if (e.key === 'Enter') {
      e.preventDefault();
      clearTimeout(searchTimer);
      S.filters[S.tab].q = E.search.value;
      renderFilters();
      renderGrid();
    }
  }

  function filteredItems() {
    const k = S.tab, L = S.lib[k], f = S.filters[k], picked = S.draft[k];
    const terms = fold(f.q).split(/\s+/).filter(Boolean);
    const g = f.genres;
    return L.items.filter((it) =>
      (!f.onlySel || picked.has(it.key)) &&
      (!g.size || it.genres.some((x) => g.has(x))) &&
      (!terms.length || terms.every((t) => it._s.includes(t))));
  }

  function describeFilters(k) {
    const f = S.filters[k];
    const parts = [];
    if (f.q.trim()) parts.push(`matching “${f.q.trim()}”`);
    if (f.genres.size) parts.push('in ' + [...f.genres].join(' or '));
    if (f.onlySel) parts.push('among the picks');
    return `No ${KIND[k].many} ${parts.join(' ')}.`;
  }

  function renderGrid() {
    const k = S.tab, L = S.lib[k];
    E.grid.replaceChildren();
    E.gmsg.replaceChildren();
    E.fcount.textContent = '';
    S.view = null;
    E.scroller.scrollTop = 0;
    if (!L || L.status === 'loading') {
      E.gmsg.append(h('p', null, `Loading ${KIND[k].many} from ${serverLabel()}. Big libraries take a few seconds.`));
      for (let i = 0; i < 14; i++) E.grid.append(h('div', { class: 'skel', 'aria-hidden': 'true' }));
      return;
    }
    if (L.status === 'error') {
      const a = S.state.account;
      E.gmsg.append(
        h('p', { class: 'big err' }, `Couldn't load ${KIND[k].many} from ${serverLabel()}.`),
        h('p', null, L.error.message),
        h('div', { class: 'row' },
          h('button', { class: 'btn', type: 'button', onclick: () => { loadLib(k, true); renderGrid(); updateLibStatus(); } }, 'Try again'),
          !a.ok ? h('button', { class: 'btn ghost', type: 'button', onclick: () => openAccount() }, 'Check the account') : null));
      return;
    }
    const list = filteredItems();
    S.view = { kind: k, list, shown: 0 };
    E.fcount.textContent = list.length === L.items.length
      ? plural(L.items.length, KIND[k].one, KIND[k].many)
      : `${list.length.toLocaleString()} of ${L.items.length.toLocaleString()}`;
    if (!list.length) {
      if (!L.items.length) {
        E.gmsg.append(h('p', { class: 'big' }, `No ${KIND[k].many} on ${serverLabel()}.`), h('p', null, KIND[k].empty));
      } else {
        E.gmsg.append(h('p', { class: 'big' }, 'Nothing matches.'), h('p', null, describeFilters(k)),
          h('div', { class: 'row' }, h('button', { class: 'btn ghost', type: 'button', onclick: clearFilters }, 'Clear filters')));
      }
      return;
    }
    appendChunk();
  }

  function metaFor(k, it) {
    const m = [];
    if (it.year) m.push(String(it.year));
    if (k === 'series' && it.count) m.push(`${it.count.toLocaleString()} ep${it.count === 1 ? '' : 's'}`);
    else if (k === 'movies' && it.runtimeMinutes) m.push(fmtRuntime(it.runtimeMinutes));
    else if ((k === 'collections' || k === 'playlists') && it.count != null) m.push(plural(it.count, 'item', 'items'));
    return m;
  }

  function card(it, k, on) {
    const poster = h('span', { class: 'poster' }, h('span', { class: 'ph', 'aria-hidden': 'true', text: it.name }));
    if (it.image) poster.append(h('img', { src: it.image, alt: '', loading: 'lazy', decoding: 'async', draggable: 'false' }));
    poster.append(h('span', { class: 'tick', 'aria-hidden': 'true' }));
    if (it.overview) poster.append(h('span', { class: 'ov', 'aria-hidden': 'true', text: it.overview }));
    const meta = metaFor(k, it);
    return h('button', {
      class: 'card', type: 'button', tabindex: '-1', 'data-key': it.key, 'aria-pressed': on,
      'aria-description': it.overview || null,
    }, poster, h('span', { class: 'cname', text: it.name }),
    meta.length ? h('span', { class: 'cmeta' }, meta.map((x) => h('span', { text: x }))) : null);
  }

  function appendChunk() {
    const v = S.view;
    if (!v || v.shown >= v.list.length) return;
    const end = Math.min(v.list.length, v.shown + CHUNK);
    const picked = S.draft[v.kind];
    const frag = document.createDocumentFragment();
    for (let i = v.shown; i < end; i++) frag.append(card(v.list[i], v.kind, picked.has(v.list[i].key)));
    const first = v.shown === 0;
    E.grid.append(frag);
    v.shown = end;
    if (first && E.grid.firstElementChild) E.grid.firstElementChild.tabIndex = 0;
    queueMore();
  }

  let moreQueued = false;
  function queueMore() {
    if (moreQueued) return;
    moreQueued = true;
    requestAnimationFrame(() => {
      moreQueued = false;
      const v = S.view;
      if (!v || v.shown >= v.list.length || !E.sentinel || !E.sentinel.isConnected) return;
      const top = E.sentinel.getBoundingClientRect().top;
      const bottom = Math.min(window.innerHeight, E.scroller.getBoundingClientRect().bottom);
      if (top < bottom + 1200) appendChunk();
    });
  }
  window.addEventListener('scroll', queueMore, { passive: true });
  window.addEventListener('resize', queueMore);

  function gridClick(e) {
    const c = e.target.closest('.card');
    if (c && S.view) toggleCard(c);
  }

  function toggleCard(c) {
    const k = S.view.kind, key = c.dataset.key, set = S.draft[k];
    if (set.has(key)) set.delete(key); else set.add(key);
    c.setAttribute('aria-pressed', String(set.has(key)));
    picksChanged();
  }

  function gridFocus(e) {
    const c = e.target.closest('.card');
    if (!c) return;
    const prev = E.grid.querySelector('.card[tabindex="0"]');
    if (prev && prev !== c) prev.tabIndex = -1;
    c.tabIndex = 0;
  }

  function gridKeys(e) {
    const c = e.target.closest('.card');
    if (!c) return;
    const cards = E.grid.children;
    const i = Array.prototype.indexOf.call(cards, c);
    const cols = Math.max(1, getComputedStyle(E.grid).gridTemplateColumns.split(' ').length);
    let n;
    switch (e.key) {
      case 'ArrowRight': n = i + 1; break;
      case 'ArrowLeft': n = i - 1; break;
      case 'ArrowDown': n = i + cols; break;
      case 'ArrowUp': n = i - cols; break;
      case 'PageDown': n = i + cols * 3; break;
      case 'PageUp': n = i - cols * 3; break;
      case 'Home': n = 0; break;
      case 'End': n = cards.length - 1; break;
      default: return;
    }
    e.preventDefault();
    if (n < 0 && (e.key === 'ArrowUp' || e.key === 'PageUp') && i < cols) { E.search.focus(); return; }
    if (n >= cards.length && S.view && S.view.shown < S.view.list.length) appendChunk();
    n = Math.max(0, Math.min(cards.length - 1, n));
    const t = cards[n];
    if (t && t !== c) { t.focus(); t.scrollIntoView({ block: 'nearest' }); }
  }

  // ---------- Picks ----------
  function isMissing(k, key) {
    if (S.ch && Array.isArray(S.ch.unmatched) && S.ch.unmatched.includes(key)) return true;
    const L = S.lib[k];
    return !!(L && L.status === 'ok' && !L.byKey.has(key));
  }

  function picksChanged() {
    updateTabs();
    renderPicks();
    changed();
  }

  function renderPicks() {
    if (!E.picks) return;
    const d = S.draft, box = E.picks;
    const buttons = [...box.querySelectorAll('button')];
    const focusIdx = buttons.indexOf(document.activeElement);
    box.replaceChildren();
    let total = 0;
    const missing = [];
    for (const k of KINDS) for (const key of d[k]) { total++; if (isMissing(k, key)) missing.push([k, key]); }
    E.picksCount.textContent = total;
    if (missing.length) {
      box.append(h('div', { class: 'missing-note', role: 'note' },
        h('span', null, missing.length === 1
          ? `1 pick is no longer on ${serverLabel()}.`
          : `${missing.length} picks are no longer on ${serverLabel()}.`),
        h('button', { class: 'btn small danger', type: 'button', onclick: () => dropKeys(missing) },
          missing.length === 1 ? 'Drop it' : `Drop all ${missing.length}`)));
    }
    if (!total) {
      box.append(h('p', { class: 'picks-empty' }, 'Nothing picked yet. Click a poster to add it to this channel.'));
    }
    for (const k of KINDS) {
      if (!d[k].size) continue;
      const L = S.lib[k];
      const rows = [...d[k]].map((key) => {
        const it = L && L.byKey ? L.byKey.get(key) : null;
        const miss = isMissing(k, key);
        const name = it ? it.name : key;
        return h('li', { class: 'pick' + (miss ? ' missing' : '') },
          h('span', { class: 'pk-name', title: key }, name,
            miss ? h('small', null, 'not on Jellyfin') : (it && it.year ? h('small', { text: it.year }) : null)),
          miss
            ? h('button', { class: 'drop', type: 'button', 'aria-label': `Drop ${name}`, onclick: () => dropKeys([[k, key]]) }, 'Drop')
            : h('button', { class: 'x', type: 'button', 'aria-label': `Remove ${name}`, title: 'Remove', onclick: () => dropKeys([[k, key]]) }, '×'));
      });
      box.append(h('div', { class: 'pk-group' },
        h('h3', null, KIND[k].label, h('span', { text: d[k].size })),
        h('ul', null, rows)));
    }
    if (focusIdx >= 0) {
      const now = box.querySelectorAll('button');
      const t = now[Math.min(focusIdx, now.length - 1)];
      if (t) t.focus();
    }
  }

  function dropKeys(list) {
    for (const [k, key] of list) {
      S.draft[k].delete(key);
      if (S.view && S.view.kind === k) {
        const c = E.grid.querySelector(`.card[data-key="${CSS.escape(key)}"]`);
        if (c) c.setAttribute('aria-pressed', 'false');
      }
    }
    picksChanged();
  }

  function syncCards() {
    if (!S.view) return;
    const set = S.draft[S.view.kind];
    for (const c of E.grid.querySelectorAll('.card')) c.setAttribute('aria-pressed', String(set.has(c.dataset.key)));
  }

  // ---------- Main: YouTube editor ----------
  const ytKey = (ref) => String(ref || '').toLowerCase();
  function handleOf(ref) {
    const m = /^(@[^\s/]+)$/.exec(ref) || /youtube\.com\/(@[^/?#]+)/i.exec(ref);
    if (!m) return '';
    try { return decodeURIComponent(m[1]); } catch { return m[1]; }
  }
  const ytBusySrc = (s) => s.listing || (!s.listed && !s.failed);
  const ytBusy = (ch) => !!(ch && Array.isArray(ch.sources) && ch.sources.some(ytBusySrc));

  // What's known of a source: the server's status for it, and what a search or lookup found.
  function ytInfo(ref) {
    const key = ytKey(ref);
    const st = (S.ch && Array.isArray(S.ch.sources) && S.ch.sources.find((s) => ytKey(s.channel) === key || ytKey(s.url) === key)) || null;
    const lk = S.yt.info.get(key);
    const d = (lk && lk.data) || {};
    return {
      ref, st,
      id: (st && st.id) || d.id || '',
      name: (st && st.name) || d.name || handleOf(ref) || String(ref).replace(/^https?:\/\/(www\.)?youtube\.com\//i, ''),
      handle: (st && st.handle) || d.handle || handleOf(ref),
      image: d.image || (st && st.image) || '',
      total: Number(d.videos) || 0,
      looking: !!(lk && lk.status === 'loading'),
      lookErr: lk && lk.status === 'error' ? lk.error : '',
    };
  }

  function avatar(name, image, small) {
    const el = h('span', { class: 'av' + (small ? ' sm' : ''), 'aria-hidden': 'true', text: [...String(name || '?').trim()][0] || '?' });
    if (image) {
      el.append(h('img', { src: image, alt: '', loading: 'lazy', decoding: 'async', referrerpolicy: 'no-referrer', draggable: 'false',
        onerror: (e) => e.target.remove() }));
    }
    return el;
  }

  // Looks up sources two at a time, for their avatars, rough sizes, and whether they can play.
  const ytQueue = [];
  let ytLooking = 0;
  function ytLookup(ref) {
    const key = ytKey(ref), cur = S.yt.info.get(key);
    if (cur && (cur.full || cur.status === 'loading' || cur.status === 'error')) return;
    S.yt.info.set(key, { status: 'loading', data: cur && cur.data });
    ytQueue.push(ref);
    ytPump();
  }
  function ytPump() {
    while (ytLooking < 2 && ytQueue.length) {
      const ref = ytQueue.shift(), key = ytKey(ref), before = S.yt.info.get(key);
      ytLooking++;
      api('GET', `/youtube/channel?u=${enc(ref)}`).then((c) => {
        const got = { status: 'ok', data: c, full: true };
        S.yt.info.set(key, got);
        if (c && c.url) S.yt.info.set(ytKey(c.url), got);
      }, (e) => {
        S.yt.info.set(key, { status: 'error', data: before && before.data, error: e.message });
      }).finally(() => {
        ytLooking--;
        if (E.yt) { ytRenderSources(); ytRenderResults(); renderSchedule(); }
        ytPump();
      });
    }
  }

  function buildYtEditor() {
    const d = S.draft;
    const fresh = isNew();
    E = { yt: true };
    const head = editorHead();
    const meta = metaSection();
    const pl = d.ytMode === 'playlist';
    const find = pl ? ytPlaylistPanel() : ytFindPanel();

    E.picksCount = h('span', { class: 'n' });
    E.picks = h('div', { class: 'picks-body' });
    E.schedHead = h('span');
    E.sched = h('div', { class: 'sched-body' });
    const aside = h('aside', { class: 'aside', 'aria-label': pl ? 'Playlist and schedule' : 'Channels and schedule' },
      h('section', { class: 'picks', 'aria-labelledby': 'picks-h' },
        h('h2', { id: 'picks-h' }, 'On this channel', E.picksCount), E.picks),
      h('section', { class: 'sched', 'aria-labelledby': 'sched-h' },
        h('h2', { id: 'sched-h' }, 'Next airings', E.schedHead), E.sched));

    $('#main').replaceChildren(h('div', { class: 'ed yt' }, head, meta, ytOpts(), scheduleSection(), h('div', { class: 'ed-body' }, find, aside)));
    sizeNum();
    ytHelp();
    updateKicker();
    ytRenderSources();
    ytRenderResults();
    renderSchedule();
    updateDirty();
    if (pl && YT_PLAYLIST.test(d.playlist)) ytPlLookup(d.playlist);
    if (!pl) for (const ref of d.sources) ytLookup(ref);
    if (fresh) E.name.focus();
  }

  // The search for YouTube channels to add, beside a channel of uploads.
  function ytFindPanel() {
    E.search = h('input', {
      class: 'input', type: 'search', id: 'search', placeholder: 'Search YouTube channels', autocomplete: 'off', spellcheck: 'false',
      'aria-label': 'Search YouTube channels, or paste a channel link', 'aria-keyshortcuts': '/', oninput: ytOnInput, onkeydown: ytSearchKeys,
    });
    E.ytCount = h('span', { class: 'fcount', 'aria-live': 'polite' });
    E.ytMsg = h('div', { class: 'gmsg', 'aria-live': 'polite' });
    E.ytRes = h('ul', { class: 'ytres', 'aria-label': 'YouTube channels found' });
    return h('section', { class: 'lib ytfind', 'aria-label': 'Find YouTube channels' },
      h('form', { class: 'fbar', role: 'search', onsubmit: (e) => { e.preventDefault(); ytSearch(E.search.value); } },
        h('div', { class: 'fbar-row' },
          h('div', { class: 'search' }, E.search, h('kbd', { 'aria-hidden': 'true' }, '/')),
          h('button', { class: 'btn', type: 'submit' }, 'Search'),
          E.ytCount),
        h('p', { class: 'help' }, 'Or paste a channel link, like youtube.com/@boilerroom, or its @handle.')),
      h('div', { class: 'scroller' }, E.ytMsg, E.ytRes));
  }

  // Switches what the channel plays. The editor is built again: the panels, the settings and the
  // Airs row differ.
  function ytSetMode(mode) {
    const d = S.draft;
    if (d.ytMode === mode) return;
    d.ytMode = mode;
    d.playlist = mode === 'playlist' ? d.plLink.trim() : '';
    S.yt.view = null;
    buildYtEditor();
    const r = $(`input[name=ytmode][value="${mode}"]`);
    if (r) r.focus();
  }

  function ytOpts() {
    const d = S.draft;
    const num = (key, label, extra) => h('input', {
      class: 'input n', id: 'yt-' + key, value: d[key], inputmode: 'numeric', autocomplete: 'off', 'aria-label': label, ...extra,
      oninput: (e) => { S.draft[key] = e.target.value; changed(); },
    });
    E.ytMin = num('minMinutes', 'Shortest, in minutes');
    E.ytMax = num('maxMinutes', 'Longest, in minutes', { placeholder: 'any' });
    E.ytRepeat = num('repeatDays', 'Days before a video airs again');
    E.ytAge = num('maxAgeDays', 'Only videos from the last so many days', { placeholder: 'any' });
    E.ytMix = h('select', { class: 'input select', id: 'yt-reruns', 'aria-labelledby': 'reruns-l', onchange: (e) => { S.draft.rerunMix = e.target.value; changed(); ytHelp(); } },
      YT_RERUNS.map((o) => h('option', { value: o.v }, o.label)));
    E.ytMix.value = d.rerunMix;
    const heights = [...YT_HEIGHTS];
    if (!heights.includes(d.maxHeight)) heights.push(d.maxHeight);
    heights.sort((a, b) => a - b);
    E.mixHelp = h('p', { class: 'help' });
    E.qualHelp = h('p', { class: 'help' });
    E.runHelp = h('p', { class: 'help' });
    E.optErr = h('p', { class: 'f-err opt-err', role: 'alert' });
    E.modeHelp = h('p', { class: 'help' });
    // Uploads' settings keep their pairs below a row of their own.
    const plays = h('div', { class: 'opt-group' + (d.ytMode === 'uploads' ? ' plays' : '') },
      h('span', { class: 'opt-label' }, 'Plays'),
      radios('ytmode', YT_MODES, d.ytMode, (e) => ytSetMode(e.target.value), 'What it plays'),
      E.modeHelp);
    const quality = h('div', { class: 'opt-group' },
      h('span', { class: 'opt-label' }, 'Quality'),
      radios('quality', heights.map((v) => ({ v, label: `${v}p` })), d.maxHeight, (e) => { S.draft.maxHeight = Number(e.target.value); changed(); ytHelp(); }, 'Quality'),
      E.qualHelp);
    if (d.ytMode === 'playlist') return h('div', { class: 'ed-opts yt-opts' }, plays, quality, E.optErr);
    return h('div', { class: 'ed-opts yt-opts' }, plays,
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label' }, 'Length'),
        h('div', { class: 'opt-line' }, E.ytMin, h('span', null, 'to'), E.ytMax, h('span', null, 'minutes')),
        h('p', { class: 'help' }, 'Shorter and longer videos stay off the air; leave the longest empty for no limit. DJ sets: 20 to 240.')),
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label' }, 'Age'),
        h('div', { class: 'opt-line' }, h('span', null, 'only videos from the last'), E.ytAge, h('span', null, 'days')),
        h('p', { class: 'help' }, 'Older uploads stay off the air, first runs and reruns alike; leave it empty for any age.')),
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label' }, 'Repeats'),
        h('div', { class: 'opt-line' }, h('span', null, 'after'), E.ytRepeat, h('span', null, 'days')),
        h('p', { class: 'help' }, 'How long before a video airs again, when the channels have enough of them.')),
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label' }, 'Run out'),
        radios('runout', YT_RUNOUT, d.deadAir, (e) => { S.draft.deadAir = e.target.value === 'true'; changed(); ytHelp(); }, 'When everything has aired'),
        E.runHelp),
      h('div', { class: 'opt-group' },
        h('span', { class: 'opt-label', id: 'reruns-l' }, 'Reruns'),
        E.ytMix,
        E.mixHelp),
      quality,
      E.optErr);
  }

  function ytHelp() {
    const d = S.draft;
    E.modeHelp.textContent = (YT_MODES.find((o) => o.v === d.ytMode) || YT_MODES[0]).help;
    if (d.ytMode === 'uploads') {
      E.mixHelp.textContent = (YT_RERUNS.find((o) => o.v === d.rerunMix) || YT_RERUNS[1]).help;
      E.runHelp.textContent = YT_RUNOUT[d.deadAir ? 1 : 0].help;
    }
    E.qualHelp.textContent = d.maxHeight >= 1080 ? 'The sharpest picture, and the most bandwidth.'
      : d.maxHeight >= 720 ? 'The most YouTube is asked for. Suits most TVs.'
        : 'Lighter on bandwidth; fine on small screens.';
  }

  // After a save, the settings as the server has them.
  function ytSyncSettings() {
    const d = S.draft;
    if (E.ytPl && E.ytPl.value !== d.plLink) E.ytPl.value = d.plLink;
    for (const [el, key] of [[E.ytMin, 'minMinutes'], [E.ytMax, 'maxMinutes'], [E.ytRepeat, 'repeatDays'], [E.ytAge, 'maxAgeDays']]) {
      if (el && el.value !== d[key]) el.value = d[key];
    }
    if (E.ytMix) E.ytMix.value = d.rerunMix;
    const r = $(`input[name=quality][value="${d.maxHeight}"]`);
    if (r) r.checked = true;
    const run = $(`input[name=runout][value="${d.deadAir}"]`);
    if (run) run.checked = true;
    ytHelp();
  }

  function ytKicker() {
    const k = E.kicker;
    k.replaceChildren();
    if (isNew()) {
      k.append(h('span', { class: 'kind k-new' }, 'New YouTube channel'), h('span', null, 'Goes on the air when you create it'));
      return;
    }
    const ch = S.ch;
    k.append(h('span', { class: 'kind k-youtube' }, 'YouTube'));
    if (!ch) return;
    const busy = ytBusy(ch);
    if (ch.error) k.append(h('span', { class: 'err', title: ch.error, text: ch.error }));
    else if (busy) {
      const since = (ch.sources || []).map((s) => s.listingSince).filter(Boolean).sort()[0];
      k.append(h('span', { class: 'upd' }, since ? `Listing videos since ${fmtTime(since)}` : 'Listing videos'));
    }
    if (!ch.error && (ch.videos || !busy)) {
      const all = ch.videos || 0, on = ch.items || 0;
      if (ch.youtube && ch.youtube.playlist) k.append(h('span', { text: `${plural(on, 'video', 'videos')} in order` }));
      else k.append(h('span', { text: on === all ? `${plural(all, 'video', 'videos')} in rotation` : `${on.toLocaleString()} of ${plural(all, 'video', 'videos')} in rotation` }));
    }
    if (ch.now && ch.now.title) {
      k.append(h('span', { class: 'now', title: [ch.now.title, ch.now.subtitle].filter(Boolean).join(': ') },
        h('b', null, 'Now'), ch.now.title,
        ch.now.subtitle ? h('em', null, ', ' + ch.now.subtitle) : null,
        ch.now.end ? ` until ${fmtTime(ch.now.end)}` : null));
    }
  }

  // The state changed: sources' listings, what's on. When listing ends, the schedule fills in.
  function ytStateChanged() {
    const busy = ytBusy(S.ch);
    ytKicker();
    ytRenderSources();
    if (S.yt.wasBusy && !busy && !isNew()) loadSchedule(S.schedToken);
    else renderSchedule();
    S.yt.wasBusy = busy;
    ytWatch();
  }

  // While a source is being listed, the state is read every few seconds.
  function ytWatch() {
    clearTimeout(S.yt.timer);
    if (!E.yt || isNew() || !ytBusy(S.ch)) return;
    S.yt.timer = setTimeout(() => { if (E.yt) refreshState(); }, YT_POLL);
  }

  function ytListingMsg() {
    if (S.draft.ytMode === 'playlist') {
      const lk = S.yt.pls.get(S.draft.playlist), n = (lk && lk.data && lk.data.videos) || 0;
      const mins = Math.round(n / YT_LIST_RATE / 60);
      return mins > 1 ? `Listing the playlist's ${n.toLocaleString()} videos; this takes about ${mins} minutes.` : 'Listing the playlist from YouTube; this takes a few seconds.';
    }
    const busy = ((S.ch && S.ch.sources) || []).filter(ytBusySrc).map((s) => ytInfo(s.channel));
    const total = busy.reduce((n, i) => n + i.total, 0);
    const big = busy.reduce((a, i) => (!a || i.total > a.total ? i : a), null);
    const secs = total / YT_LIST_RATE;
    const how = !total ? 'this takes a minute or so, longer for big channels'
      : secs < 90 ? 'this takes about a minute' : `this takes about ${Math.round(secs / 60)} minutes`;
    if (big && big.total >= 1000) return `Listing videos; ${big.name} has about ${big.total.toLocaleString()}, ${how}.`;
    return `Listing videos from YouTube; ${how}.`;
  }

  function ytNothingOn() {
    const ch = S.ch, srcs = (ch && ch.sources) || [];
    if (S.draft.ytMode === 'playlist') {
      if (!S.draft.playlist) return "Nothing scheduled. Paste a playlist's link and save.";
      if (ytBusy(ch)) return ytListingMsg();
      if (ch && ch.videos && !ch.items) return "None of the playlist's videos can play yet: private, deleted, members-only and upcoming ones are left out.";
      if (S.refreshing) return 'Building the schedule.';
      if (srcs.length && srcs.every((s) => s.failed)) return "Couldn't list the playlist, so nothing's scheduled yet. The server tries again within the hour.";
      return 'Nothing scheduled yet. The server may still be loading this channel.';
    }
    if (!S.draft.sources.length) return 'Nothing scheduled. Add a YouTube channel and save.';
    if (ytBusy(ch)) return ytListingMsg();
    // Known from the channel's state at once, without waiting on the schedule.
    const age = Number(ch && ch.youtube && ch.youtube.maxAgeDays) || 0;
    if (ch && ch.videos && !ch.items && age) return `Nothing from the last ${plural(age, 'day', 'days')} fits, so the channel is off the air until a new upload does. Allow older videos to fill the schedule.`;
    if (ch && ch.videos && !ch.items) return "None of the videos fit the length limits. Widen them to fill the schedule.";
    if (S.refreshing) return 'Building the schedule.';
    if (srcs.length && srcs.every((s) => s.failed)) return "Couldn't list the channels' videos, so nothing's scheduled yet. The server tries again within the hour.";
    return 'Nothing scheduled yet. The server may still be loading this channel.';
  }

  function ytRenderSources() {
    if (!E.picks) return;
    if (S.draft.ytMode === 'playlist') { ytRenderPlaylist(); return; }
    const d = S.draft, box = E.picks;
    const buttons = [...box.querySelectorAll('button')];
    const focusIdx = buttons.indexOf(document.activeElement);
    E.picksCount.textContent = d.sources.length;
    if (!d.sources.length) {
      box.replaceChildren(h('p', { class: 'picks-empty' }, 'Nothing added yet. Search for a YouTube channel, or paste its link.'));
    } else {
      box.replaceChildren(h('ul', { class: 'ysrc-list' }, d.sources.map((ref) => {
        const i = ytInfo(ref), st = i.st;
        let status = null, bad = false;
        if (i.lookErr && !(st && st.listed)) {
          bad = true;
          status = h('span', { class: 'bad', title: i.lookErr, text: `Can't play: ${i.lookErr}` });
        } else if (st && st.failed) {
          bad = true;
          status = h('span', { class: 'bad', title: st.error || '', text: st.error ? `Couldn't list: ${st.error}` : "Couldn't list its videos; trying again soon" });
        } else if (st && ytBusySrc(st)) {
          status = h('span', { class: 'upd' }, 'Listing');
        } else if (st && st.listed) {
          status = h('span', { title: `${(st.playable || 0).toLocaleString()} fit the length and age limits`, text: plural(st.videos || 0, 'video', 'videos') });
        } else if (i.total) {
          status = h('span', { text: `about ${plural(i.total, 'video', 'videos')}` });
        } else if (i.looking) {
          status = h('span', { class: 'wait', text: 'looking up' });
        }
        return h('li', { class: 'ysrc' + (bad ? ' bad' : '') },
          avatar(i.name, i.image, true),
          h('div', { class: 'ys-body' },
            h('div', { class: 'ys-name', title: ref, text: i.name }),
            h('div', { class: 'ys-meta' }, i.handle && i.handle !== i.name ? h('span', { text: i.handle }) : null, status)),
          h('button', { class: 'x', type: 'button', 'aria-label': `Remove ${i.name}`, title: 'Remove', onclick: () => ytRemove(ref) }, '×'));
      })));
    }
    if (focusIdx >= 0) {
      const now = box.querySelectorAll('button');
      const t = now[Math.min(focusIdx, now.length - 1)];
      (t || E.search).focus();
    }
  }

  const ytFind = (c) => S.draft.sources.find((r) => ytKey(r) === ytKey(c.url) || (c.id && ytInfo(r).id === c.id));

  function ytAdd(c) {
    const d = S.draft;
    if (ytFind(c) || d.sources.length >= YT_MAX_SOURCES) return;
    d.sources.push(c.url);
    const cur = S.yt.info.get(ytKey(c.url));
    if (!cur || !cur.full) S.yt.info.set(ytKey(c.url), { status: 'ok', data: c });
    ytLookup(c.url);
    // A new channel takes the name of the first channel added to it.
    ytNameFrom(c.name);
    ytSourcesChanged();
  }

  // A new channel without a name takes one: its first channel's, or its playlist's.
  function ytNameFrom(name) {
    const d = S.draft;
    if (!isNew() || d.name.trim() || !name) return;
    d.name = String(name).replace(/[\/\\]/g, ' ').replace(/^[.\s]+/, '').trim().slice(0, 48);
    E.name.value = d.name;
    S.touched.name = false;
    const li = $(`#channels .ch[data-number="${S.sel}"]`);
    if (li) $('.ch-name', li).textContent = d.name || 'New channel';
    if (!squash(d.callSign)) renderLogo();
  }

  function ytRemove(ref) {
    S.draft.sources = S.draft.sources.filter((r) => r !== ref);
    ytSourcesChanged();
  }

  function ytSourcesChanged() {
    ytRenderSources();
    ytRenderResults();
    renderSchedule();
    changed();
  }

  let ytInputTimer = 0;
  function ytOnInput() {
    clearTimeout(ytInputTimer);
    const v = E.search.value.trim();
    if (!v) {
      S.yt.token++;
      S.yt.view = null;
      ytRenderResults();
    } else if (YT_LINK.test(v)) {
      // A pasted link is looked up at once; words wait for Enter.
      ytInputTimer = setTimeout(() => ytSearch(v), 250);
    }
  }

  function ytSearchKeys(e) {
    if (e.key === 'Escape') {
      if (E.search.value) {
        e.preventDefault();
        E.search.value = '';
        ytOnInput();
      } else {
        E.search.blur();
      }
    } else if (e.key === 'ArrowDown') {
      const b = E.ytRes.querySelector('button');
      if (b) { e.preventDefault(); b.focus(); }
    }
  }

  async function ytSearch(q) {
    q = String(q || '').trim();
    clearTimeout(ytInputTimer);
    if (!q) { S.yt.view = null; ytRenderResults(); return; }
    const link = YT_LINK.test(q);
    const key = (link ? 'u:' : 'q:') + q.toLowerCase();
    const token = ++S.yt.token;
    if (link && YT_PLAYLIST.test(q)) {
      S.yt.view = { q, link, status: 'playlist', items: [] };
      ytRenderResults();
      return;
    }
    const cached = S.yt.searches.get(key);
    if (cached) {
      S.yt.view = { q, link, status: 'ok', items: cached };
      ytRenderResults();
      return;
    }
    S.yt.view = { q, link, status: 'loading', items: [] };
    ytRenderResults();
    try {
      let items;
      if (link) {
        const c = await api('GET', `/youtube/channel?u=${enc(q)}`);
        items = [c];
        S.yt.info.set(ytKey(c.url), { status: 'ok', data: c, full: true });
      } else {
        const r = await api('GET', `/youtube/search?q=${enc(q)}`);
        items = Array.isArray(r && r.channels) ? r.channels : [];
        for (const c of items) {
          const cur = S.yt.info.get(ytKey(c.url));
          if (!cur || !cur.full) S.yt.info.set(ytKey(c.url), { status: 'ok', data: c });
        }
      }
      S.yt.searches.set(key, items);
      if (token !== S.yt.token || !E.yt) return;
      S.yt.view = { q, link, status: 'ok', items };
    } catch (e) {
      if (token !== S.yt.token || !E.yt) return;
      S.yt.view = { q, link, status: 'error', items: [], error: e.message };
      // yt-dlp being installed: try again shortly.
      if (e.status === 503 && /being installed/.test(e.message)) {
        setTimeout(() => { if (token === S.yt.token && E.yt) ytSearch(q); }, 8000);
      }
    }
    ytRenderResults();
  }

  function ytRow(c) {
    const added = !!ytFind(c);
    const full = S.draft.sources.length >= YT_MAX_SOURCES;
    const info = S.yt.info.get(ytKey(c.url));
    const total = (info && info.data && info.data.videos) || c.videos;
    const meta = [];
    if (c.handle) meta.push(h('span', { text: c.handle }));
    if (c.subscribers) meta.push(h('span', { text: `${compact(c.subscribers)} subscribers` }));
    if (total) meta.push(h('span', { text: `about ${plural(total, 'video', 'videos')}` }));
    return h('li', { class: 'ytrow' + (added ? ' added' : '') },
      avatar(c.name, c.image),
      h('div', { class: 'yr-body' },
        h('div', { class: 'yr-name', dir: 'auto', text: c.name }),
        meta.length ? h('div', { class: 'yr-meta' }, meta) : null,
        c.description ? h('p', { class: 'yr-desc', dir: 'auto', text: c.description }) : null),
      h('button', {
        class: 'btn small' + (added ? ' ghost' : ''), type: 'button', 'aria-pressed': added, 'aria-label': `Add ${c.name}`,
        disabled: !added && full, title: !added && full ? `A channel plays up to ${YT_MAX_SOURCES} YouTube channels` : null,
        onclick: () => { const r = ytFind(c); if (r) ytRemove(r); else ytAdd(c); },
      }, added ? 'Added' : 'Add'));
  }

  function ytTry(q) {
    E.search.value = q;
    ytSearch(q);
  }

  function ytRenderResults() {
    if (!E.ytRes) return;
    const v = S.yt.view;
    const rows = [...E.ytRes.children];
    const focusIdx = rows.findIndex((r) => r.contains(document.activeElement));
    E.ytRes.replaceChildren();
    E.ytMsg.replaceChildren();
    E.ytCount.textContent = '';
    if (!v) {
      E.ytMsg.append(
        h('p', { class: 'big' }, 'Find channels to play'),
        h('p', null, 'Search YouTube by name or topic, like ',
          h('button', { class: 'linkish', type: 'button', onclick: () => ytTry('boiler room') }, 'boiler room'), ' or ',
          h('button', { class: 'linkish', type: 'button', onclick: () => ytTry('dj sets') }, 'dj sets'),
          ", or paste a channel's link."),
        h('p', null, "The channels' uploads take turns on the air, and new uploads air first. Nothing is downloaded: videos stream from YouTube as they air."));
      return;
    }
    if (v.status === 'loading') {
      E.ytMsg.append(h('p', null, v.link ? 'Looking up the channel on YouTube' : `Searching YouTube for “${v.q}”`));
      for (let i = 0; i < (v.link ? 1 : 6); i++) {
        E.ytRes.append(h('li', { class: 'ytrow skel-row', 'aria-hidden': 'true' }, h('span', { class: 'av' }), h('span', { class: 'skl' })));
      }
      return;
    }
    if (v.status === 'playlist') {
      // A playlist's link, pasted to find channels: offer to play the playlist instead.
      E.ytMsg.append(
        h('p', { class: 'big' }, "That's a playlist's link."),
        h('p', null, "This channel can play the playlist in its order, in place of channels' uploads."),
        h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', onclick: () => { S.draft.plLink = v.q; ytSetMode('playlist'); } }, 'Play the playlist')));
      return;
    }
    if (v.status === 'error') {
      E.ytMsg.append(
        h('p', { class: 'big err' }, v.link ? "Couldn't look up that channel." : "Couldn't search YouTube."),
        h('p', null, v.error),
        h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', onclick: () => ytSearch(v.q) }, 'Try again')));
      return;
    }
    if (!v.items.length) {
      E.ytMsg.append(h('p', { class: 'big' }, 'No channels found.'),
        h('p', null, `Nothing on YouTube matches “${v.q}”. Try other words, or paste the channel's link.`));
      return;
    }
    if (!v.link) E.ytCount.textContent = plural(v.items.length, 'channel', 'channels');
    for (const c of v.items) E.ytRes.append(ytRow(c));
    if (focusIdx >= 0) {
      const b = E.ytRes.children[Math.min(focusIdx, E.ytRes.children.length - 1)];
      if (b) b.querySelector('button').focus();
    }
  }

  // ---------- Main: YouTube playlist ----------
  // A YouTube channel may play a playlist in its order instead of channels' uploads. Its link is
  // looked up as it's pasted, for the playlist's title, its channel and how many videos it has.
  function ytPlaylistPanel() {
    E.ytPl = h('input', {
      class: 'input', id: 'yt-playlist', value: S.draft.plLink, placeholder: 'youtube.com/playlist?list=...', autocomplete: 'off', spellcheck: 'false',
      'aria-describedby': 'yt-pl-help', oninput: ytPlInput,
    });
    E.ytPlMsg = h('div', { class: 'gmsg', 'aria-live': 'polite' });
    return h('section', { class: 'lib ytfind ytpl', 'aria-label': 'YouTube playlist' },
      h('form', { class: 'fbar', onsubmit: (e) => { e.preventDefault(); ytPlLookup(E.ytPl.value, true); } },
        h('label', { class: 'label', for: 'yt-playlist' }, "The playlist's link"),
        h('div', { class: 'fbar-row' },
          h('div', { class: 'search pl-link' }, E.ytPl),
          h('button', { class: 'btn', type: 'submit' }, 'Look up')),
        h('p', { class: 'help', id: 'yt-pl-help' }, 'Or the link of a video played from it: either has list= in it.')),
      h('div', { class: 'scroller' }, E.ytPlMsg));
  }

  let ytPlTimer = 0;
  function ytPlInput() {
    const d = S.draft;
    d.plLink = E.ytPl.value;
    d.playlist = d.plLink.trim();
    clearTimeout(ytPlTimer);
    if (YT_PLAYLIST.test(d.playlist)) ytPlTimer = setTimeout(() => ytPlLookup(d.playlist), 250);
    ytRenderPlaylist();
    renderSchedule();
    changed();
  }

  // Looks up a playlist once; again asks again after a failure.
  function ytPlLookup(link, again) {
    link = String(link || '').trim();
    clearTimeout(ytPlTimer);
    const cur = S.yt.pls.get(link);
    if (!YT_PLAYLIST.test(link) || (cur && (cur.status !== 'error' || !again))) { ytRenderPlaylist(); return; }
    S.yt.pls.set(link, { status: 'loading' });
    ytRenderPlaylist();
    api('GET', `/youtube/playlist?u=${enc(link)}`).then((p) => {
      S.yt.pls.set(link, { status: 'ok', data: p || {} });
      // A new channel takes the playlist's name.
      if (E.ytPl && S.draft.playlist === link && p && isNew() && !S.draft.name.trim()) { ytNameFrom(p.title); changed(); }
    }, (e) => {
      S.yt.pls.set(link, { status: 'error', error: e.message });
      // yt-dlp being installed: try again shortly.
      if (e.status === 503 && /being installed/.test(e.message)) {
        setTimeout(() => { if (E.ytPl && S.draft.playlist === link) ytPlLookup(link, true); }, 8000);
      }
    }).finally(() => {
      if (E.ytPl) { ytRenderPlaylist(); renderSchedule(); }
    });
  }

  // A playlist's picture: its first video's thumbnail.
  function plThumb(image, small) {
    const el = h('span', { class: 'plthumb' + (small ? ' sm' : ''), 'aria-hidden': 'true' });
    if (image) {
      el.append(h('img', { src: image, alt: '', loading: 'lazy', decoding: 'async', referrerpolicy: 'no-referrer', draggable: 'false',
        onerror: (e) => e.target.remove() }));
    }
    return el;
  }

  function ytPlCard(p) {
    const meta = [];
    if (p.owner) meta.push(h('span', { text: p.owner }));
    if (p.videos) meta.push(h('span', { text: plural(p.videos, 'video', 'videos') }));
    return h('div', { class: 'plcard' }, plThumb(p.image),
      h('div', { class: 'yr-body' },
        h('div', { class: 'yr-name', dir: 'auto', text: p.title || 'Untitled playlist' }),
        meta.length ? h('div', { class: 'yr-meta' }, meta) : null,
        p.url ? h('a', { class: 'linkish', href: p.url, target: '_blank', rel: 'noopener noreferrer' }, 'Open on YouTube') : null));
  }

  // The playlist: on the left, what a lookup of its link found, or why it won't do; on the right,
  // the playlist as the server has it once saved, and how its listing is going.
  function ytRenderPlaylist() {
    const d = S.draft, link = d.playlist;
    const lk = (link && S.yt.pls.get(link)) || null;
    const found = lk && lk.status === 'ok' ? lk.data : null;
    const cfg = S.ch && S.ch.youtube;
    const st = cfg && cfg.playlist && ytListId(cfg.playlist) === ytListId(link) ? (S.ch.sources || [])[0] || null : null;
    if (E.ytPlMsg) {
      const how = [
        h('p', null, "Its videos play from the top in the playlist's order, then again from the first. Videos added to it join within a day, or when you save."),
        h('p', null, 'Under Airs, a schedule can air it only at set times, like four episodes a night from the first, each night picking up where the last left off.'),
        h('p', null, 'Nothing is downloaded: videos stream from YouTube as they air.'),
      ];
      const box = E.ytPlMsg;
      if (!link) {
        box.replaceChildren(h('p', { class: 'big' }, 'Play a playlist in order'),
          h('p', null, "Paste a playlist's link above: a season someone put together, a course, a band's live sets."), ...how);
      } else if (!YT_PLAYLIST.test(link)) {
        box.replaceChildren(h('p', { class: 'big err' }, "That isn't a playlist's link."),
          h('p', null, "A playlist's link has list= in it, like youtube.com/playlist?list=PL..., and so does the link of a video played from a playlist."));
      } else if (lk && lk.status === 'error') {
        box.replaceChildren(h('p', { class: 'big err' }, "Couldn't look up that playlist."), h('p', null, lk.error),
          h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', onclick: () => ytPlLookup(link, true) }, 'Try again')));
      } else if (!found) {
        box.replaceChildren(h('p', null, 'Looking up the playlist on YouTube'),
          h('div', { class: 'plcard skel-row', 'aria-hidden': 'true' }, plThumb(''), h('span', { class: 'skl' })));
      } else {
        box.replaceChildren(ytPlCard(found), ...how);
      }
    }
    E.picksCount.textContent = '';
    if (!link) {
      E.picks.replaceChildren(h('p', { class: 'picks-empty' }, "No playlist yet. Paste its link."));
      return;
    }
    const name = (st && st.name) || (found && found.title) || link.replace(/^https?:\/\/(www\.)?/i, '');
    const owner = (st && st.owner) || (found && found.owner) || '';
    let status = null, bad = false;
    if (st && st.failed) {
      bad = true;
      status = h('span', { class: 'bad', title: st.error || '', text: st.error ? `Couldn't list: ${st.error}` : "Couldn't list its videos; trying again soon" });
    } else if (st && ytBusySrc(st)) {
      status = h('span', { class: 'upd' }, 'Listing');
    } else if (st && st.listed) {
      const waiting = (st.videos || 0) - (st.playable || 0);
      status = h('span', { title: waiting > 0 ? `${plural(waiting, 'more is', 'more are')} upcoming, live, or without a length yet` : null, text: plural(st.playable || 0, 'video', 'videos') });
    } else if (lk && lk.status === 'error') {
      bad = true;
      status = h('span', { class: 'bad', title: lk.error, text: `Can't play: ${lk.error}` });
    } else if (found && found.videos) {
      status = h('span', { text: `about ${plural(found.videos, 'video', 'videos')}` });
    } else if (lk && lk.status === 'loading') {
      status = h('span', { class: 'wait', text: 'looking up' });
    }
    E.picks.replaceChildren(h('ul', { class: 'ysrc-list' }, h('li', { class: 'ysrc pl' + (bad ? ' bad' : '') },
      plThumb((st && st.image) || (found && found.image), true),
      h('div', { class: 'ys-body' },
        h('div', { class: 'ys-name', dir: 'auto', title: link, text: name }),
        h('div', { class: 'ys-meta' }, owner ? h('span', { text: owner }) : null, status)))));
  }

  // ---------- Schedule ----------
  async function loadSchedule(token) {
    const num = S.origNumber;
    if (!num || num === 'new') return;
    if (S.sched.number !== num) S.sched = { number: num, status: 'loading', programs: [], error: '' };
    renderSchedule();
    try {
      const r = await api('GET', `/channels/${enc(num)}/schedule`);
      if (token !== S.schedToken || S.origNumber !== num) return;
      S.sched = { number: num, status: 'ok', programs: Array.isArray(r && r.programs) ? r.programs : [], error: '' };
    } catch (e) {
      if (token !== S.schedToken || S.origNumber !== num) return;
      S.sched = { number: num, status: 'error', programs: [], error: e.message };
    }
    renderSchedule();
  }

  function startScheduleRefresh() {
    const token = ++S.schedToken;
    S.refreshing = true;
    renderSchedule();
    let i = 0;
    const step = async () => {
      if (token !== S.schedToken) return;
      await Promise.all([loadSchedule(token), refreshState()]);
      if (token !== S.schedToken) return;
      i++;
      if (i < SCHEDULE_POLLS.length) setTimeout(step, SCHEDULE_POLLS[i] - SCHEDULE_POLLS[i - 1]);
      else { S.refreshing = false; renderSchedule(); }
    };
    setTimeout(step, SCHEDULE_POLLS[0]);
  }

  const dayKey = (d) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
  function dayLabel(d) {
    const t = new Date();
    t.setDate(t.getDate() + 1);
    if (dayKey(d) === dayKey(t)) return 'Tomorrow';
    return d.toLocaleDateString([], { weekday: 'long' });
  }

  function renderSchedule() {
    if (!E.sched) return;
    E.schedHead.replaceChildren(S.refreshing ? h('span', { class: 'upd' }, 'Updating') : '');
    const box = E.sched;
    const msg = (t, err) => h('p', { class: 'sched-msg' + (err ? ' err' : '') }, t);
    if (isNew()) { box.replaceChildren(msg('The schedule shows up here once the channel is created.')); return; }
    const s = S.sched;
    if (s.number !== S.origNumber || s.status === 'loading' || s.status === 'idle') { box.replaceChildren(msg('Loading the schedule')); return; }
    if (s.status === 'error') { box.replaceChildren(msg(`Couldn't load the schedule: ${s.error}`, true)); return; }
    const now = Date.now();
    const progs = s.programs.filter((p) => !(Date.parse(p.end) <= now));
    if (!progs.length && E.yt) { box.replaceChildren(msg(ytNothingOn())); return; }
    if (!progs.length && E.metaOnly) {
      box.replaceChildren(msg(S.draft.kind === 'folder' ? 'Nothing scheduled: there are no playable videos in the folder yet.' : 'Nothing scheduled.'));
      return;
    }
    if (!progs.length) {
      const nothing = KINDS.every((k) => !S.draft[k].size);
      box.replaceChildren(msg(S.refreshing
        ? 'Building the schedule from the new picks.'
        : nothing ? 'Nothing scheduled. Pick something to play and save.'
          : 'Nothing scheduled yet. The server may still be loading this channel.'));
      return;
    }
    const ol = h('ol', { class: 'prog-list' });
    let day = dayKey(new Date());
    for (const p of progs) {
      const st = new Date(p.start);
      if (!isNaN(st) && dayKey(st) !== day) {
        day = dayKey(st);
        ol.append(h('li', { class: 'prog-day' }, dayLabel(st)));
      }
      const on = Date.parse(p.start) <= now && now < Date.parse(p.end);
      ol.append(h('li', { class: 'prog' + (on ? ' now' : '') + (p.offAir ? ' off' : '') },
        h('time', { datetime: p.start }, on ? 'Now' : fmtTime(p.start)),
        h('div', { class: 'pr-body' },
          h('div', { class: 't', text: p.title || 'Untitled' }),
          p.subtitle ? h('div', { class: 's', text: p.subtitle }) : null)));
    }
    if (E.yt && ytBusy(S.ch)) box.replaceChildren(msg(ytListingMsg()), ol);
    else box.replaceChildren(ol);
  }

  // ---------- Save and delete ----------
  async function save() {
    if (!E.save || E.save.disabled || S.saving) return;
    const fresh = isNew();
    const kind = S.draft.kind;
    const yt = kind === 'youtube', metaOnly = kind === 'folder' || kind === 'weather';
    const sent = sig(S.draft);
    const body = bodyOf(S.draft);
    const path = fresh ? 'new' : S.origNumber;
    S.saving = true;
    S.saveMsg = null;
    updateDirty();
    let saved;
    try {
      saved = await api('PUT', `/channels/${enc(path)}`, body);
    } catch (e) {
      S.saving = false;
      if (e.status === 409) S.serverErr.number = e.message;
      else if (e.status === 400 && /^the schedule/i.test(e.message)) S.serverErr.sched = e.message;
      else if (e.status === 400 && /number/i.test(e.message)) S.serverErr.number = e.message;
      else if (e.status === 400 && /call sign|category|description/i.test(e.message)) S.serverErr.meta = e.message;
      else if (e.status === 400 && /name/i.test(e.message)) S.serverErr.name = e.message;
      else S.saveMsg = { text: `Couldn't save: ${e.message}`, cls: 'err' };
      updateDirty();
      return;
    }
    const ch = saved && typeof saved === 'object' && saved.number ? saved : { number: body.number, name: body.name, ...metaBody(S.draft) };
    ch.kind = ch.kind || kind;
    if (metaOnly) ch.folder = ch.folder || (S.ch && S.ch.folder);
    if (yt && !ch.youtube) ch.youtube = body.youtube;
    if (kind === 'jellyfin' && !ch.config) {
      ch.config = { order: body.order, maxBitrate: body.maxBitrate };
      for (const k of KINDS) ch.config[k] = body[k];
    }
    const list = S.state.channels;
    const i = fresh ? -1 : list.findIndex((c) => c.number === S.origNumber);
    if (i >= 0) list[i] = ch; else list.push(ch);
    list.sort((a, b) => cmpNum(a.number, b.number));

    S.saving = false;
    S.ch = ch;
    S.origNumber = ch.number;
    S.sel = ch.number;
    const fromServer = yt ? ytDraftFrom(ch) : metaOnly ? metaDraftFrom(ch) : draftFrom(ch);
    // Keep anything typed while the request was in flight.
    if (sig(S.draft) === sent) S.draft = fromServer;
    S.base = sig(fromServer);
    S.touched = {};
    S.serverErr = {};
    S.saveMsg = { text: fresh ? 'Created' : 'Saved', cls: 'ok' };
    history.replaceState(null, '', hashFor(ch.number));

    if (E.num.value !== S.draft.number) { E.num.value = S.draft.number; sizeNum(); }
    if (E.name.value !== S.draft.name) E.name.value = S.draft.name;
    E.del.hidden = metaOnly;
    E.save.textContent = 'Save changes';
    syncMetaInputs();
    fitDesc();
    schedSaved();
    if (yt) {
      ytSyncSettings();
      ytRenderSources();
      ytRenderResults();
    } else if (metaOnly) {
      renderMetaInfo();
    } else {
      syncCards();
      updateTabs();
      renderPicks();
    }
    updateKicker();
    renderChannels();
    updateDirty();
    setTitle();
    toast(`${fresh ? 'Created' : 'Saved'} ${ch.number} ${ch.name}`);
    S.sched = { number: null, status: 'idle', programs: [], error: '' };
    startScheduleRefresh();
    if (yt) {
      S.yt.wasBusy = ytBusy(ch);
      ytWatch();
    }
  }

  async function deleteChannel() {
    const ch = S.ch;
    if (!ch) return;
    const ok = await confirmDialog({
      title: `Delete ${ch.number} ${ch.name}?`,
      body: ch.kind === 'youtube'
        ? 'It comes off the lineup right away, with its list of videos and its schedule. Nothing changes on YouTube.'
        : 'It comes off the lineup right away and its picks are lost. Nothing changes on the Jellyfin server.',
      ok: 'Delete channel', cancel: 'Cancel', danger: true,
    });
    if (!ok) return;
    E.del.disabled = true;
    try {
      await api('DELETE', `/channels/${enc(ch.number)}`);
    } catch (e) {
      E.del.disabled = false;
      S.saveMsg = { text: `Couldn't delete: ${e.message}`, cls: 'err' };
      updateDirty();
      return;
    }
    S.state.channels = S.state.channels.filter((c) => c.number !== ch.number);
    S.draft = null;
    history.pushState(null, '', hashFor(null));
    openTarget(null);
    toast(`Deleted ${ch.number} ${ch.name}`);
    const btn = $('#main .btn');
    if (btn) btn.focus();
  }

  // ---------- Backups ----------
  // Copies of the channel setup the server keeps (internal/admin/backups.go and restore.go): a
  // line under the account with the latest, and a page (#backups) to back up now, upload a
  // backup, download, delete and restore, with a dialog that says what a restore changes first.
  const BACKUPS = 'backups';
  const BK_MAX = 16 << 20;
  const BK_REASON = {
    change: { label: 'After a change', help: 'Made by itself 30 seconds after changes here or by an agent' },
    daily: { label: 'Daily', help: 'Made by itself once a day, when the setup changed some other way' },
    restore: { label: 'Before a restore', help: 'The setup just before a restore: restore this to undo it' },
    manual: { label: 'By hand', help: 'Kept until you delete it' },
    uploaded: { label: 'Uploaded', help: 'Kept until you delete it' },
  };
  const BK_PARTS = { settings: 'settings', details: 'details', logo: 'logo', schedule: 'schedule' };
  const BK = { list: null, err: null, folder: '', pending: false, keep: null, loading: null, timer: 0, busy: '', r: null };

  function bkAgo(t) {
    const s = (Date.now() - Date.parse(t)) / 1000;
    if (!(s >= 60)) return 'just now';
    if (s < 3600) return `${Math.floor(s / 60)} min ago`;
    if (s < 86400) return plural(Math.floor(s / 3600), 'hour', 'hours') + ' ago';
    return plural(Math.floor(s / 86400), 'day', 'days') + ' ago';
  }
  function bkWhen(t) {
    const d = new Date(t);
    if (isNaN(d)) return '';
    const now = new Date();
    const day = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const days = Math.round((day(now) - day(d)) / 86400000);
    const time = fmtTime(d);
    if (days === 0) return `Today ${time}`;
    if (days === 1) return `Yesterday ${time}`;
    if (days > 1 && days < 7) return `${d.toLocaleDateString([], { weekday: 'long' })} ${time}`;
    return `${d.toLocaleDateString([], { month: 'short', day: 'numeric', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' })}, ${time}`;
  }
  const bkSize = (n) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`);
  const bkReason = (b) => BK_REASON[b.reason] || { label: b.reason || 'Backup', help: '' };
  const bkList = (names) => (names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`);

  // What a backup holds, in a line.
  function bkContents(b) {
    const n = { jellyfin: 0, youtube: 0, folder: 0 };
    for (const f of b.folders || []) n[f.kind] = (n[f.kind] || 0) + 1;
    const kinds = [];
    if (n.jellyfin) kinds.push(`${n.jellyfin} Jellyfin`);
    if (n.youtube) kinds.push(`${n.youtube} YouTube`);
    if (n.folder) kinds.push(plural(n.folder, 'folder', 'folders'));
    if (b.weather) kinds.push('weather');
    let s = plural((b.folders || []).length + (b.weather ? 1 : 0), 'channel', 'channels');
    if (kinds.length) s += ` (${kinds.join(', ')})`;
    if (b.account) s += ', the Jellyfin account';
    return `${s}. ${bkSize(b.size || 0)}`;
  }

  function loadBackups() {
    if (BK.loading) return BK.loading;
    BK.loading = (async () => {
      try {
        const r = await api('GET', '/backups');
        BK.list = Array.isArray(r && r.backups) ? r.backups : [];
        BK.folder = (r && r.folder) || '';
        BK.pending = !!(r && r.pending);
        BK.keep = (r && r.keep) || null;
        BK.err = null;
      } catch (e) {
        BK.err = e;
      } finally {
        BK.loading = null;
      }
      renderBackupLine();
      if (E.backups) renderBackupList();
      bkTick();
    })();
    return BK.loading;
  }
  // The list is looked at again now and then: often while a backup is on its way or the page is open.
  function bkTick() {
    clearTimeout(BK.timer);
    const soon = BK.pending || S.sel === BACKUPS;
    BK.timer = setTimeout(() => { if (document.hidden) bkTick(); else loadBackups(); }, soon ? 6000 : 30000);
  }

  // The side line: the latest backup, or that one is on its way.
  function renderBackupLine() {
    const box = $('#bk-side');
    if (!box) return;
    let state = '', text = 'Loading';
    if (BK.err) {
      state = 'err';
      text = BK.err.status === 503 ? 'Off on this server' : "Can't list them";
    } else if (BK.list) {
      const latest = BK.list.find((b) => !b.error);
      if (BK.pending) { state = 'wait'; text = 'Backing up your changes'; }
      else if (latest) { state = 'ok'; text = `Latest ${bkAgo(latest.created)}`; }
      else text = 'None yet';
    }
    const cur = S.sel === BACKUPS;
    const had = box.contains(document.activeElement);
    box.replaceChildren(h('a', {
      class: 'bk-line', href: '#' + BACKUPS, 'data-number': BACKUPS, 'aria-current': cur ? 'page' : null, onclick: linkNav,
    },
    h('span', { class: 'bk-dot' + (state ? ' ' + state : ''), 'aria-hidden': 'true' }),
    h('span', { class: 'bk-line-body' }, h('b', null, 'Backups'), h('span', { text })),
    h('span', { class: 'bk-go', 'aria-hidden': 'true' })));
    if (had) $('.bk-line', box).focus();
  }

  // The page.
  function renderBackupsPage() {
    E = { backups: true };
    E.bkLabel = h('input', {
      class: 'input', id: 'bk-label', maxlength: '60', autocomplete: 'off', placeholder: 'A note, like "before the big reshuffle"',
    });
    E.bkNow = h('button', { class: 'btn', type: 'submit', id: 'bk-now' }, 'Back up now');
    E.bkFile = h('input', {
      type: 'file', id: 'bk-file', class: 'visually-hidden', tabindex: '-1', accept: '.gz,.tgz,application/gzip,application/x-gzip',
      onchange: (e) => { const f = e.target.files[0]; e.target.value = ''; if (f) uploadBackup(f); },
    });
    E.bkUp = h('button', { class: 'btn ghost', type: 'button', id: 'bk-upload', onclick: () => E.bkFile.click() }, 'Upload a backup');
    E.bkMsg = h('p', { class: 'bk-msg', role: 'status' });
    E.bkList = h('div', { class: 'bk-listing' });
    E.bkFoot = h('div', { class: 'bk-foot' });
    $('#main').replaceChildren(h('div', { class: 'bk' },
      h('header', { class: 'bk-head' },
        h('h1', null, 'Backups'),
        h('p', { class: 'bk-lead' }, "Copies of the channel setup: each channel's number, name, settings, details, logo and YouTube schedule, " +
          'the weather channel, and the Jellyfin account. Videos are never in them. Restoring puts channels back as they were.')),
      h('form', { class: 'bk-actions', onsubmit: backUpNow },
        h('label', { class: 'label', for: 'bk-label' }, 'Back up now, with a note'),
        h('div', { class: 'bk-row-in' }, E.bkLabel, E.bkNow, h('span', { class: 'bk-or' }, 'or'), E.bkUp, E.bkFile)),
      E.bkMsg, E.bkList, E.bkFoot));
    renderBackupList();
    loadBackups();
  }

  function setBkMsg(text, cls) {
    if (!E.bkMsg) return;
    E.bkMsg.textContent = text || '';
    E.bkMsg.className = 'bk-msg' + (cls ? ' ' + cls : '');
  }

  function renderBackupList() {
    if (!E.backups) return;
    const off = BK.err && BK.err.status === 503;
    E.bkNow.disabled = E.bkUp.disabled = E.bkLabel.disabled = !!BK.busy || off;
    E.bkNow.textContent = BK.busy === 'now' ? 'Backing up' : 'Back up now';
    E.bkUp.textContent = BK.busy === 'upload' ? 'Uploading' : 'Upload a backup';
    let body;
    if (BK.err && !BK.list) {
      body = h('div', { class: 'bk-empty' },
        h('p', { class: 'empty-line err' }, off ? 'Backups are off on this server.' : "Can't list the backups."),
        h('p', { class: 'help' }, BK.err.message));
    } else if (!BK.list) {
      body = h('p', { class: 'bk-empty help' }, 'Loading the backups');
    } else if (!BK.list.length) {
      body = h('div', { class: 'bk-empty' },
        h('p', { class: 'empty-line' }, 'No backups yet.'),
        h('p', { class: 'help' }, 'One is made 30 seconds after your next change, and daily. Or back up now.'));
    } else {
      const latest = BK.list.find((b) => !b.error);
      body = h('ol', { class: 'bk-rows', 'aria-label': 'Backups, newest first' }, BK.list.map((b) => bkRow(b, b === latest)));
    }
    const had = E.bkList.contains(document.activeElement) ? document.activeElement : null;
    const focusFile = had && had.closest('[data-file]') ? had.closest('[data-file]').dataset.file : null;
    const focusAct = had ? had.dataset.act : null;
    E.bkList.replaceChildren(body);
    if (focusFile && focusAct) {
      const el = E.bkList.querySelector(`[data-file="${CSS.escape(focusFile)}"] [data-act="${focusAct}"]`);
      if (el) el.focus();
    }
    renderBackupFoot();
  }

  function bkRow(b, latest) {
    const r = bkReason(b);
    const when = bkWhen(b.created);
    const what = `the backup from ${when}`;
    return h('li', { class: 'bk-item-row' + (latest ? ' latest' : '') + (b.error ? ' bad' : ''), 'data-file': b.file },
      h('div', { class: 'bk-when' }, h('b', { text: when }), h('span', { text: bkAgo(b.created) })),
      h('div', { class: 'bk-what' },
        h('div', { class: 'bk-tags' },
          h('span', { class: 'bk-tag r-' + b.reason, title: r.help, text: r.label }),
          latest ? h('span', { class: 'bk-tag latest' }, 'Latest') : null,
          b.older ? h('span', { class: 'bk-tag', title: 'Made by hand before Airwaves made its own backups; it restores the same' }, 'Older format') : null,
          b.label ? h('span', { class: 'bk-note', text: b.label }) : null),
        b.error
          ? h('p', { class: 'bk-sum err' }, `Can't be read: ${b.error}`)
          : h('p', { class: 'bk-sum' }, bkContents(b))),
      h('div', { class: 'bk-acts' },
        h('button', { class: 'btn small ghost', type: 'button', 'data-act': 'restore', disabled: !!b.error, 'aria-label': `Restore from ${what}`, onclick: (e) => openRestore(b, e.currentTarget) }, 'Restore'),
        h('a', { class: 'btn small ghost', href: `${API}/backups/${enc(b.file)}`, download: b.file, 'data-act': 'download', 'aria-label': `Download ${what}` }, 'Download'),
        h('button', { class: 'btn small ghost danger', type: 'button', 'data-act': 'delete', 'aria-label': `Delete ${what}`, onclick: () => deleteBackup(b) }, 'Delete')));
  }

  function renderBackupFoot() {
    const k = BK.keep || { recent: 30, days: 14, delaySeconds: 30 };
    const folder = BK.folder || '/data/backups';
    const host = location.hostname || 'the server';
    E.bkFoot.replaceChildren(
      h('p', null, `Automatic backups are made ${k.delaySeconds} seconds after a change, before every restore, and once a day when the setup changed some other way. ` +
        `The newest ${k.recent} are kept, and the newest of each of the last ${k.days} days; older ones are removed. Backups you make or upload stay until you delete them.`),
      h('p', null, 'They are kept in ', h('code', null, folder), " in the server's container (", h('code', null, '~/airwaves/data/backups'),
        ' on the host, as scripts/deploy.sh sets it up), readable only by the server. To keep copies elsewhere, download them here, or copy them all off:'),
      h('code', { class: 'path', title: 'Click to select' }, `rsync -a ${host}:airwaves/data/backups/ ./airwaves-backups/`));
  }

  async function backUpNow(e) {
    e.preventDefault();
    if (BK.busy) return;
    BK.busy = 'now';
    setBkMsg('');
    renderBackupList();
    try {
      const b = await api('POST', '/backups', { label: squash(E.bkLabel.value) });
      E.bkLabel.value = '';
      setBkMsg(`Backed up ${bkContents(b).replace(/\. [\d.]+ [KM]B$/, '')}.`, 'ok');
      toast('Backed up');
    } catch (err) {
      setBkMsg(`Couldn't back up: ${err.message}`, 'err');
    }
    BK.busy = '';
    renderBackupList();
    await loadBackups();
    const n = $('#bk-now');
    if (n) n.focus();
  }

  async function uploadBackup(file) {
    if (BK.busy) return;
    if (file.size > BK_MAX) { setBkMsg('A backup is under 16 MB; that file is too big to be one.', 'err'); return; }
    BK.busy = 'upload';
    setBkMsg(`Checking ${file.name}`);
    renderBackupList();
    let res = null;
    try {
      res = await api('POST', `/backups/upload?name=${enc(file.name)}`, file);
      const skipped = (res.skipped || []).length;
      setBkMsg((res.existing ? `${file.name} is here already.` : `Uploaded ${file.name}.`) +
        (skipped === 1 ? " 1 file in it that isn't part of a channel setup was left out." : '') +
        (skipped > 1 ? ` ${plural(skipped, 'file', 'files')} in it that aren't part of a channel setup were left out.` : ''), 'ok');
    } catch (err) {
      setBkMsg(`Couldn't take ${file.name}: ${err.message}`, 'err');
    }
    BK.busy = '';
    renderBackupList();
    await loadBackups();
    if (res && res.backup) openRestore(res.backup, $('#bk-upload'));
  }

  async function deleteBackup(b) {
    const ok = await confirmDialog({
      title: 'Delete this backup?',
      body: `The backup from ${bkWhen(b.created)} (${bkReason(b).label.toLowerCase()}${b.label ? ', ' + b.label : ''}) is removed from the server for good.`,
      ok: 'Delete backup', danger: true,
    });
    if (!ok) return;
    try {
      await api('DELETE', `/backups/${enc(b.file)}`);
      toast('Backup deleted');
    } catch (err) {
      toast(`Couldn't delete it: ${err.message}`, true);
    }
    await loadBackups();
    const first = $('.bk-rows [data-act="restore"]') || $('#bk-now');
    if (first) first.focus();
  }

  // ---------- Backups: restore ----------
  function bkDialog() {
    let dlg = $('#bk-dlg');
    if (dlg) return dlg;
    dlg = h('dialog', { id: 'bk-dlg', class: 'bk-dlg', 'aria-labelledby': 'bk-dlg-title' });
    // Escape closes it as Cancel does, but not while a restore runs.
    dlg.addEventListener('cancel', (e) => { e.preventDefault(); closeRestore(); });
    dlg.addEventListener('close', () => { if (BK.r) closeRestore(); });
    document.body.append(dlg);
    return dlg;
  }

  // closeRestore closes the dialog and puts focus back where it was opened from (the list may have
  // been drawn again since), there and then: a dialog's close event can come late.
  function closeRestore() {
    const dlg = $('#bk-dlg'), r = BK.r;
    if (r && r.busy) return;
    BK.r = null;
    if (dlg && dlg.open) dlg.close();
    if (!r) return;
    const again = $(`.bk-rows [data-file="${CSS.escape(r.b.file)}"] [data-act="restore"]`);
    const to = r.back && r.back.isConnected ? r.back : again || $('#bk-now');
    if (to) to.focus();
  }

  function openRestore(b, back) {
    const dlg = bkDialog();
    BK.r = { b, back, mode: 'all', chosen: null, account: false, schedules: true, plan: null, err: null, token: 0, busy: false, done: null };
    renderRestore();
    if (!dlg.open) dlg.showModal();
    const first = $('#bk-dlg input[name="bk-mode"]:checked') || $('#bk-dlg-cancel');
    if (first) first.focus();
    previewRestore();
  }

  function restoreBody(r) {
    const body = { schedules: r.schedules, account: r.account };
    if (r.mode === 'chosen') body.channels = [...(r.chosen || [])];
    return body;
  }

  let bkPreviewTimer = 0;
  function previewRestore(wait) {
    const r = BK.r;
    if (!r) return;
    clearTimeout(bkPreviewTimer);
    const token = ++r.token;
    bkPreviewTimer = setTimeout(async () => {
      try {
        const plan = await api('POST', `/backups/${enc(r.b.file)}/restore`, { ...restoreBody(r), preview: true });
        if (BK.r !== r || r.token !== token) return;
        r.plan = plan;
        r.err = null;
        if (!r.chosen) r.chosen = new Set(plan.items.filter((x) => x.action === 'add' || x.action === 'change').map((x) => x.key));
      } catch (e) {
        if (BK.r !== r || r.token !== token) return;
        r.err = e;
      }
      renderRestore();
    }, wait || 0);
  }

  const bkNum = (x) => (x.number ? `${x.number} ${x.name}` : x.name);

  // What restoring one channel does, in words.
  function bkItemWhat(x) {
    if (x.action === 'skip') return x.reason;
    if (x.error) return `Didn't work: ${x.error}`;
    const parts = (x.changes || []).filter((c) => BK_PARTS[c]).map((c) => BK_PARTS[c]);
    if (x.action === 'add') {
      if (x.empty) return 'Made again, empty: put its videos back in its folder.';
      return 'Brought back' + (parts.includes('schedule') ? ', with its schedule.' : '.');
    }
    if (x.action === 'same') return 'As in the backup; nothing changes.';
    const out = [];
    const now = x.now;
    if ((x.changes || []).includes('rename') && now) out.push(`Now ${now.number} ${now.name}: it gets its old number and name back.`);
    else if (x.kind === 'weather' && now && (now.number !== x.number || now.name !== x.name)) out.push(`Now ${now.number} ${now.name}: it goes back to ${x.number} ${x.name}.`);
    if ((x.changes || []).includes('kind') && now) out.push(`Now a ${CHANNEL_KIND[now.kind] || now.kind} channel: it turns back into a ${CHANNEL_KIND[x.kind] || x.kind} one.`);
    const rest = parts.filter((p) => p !== 'schedule');
    if (rest.length) out.push(`Its ${bkList(rest)} go back as they were.`);
    if (parts.includes('schedule')) out.push('Its schedule comes back.');
    return out.join(' ') || 'Changes go back as they were.';
  }

  function renderRestore() {
    const dlg = $('#bk-dlg'), r = BK.r;
    if (!dlg || !r) return;
    const b = r.b, plan = r.plan, done = r.done;
    const reason = bkReason(b);
    const items = plan ? plan.items : [];
    const chosen = (x) => (r.mode === 'all' ? true : r.chosen && r.chosen.has(x.key));

    const head = [
      h('h2', { id: 'bk-dlg-title' }, done ? 'Restored' : 'Restore channels'),
      h('p', { class: 'bk-from' }, `From ${bkWhen(b.created)}, ${reason.label.toLowerCase()}`, b.label ? h('b', { text: `: ${b.label}` }) : null, '.'),
    ];
    const kids = [...head];
    if (!done) {
      kids.push(h('div', { class: 'bk-mode' },
        radios('bk-mode', [{ v: 'all', label: 'Everything' }, { v: 'chosen', label: 'Chosen channels' }], r.mode, (e) => {
          r.mode = e.target.value;
          renderRestore();
          previewRestore();
        }, 'What to restore')));
    }
    let list;
    if (r.err && !plan) {
      list = h('p', { class: 'bk-sum err' }, r.err.message);
    } else if (!plan) {
      list = h('p', { class: 'bk-sum' }, 'Working out what a restore changes');
    } else {
      list = h('ul', { class: 'bk-items' + (r.mode === 'chosen' && !done ? ' choosing' : ''), 'aria-label': 'Channels in the backup' }, items.map((x) => {
        const id = 'bk-x-' + items.indexOf(x);
        const box = r.mode === 'chosen' && !done
          ? h('input', {
            type: 'checkbox', id, checked: chosen(x), 'aria-describedby': id + '-what',
            onchange: (e) => { if (e.target.checked) r.chosen.add(x.key); else r.chosen.delete(x.key); renderRestore(); previewRestore(150); },
          })
          : null;
        const state = done ? (x.error ? 'bad' : x.done ? 'did' : 'same') : (chosen(x) ? x.action : 'off');
        return h('li', { class: 'bk-item a-' + state },
          box || h('span', { class: 'bk-mark', 'aria-hidden': 'true' }),
          h('label', { class: 'bk-item-name', for: box ? id : null },
            h('span', { class: 'num', text: x.number || '' }),
            h('span', { class: 'nm', text: x.name }),
            h('span', { class: 'kind k-' + x.kind, text: CHANNEL_KIND[x.kind] || x.kind })),
          h('p', { class: 'what', id: id + '-what', text: !chosen(x) ? 'Left as it is: not chosen.' : bkItemWhat(x) }));
      }));
    }
    kids.push(list);

    if (plan && !done) {
      const a = plan.account || {};
      kids.push(h('div', { class: 'bk-opts' },
        h('label', { class: 'bk-check' + (a.inBackup ? '' : ' off') },
          h('input', {
            type: 'checkbox', id: 'bk-acct', checked: r.account && a.inBackup, disabled: !a.inBackup,
            onchange: (e) => { r.account = e.target.checked; previewRestore(); },
          }),
          h('span', null, h('b', null, 'The Jellyfin account'),
            h('small', null, !a.inBackup ? 'Not in this backup.'
              : a.same ? `${a.user} on ${hostOf(a.server)}, the same as now.`
                : `${a.user} on ${hostOf(a.server)}, in place of ${S.state && S.state.account.server ? `${S.state.account.user} on ${hostOf(S.state.account.server)}` : 'none'} now.`))),
        h('label', { class: 'bk-check' },
          h('input', { type: 'checkbox', id: 'bk-sched', checked: r.schedules, onchange: (e) => { r.schedules = e.target.checked; previewRestore(); } }),
          h('span', null, h('b', null, 'YouTube schedules'),
            h('small', null, 'For channels that have none now, so their guide picks up where the backup left off. A channel on the air keeps its own.')))));
    }

    kids.push(h('div', { class: 'bk-summary', role: 'status', 'aria-live': 'polite' }, plan ? restoreSummary(r) : null));

    const changes = plan ? items.filter((x) => chosen(x) && (x.action === 'add' || x.action === 'change')).length : 0;
    const acct = plan && r.account && plan.account && plan.account.inBackup && !plan.account.same;
    let okText = 'Restore';
    if (changes) okText = `Restore ${plural(changes, 'channel', 'channels')}`;
    else if (acct) okText = 'Restore the account';
    const actions = done
      ? [h('button', { class: 'btn', type: 'button', id: 'bk-dlg-cancel', onclick: closeRestore }, 'Close')]
      : [h('button', { class: 'btn ghost', type: 'button', id: 'bk-dlg-cancel', disabled: r.busy, onclick: closeRestore }, 'Cancel'),
        h('button', {
          class: 'btn', type: 'button', id: 'bk-dlg-ok', disabled: r.busy || !plan || (!changes && !acct),
          onclick: doRestore,
        }, r.busy ? 'Restoring' : okText)];
    kids.push(h('div', { class: 'dlg-actions' }, actions));

    const had = dlg.contains(document.activeElement) ? document.activeElement.id || (document.activeElement.name ? `[name="${document.activeElement.name}"]:checked` : '') : '';
    dlg.replaceChildren(h('form', { method: 'dialog' }, kids));
    if (had) {
      const el = had.startsWith('[') ? dlg.querySelector('input' + had) : dlg.querySelector('#' + CSS.escape(had));
      if (el) el.focus();
    }
  }

  // The summary under the list: what a restore will do, all told.
  function restoreSummary(r) {
    const plan = r.plan, done = r.done;
    const items = plan.items;
    const chosen = (x) => (r.mode === 'all' ? true : r.chosen && r.chosen.has(x.key));
    const lines = [];
    if (done) {
      const did = items.filter((x) => x.done), bad = items.filter((x) => x.error);
      if (did.length) lines.push(`Put back ${plural(did.length, 'channel', 'channels')}: ${bkList(did.map(bkNum))}.`);
      if (plan.account && plan.account.done) lines.push(`The Jellyfin account is back: ${plan.account.user} on ${hostOf(plan.account.server)}.`);
      if (bad.length) lines.push(h('span', { class: 'err' }, `${plural(bad.length, 'channel', 'channels')} didn't work: ${bkList(bad.map(bkNum))}.`));
      if (plan.account && plan.account.error) lines.push(h('span', { class: 'err' }, `The account didn't work: ${plan.account.error}`));
      if (plan.before) lines.push(`The setup as it was is backed up (${plan.before}); restore that to undo this.`);
      return h('ul', null, lines.map((l) => h('li', null, l)));
    }
    const pick = (act) => items.filter((x) => chosen(x) && x.action === act);
    const adds = pick('add'), changes = pick('change'), same = pick('same'), skips = pick('skip');
    if (adds.length) {
      const empty = adds.filter((x) => x.empty);
      lines.push(`Brings back ${plural(adds.length, 'channel', 'channels')}: ${bkList(adds.map(bkNum))}.` +
        (empty.length ? ` ${bkList(empty.map(bkNum))} ${empty.length === 1 ? 'comes' : 'come'} back empty, as videos aren't in backups.` : ''));
    }
    if (changes.length) {
      const moved = changes.filter((x) => x.now && x.now.number !== x.number);
      lines.push(`Puts ${plural(changes.length, 'channel', 'channels')} back as ${changes.length === 1 ? 'it was' : 'they were'}: ${bkList(changes.map(bkNum))}.` +
        (moved.length ? ` ${bkList(moved.map((x) => `${x.now.number} ${x.now.name}`))} ${moved.length === 1 ? 'moves' : 'move'} back to ${moved.length === 1 ? 'its' : 'their'} old number.` : ''));
    }
    if (same.length) lines.push(`${plural(same.length, 'channel is', 'channels are')} already as in the backup.`);
    if (skips.length) lines.push(h('span', { class: 'err' }, `Leaves out ${bkList(skips.map(bkNum))}: ${skips.length === 1 ? 'its number is' : 'their numbers are'} taken now.`));
    const a = plan.account || {};
    if (a.inBackup && r.account && !a.same) lines.push(`Signs Jellyfin in as ${a.user} on ${hostOf(a.server)}.`);
    else if (a.inBackup && !a.same) lines.push('Keeps the Jellyfin account as it is now.');
    if ((plan.others || []).length) {
      lines.push(`Leaves alone ${plural(plan.others.length, 'channel', 'channels')} the backup doesn't have: ${bkList(plan.others.map((c) => `${c.number} ${c.name}`))}.`);
    }
    if (!adds.length && !changes.length && !(a.inBackup && r.account && !a.same)) lines.push('Nothing to restore: what is chosen is already as in the backup.');
    else lines.push('First, the setup as it is now is backed up, so this can be undone. Videos are never touched.');
    return h('ul', null, lines.map((l) => h('li', null, l)));
  }

  async function doRestore() {
    const r = BK.r;
    if (!r || r.busy || !r.plan) return;
    r.busy = true;
    renderRestore();
    let plan = null;
    try {
      plan = await api('POST', `/backups/${enc(r.b.file)}/restore`, restoreBody(r));
    } catch (e) {
      r.busy = false;
      if (BK.r !== r) return;
      r.err = e;
      renderRestore();
      toast(`Couldn't restore: ${e.message}`, true);
      return;
    }
    r.busy = false;
    if (BK.r !== r) return;
    r.plan = plan;
    r.done = true;
    renderRestore();
    const did = plan.items.filter((x) => x.done).length;
    toast(plan.errors ? `Restored with ${plural(plan.errors, 'problem', 'problems')}` : `Restored ${plural(did, 'channel', 'channels')}`, !!plan.errors);
    const c = $('#bk-dlg-cancel');
    if (c) c.focus();
    loadBackups();
    refreshState();
  }

  loadBackups();

  // ---------- Channel changes ----------
  // How long tuning in took lately, by kind of channel: the server's time to a ready stream,
  // and the app's from the key press to the picture, as the apps report it.
  const TUNE_KINDS = { antenna: 'Antenna', folder: 'Folders', jellyfin: 'Jellyfin', youtube: 'YouTube', weather: 'Weather' };
  async function loadTunes() {
    let rows = null;
    try {
      const r = await api('GET', '/tunes');
      rows = Array.isArray(r && r.tunes) ? r.tunes : [];
    } catch { /* kept as it was */ }
    if (rows) renderTunes(rows);
    setTimeout(loadTunes, document.hidden ? 300000 : 60000);
  }
  function renderTunes(rows) {
    const box = $('#tune-side');
    if (!box) return;
    const by = {};
    for (const r of rows) (by[r.kind] = by[r.kind] || {})[r.measure] = r;
    const kinds = Object.keys(TUNE_KINDS).filter((k) => by[k]);
    box.hidden = !kinds.length;
    const cell = (r) => h('td', { title: r ? `${r.count} lately, the slowest ${r.max.toFixed(1)} s` : null }, r ? `${r.p50.toFixed(1)} / ${r.p90.toFixed(1)}` : '-');
    box.replaceChildren(
      h('div', { class: 'tune-head' }, h('b', null, 'Channel changes'), h('span', { text: 'median / 90%, s' })),
      h('table', { class: 'tune-table' },
        h('thead', null, h('tr', null, h('td', null), h('th', { scope: 'col' }, 'Server'), h('th', { scope: 'col' }, 'App'))),
        h('tbody', null, kinds.map((k) => h('tr', null, h('th', { scope: 'row' }, TUNE_KINDS[k]), cell(by[k].ready), cell(by[k]['first frame']))))));
  }
  loadTunes();

  // ---------- Global keys ----------
  // The skip link would otherwise change the hash, which the router reads as a channel.
  $('.skip').addEventListener('click', (e) => {
    e.preventDefault();
    const t = $('#search') || $('#main');
    t.focus();
  });

  document.addEventListener('keydown', (e) => {
    if ($('#dlg').open) return;
    if (e.key === '/' && !e.metaKey && !e.ctrlKey && !e.altKey && !isTyping(e.target) && E.search) {
      e.preventDefault();
      E.search.focus();
      E.search.select();
    } else if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 's' && E.save) {
      e.preventDefault();
      save();
    }
  });

  boot();
})();
