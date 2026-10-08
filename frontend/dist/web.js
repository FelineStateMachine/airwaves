'use strict';

// The web app. airwavesd serves this interface at /tv/ to browsers and to
// the Android TV app, where window.go.main.App (the desktop app's Go side,
// app.go) is this: the same calls, made with fetch to the server that
// served the page, with the same answers and errors (a rejected promise
// with the message, as Wails gives Go's). The desktop app's settings file
// is localStorage here, one per server. Inside the desktop app Wails has
// set window.go already, and this does nothing.
//
// The Android TV app is a WebView whose user agent ends in
// "AirwavesTV/<version>". It may offer window.AirwavesAndroid: getServer(),
// setServer(url) (it saves the address and loads <url>/tv/), copyText(s),
// openUrl(u), log(level, msg) and exit(). Its Back key calls
// window.airwavesBack() (app.js).
(() => {
  if (window.go) return;

  const tvApp = (navigator.userAgent.match(/\bAirwavesTV\/(\S+)/) || [])[1] || '';
  const shell = window.AirwavesAndroid || null;
  // The server: this page's, less /tv/ (which a proxy may put under a path).
  const base = location.origin + location.pathname.replace(/\/tv(\/.*)?$/, '');
  const settingsKey = 'airwaves.settings';
  const DEFAULT_PORT = 8089;

  // serverURL is an address as the desktop app reads it (api.NewClient): a
  // bare host ("nas") gets http:// and the default port.
  function serverURL(s) {
    let u = String(s || '').trim().replace(/\/+$/, '');
    if (!u || u.includes('://')) return u;
    u = `http://${u}`;
    try {
      const p = new URL(u);
      if (!p.port) {
        p.port = String(DEFAULT_PORT);
        u = p.origin + p.pathname.replace(/\/+$/, '');
      }
    } catch { /* used as typed */ }
    return u;
  }

  // The address shown for this server: the one the Android TV app was
  // given, else the page's.
  const serverName = () => {
    try {
      return (shell && shell.getServer && serverURL(shell.getServer())) || base;
    } catch {
      return base;
    }
  };

  // ---------- settings (internal/store/settings.go) ----------
  const hex = (n) => [...crypto.getRandomValues(new Uint8Array(n))].map((b) => b.toString(16).padStart(2, '0')).join('');
  let memory = null; // when localStorage can't be used

  function normalized(s) {
    const out = { server: '', token: '', clientId: '', lastChannel: '', captions: false, ...s };
    // Settings of reception estimates, from before the measured lineup.
    delete out.antenna;
    delete out.showAll;
    if (out.scale && (out.scale < 50 || out.scale > 300)) out.scale = 0;
    // Its own client ID, so the server keeps this screen's stream apart.
    if (!out.clientId) out.clientId = `${tvApp ? 'tv' : 'web'}-${hex(6)}`;
    out.server = serverName();
    return out;
  }

  function load() {
    let s = memory;
    try {
      s = JSON.parse(localStorage.getItem(settingsKey) || 'null') || memory;
    } catch { /* unreadable: defaults */ }
    return normalized(s || {});
  }

  function save(s) {
    const out = normalized(s);
    memory = out;
    try {
      localStorage.setItem(settingsKey, JSON.stringify(out));
    } catch { /* kept in memory for this page */ }
    return out;
  }
  save(load()); // a stable client ID from the first call

  // ---------- the API ----------
  // call is one API request, with the token if one is set. Errors are
  // their messages, worded like the desktop app's client's.
  async function call(method, path, body, { token = load().token, ms = 120_000 } = {}) {
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (token) headers.Authorization = `Bearer ${token}`;
    const abort = new AbortController();
    const timer = setTimeout(() => abort.abort(), ms);
    let resp;
    try {
      resp = await fetch(base + path, {
        method, headers, cache: 'no-store', signal: abort.signal,
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (!resp.ok) {
        let msg = '';
        try { msg = (await resp.json()).error; } catch { /* not JSON */ }
        throw new Error(msg || `airwaves server: HTTP ${resp.status}`);
      }
      return await resp.json();
    } catch (e) {
      // Wails rejects with Go's error text, not an Error.
      if (e && e.name === 'AbortError') throw 'airwaves server: timed out';
      if (e instanceof TypeError) throw `airwaves server: ${e.message}`;
      throw String(e && e.message ? e.message : e);
    } finally {
      clearTimeout(timer);
    }
  }

  // reachable tells whether a server answers, though another origin's
  // answers can't be read here.
  async function reachable(url) {
    const abort = new AbortController();
    const timer = setTimeout(() => abort.abort(), 8000);
    try {
      await fetch(`${url}/healthz`, { mode: 'no-cors', cache: 'no-store', signal: abort.signal });
      return '';
    } catch (e) {
      return e && e.name === 'AbortError' ? 'airwaves server: timed out' : `airwaves server: can't connect to ${url}`;
    } finally {
      clearTimeout(timer);
    }
  }

  const query = new URLSearchParams(location.search);

  // fit is the zoom that fits a 1080p screen's interface into this one:
  // the Android TV app's WebView is often 960x540 (the TV's 1080p at twice
  // the density), where 125% is what 125% is on a 1080p TV. It goes by the
  // width, which an on-screen keyboard leaves alone. Elsewhere 1.
  const fit = () => (tvApp && innerWidth > 0 ? innerWidth / 1920 : 1);
  let lastBoot = null;
  // Before app.js's resize handler, which reads it.
  addEventListener('resize', () => { if (lastBoot) lastBoot.fit = fit(); });

  async function boot() {
    const out = {
      settings: load(), info: { name: '', mode: '', version: '', tuner: {}, dvr: false, antenna: false }, config: {},
      serverUrl: base,
      view: query.get('view') || '',
      // Lite on the Android TV app, a TV box like the Linux ones;
      // ?lite=1 or 0 turns it on or off anywhere, as AIRWAVES_LITE does.
      lite: query.get('lite') || (tvApp ? '1' : ''),
      // Auto is 125% on a TV across the room, as under gamescope, else 100%.
      autoScale: tvApp ? 125 : 100,
      fit: fit(),
      nativeZoom: false,
      version: tvApp,
      // In a browser the server is the page's own; only the Android TV app
      // can switch to another.
      fixedServer: !(shell && shell.setServer),
    };
    lastBoot = out;
    try {
      const [info, config] = await Promise.all([
        call('GET', '/api/info', undefined, { ms: 10_000 }),
        call('GET', '/api/config', undefined, { ms: 10_000 }),
      ]);
      Object.assign(out, { info, config });
      out.version = out.version || info.version;
    } catch (e) {
      out.error = String(e);
    }
    return out;
  }

  const listeners = {};
  const emit = (name, ...args) => (listeners[name] || []).forEach((cb) => cb(...args));
  const playback = (pb) => ({ ...pb, url: base + pb.path });
  const enc = encodeURIComponent;

  const App = {
    Boot: boot,

    // SaveSettings keeps settings and returns a fresh Boot. A new server
    // address goes to the Android TV app, which loads that server's page
    // once it answers; this page is then on its way out.
    async SaveSettings(s) {
      const want = serverURL(s && s.server);
      if (shell && shell.setServer && want && want !== serverURL(serverName())) {
        const err = await reachable(want);
        if (err) return { ...(await boot()), error: err };
        shell.setServer(want);
        return new Promise(() => {});
      }
      save(s);
      return boot();
    },
    SetZoom() {}, // CSS zoom on the web (nativeZoom is false)
    SetConfig: (c) => call('PUT', '/api/config', c),

    // TestServer checks an address without switching to it. Another
    // server's info can't be read from this page, so it's named by host.
    async TestServer(server, token) {
      const url = serverURL(server);
      if (!url || url === base || url === serverURL(serverName())) return call('GET', '/api/info', undefined, { token, ms: 8000 });
      const err = await reachable(url);
      if (err) throw err;
      return { name: new URL(url).host };
    },
    Scan(refresh) {
      emit('progress', `Loading lineup and listings from ${serverName()}`);
      return call('GET', `/api/snapshot${refresh ? '?refresh=1' : ''}`);
    },
    // What the tuners measured, and Measure now.
    Signal: () => call('GET', '/api/signal', undefined, { ms: 15_000 }),
    Measure: () => call('POST', '/api/signal/measure', {}, { ms: 15_000 }),
    ScanChannels: () => call('POST', '/api/tuner/scan', {}, { ms: 15_000 }),
    Weather: () => call('GET', '/api/weather'),

    async CopyText(text) {
      text = String(text);
      if (shell && shell.copyText) return void shell.copyText(text);
      if (navigator.clipboard && window.isSecureContext) {
        try {
          return await navigator.clipboard.writeText(text);
        } catch (e) {
          throw String(e && e.message ? e.message : e);
        }
      }
      // Plain HTTP has no clipboard API; a selection still copies.
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
      document.body.append(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch { /* not allowed */ }
      ta.remove();
      if (!ok) throw 'copying is not allowed here';
      return undefined;
    },

    async Tune(number) {
      const pb = await call('POST', '/api/tune', { client: load().clientId, number });
      save({ ...load(), lastChannel: number });
      return playback(pb);
    },
    async StopTV() {
      try { await call('POST', '/api/stop', { client: load().clientId }); } catch { /* as the desktop app: ignored */ }
    },
    // How long a channel change took, for the server's admin page.
    ReportTune: (m) => call('POST', '/api/metrics', m).then(() => undefined),
    DVR: () => call('GET', '/api/dvr'),
    Record: (req) => call('POST', '/api/dvr/record', req),
    DeleteRule: (id) => call('DELETE', `/api/dvr/rules/${enc(id)}`).then(() => undefined),
    DeleteRecording: (id) => call('DELETE', `/api/dvr/recordings/${enc(id)}`).then(() => undefined),
    StopRecording: (id) => call('POST', `/api/dvr/recordings/${enc(id)}/stop`, {}).then(() => undefined),
    ExtendRecording: (id, minutes) => call('POST', `/api/dvr/recordings/${enc(id)}/extend`, { minutes }).then((r) => r.until),
    async PlayRecording(id, from) {
      if (!id) throw 'no recording selected';
      return playback(await call('POST', '/api/dvr/play', { client: load().clientId, id, from: from || 0 }));
    },
    SaveProgress: (id, position, duration) => call('POST', '/api/dvr/progress', { id, position, duration }).then(() => undefined),
    MarkWatched: (id, watched) => call('POST', '/api/dvr/progress', { id, position: 0, duration: 0, watched: !!watched }).then(() => undefined),
    UpdateRule: (id, u) => call('PUT', `/api/dvr/rules/${enc(id)}`, u),
    SetDVRPrefs: (p) => call('PUT', '/api/dvr/prefs', p),

    async OpenURL(u) {
      if (shell && shell.openUrl) shell.openUrl(String(u));
      else window.open(u, '_blank', 'noopener');
    },
    async Log(level, msg) {
      if (shell && shell.log) return void shell.log(String(level), String(msg));
      (level === 'warn' || level === 'error' ? console.warn : console.info)(`ui ${level}: ${msg}`);
      return undefined;
    },
  };
  window.go = { main: { App } };

  // No window to drag here.
  const titlebar = document.getElementById('titlebar');
  if (titlebar) titlebar.hidden = true;

  // The Wails runtime calls app.js makes: events, and full screen.
  window.runtime = {
    EventsOn(name, cb) {
      (listeners[name] = listeners[name] || []).push(cb);
      return () => { listeners[name] = (listeners[name] || []).filter((x) => x !== cb); };
    },
    EventsOff(name) { delete listeners[name]; },
    WindowIsFullscreen: async () => !!document.fullscreenElement,
    WindowFullscreen() {
      const el = document.documentElement;
      if (el.requestFullscreen) el.requestFullscreen().catch(() => {});
    },
    WindowUnfullscreen() {
      if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    },
  };

  // The desktop app stops its stream when it quits; a page, when it goes.
  // A page kept for Back and Forward starts over when shown again.
  addEventListener('pagehide', () => {
    const s = load();
    const headers = { 'Content-Type': 'application/json' };
    if (s.token) headers.Authorization = `Bearer ${s.token}`;
    try {
      fetch(`${base}/api/stop`, { method: 'POST', keepalive: true, headers, body: JSON.stringify({ client: s.clientId }) }).catch(() => {});
    } catch { /* leaving anyway */ }
  });
  addEventListener('pageshow', (e) => { if (e.persisted) location.reload(); });
})();
