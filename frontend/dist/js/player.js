'use strict';

// Tuning and playing a channel, the banner and the on-screen controls.

// ---------- tuning & playback ----------
// at is when the viewer asked for the channel (a key press), for timing
// the change.
async function tune(ch, { quiet = false, at = performance.now() } = {}) {
  if (!ch) return;
  if (state.recording) saveRecordingProgress();
  if (state.current && state.current.key !== ch.key) state.previous = state.current;
  state.current = ch;
  state.note = '';
  state.recording = null;
  state.staleStream = false;
  const token = ++state.tuneToken;
  if (!quiet) showBanner(); else renderBanner();
  stage.classList.add('tuning');
  $('#nosignal').hidden = true;
  showWX(!!ch.weather);
  if (ch.weather) {
    resetPlayer();
    api().StopTV();
    setTimeout(() => stage.classList.remove('tuning'), 400);
    return renderBanner();
  }
  // The server ends this screen's stream as it starts the next: the
  // picture plays out what it has, without asking for more of it.
  if (state.hls) state.hls.stopLoad();
  let played = false;
  try {
    const pb = await api().Tune(ch.number);
    if (token !== state.tuneToken) return;
    state.note = pb.note || '';
    await play(pb, token);
    played = token === state.tuneToken;
    if (played) reportTune(ch, performance.now() - at);
  } catch (e) {
    if (token !== state.tuneToken) return;
    log('warn', `tune ${ch.number}: ${e}`);
    showNoSignal(ch, String(e && e.message ? e.message : e));
    // The server read the tuner while it tried.
    if (!ch.own) loadSignal();
  } finally {
    // The static clears as the first frame shows.
    if (played) onFirstFrame(() => { if (token === state.tuneToken) stage.classList.remove('tuning'); });
    else if (token === state.tuneToken) setTimeout(() => stage.classList.remove('tuning'), 200);
  }
  renderBanner();
}

// onFirstFrame calls fn once the video's first frame is on screen, where
// the browser can tell (requestVideoFrameCallback), else a moment after
// playback starts; within a second either way.
function onFirstFrame(fn) {
  let done = false;
  const once = () => { if (!done) { done = true; fn(); } };
  if (video.requestVideoFrameCallback) video.requestVideoFrameCallback(once);
  setTimeout(once, video.requestVideoFrameCallback ? 1000 : 200);
}

// reportTune tells the server how long a channel change took, from the
// key to the picture playing, for the times on its admin page.
function reportTune(ch, ms) {
  if (!api().ReportTune) return;
  api().ReportTune({ number: ch.number, kind: ch.own ? ch.kind : 'antenna', firstFrameMs: Math.round(ms) }).catch(() => {});
}

function resetPlayer() {
  state.liveBaseline = null;
  if (state.hls) { state.hls.destroy(); state.hls = null; }
  video.removeAttribute('src');
  video.load();
}

async function play(pb, token) {
  resetPlayer();
  // hls.js first, so playback and captions work the same everywhere: it
  // hands the app a stream's captions ahead of time, where macOS's own HLS
  // gives them only as they show. Native HLS when there's no hls.js.
  const tries = [];
  if (window.Hls && window.Hls.isSupported()) tries.push(['hls.js', pb.url], ['hls.js', pb.path]);
  if (video.canPlayType('application/vnd.apple.mpegurl')) tries.push(['native', pb.url]);
  let last = 'no HLS support in this webview';
  for (const [how, src] of tries) {
    if (token !== state.tuneToken) return;
    try {
      await attempt(how, src);
      log('info', `playing ${state.recording ? 'recording ' + state.recording.title : state.current.number} via ${how}`);
      return;
    } catch (e) {
      last = `${how}: ${e.message || e}`;
      log('warn', `${how} ${src}: ${last}`);
      resetPlayer();
    }
  }
  throw new Error(`Could not play the stream (${last})`);
}

function attempt(how, src) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const done = (err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      video.removeEventListener('playing', onPlaying);
      video.removeEventListener('error', onError);
      if (err) reject(err); else resolve();
    };
    const onPlaying = () => done();
    const onError = () => done(new Error((video.error && video.error.message) || `media error ${video.error && video.error.code}`));
    const timer = setTimeout(() => done(new Error('timed out')), 15000);
    video.addEventListener('playing', onPlaying);
    video.addEventListener('error', onError);
    if (how === 'native') {
      video.src = src;
    } else {
      // hls.js keeps all it has played, up to WebKit's limit, for rewinding;
      // lite mode keeps 90 s, and loads older video again from the server.
      const h = new window.Hls({ liveSyncDurationCount: 2, maxBufferLength: 12, subtitleDisplay: false, ...(lite ? { backBufferLength: 90 } : {}) });
      state.hls = h;
      h.on(window.Hls.Events.ERROR, (_, d) => { if (d.fatal) done(new Error(d.details)); });
      h.loadSource(src);
      h.attachMedia(video);
    }
    video.play().catch((err) => {
      if (err && err.name === 'NotAllowedError') {
        video.muted = true;
        video.play().catch(() => {});
        // A touch screen's next touch, which lets sound play, turns it on.
        if (platform === 'touch') {
          document.addEventListener('pointerdown', () => { if (!state.userMuted) video.muted = false; }, { once: true, capture: true });
          return toast('Started muted. Tap for sound.', 3500);
        }
        const m = keyHint('m', 'M');
        if (m) return toast(`Started muted. Press ${m} for sound.`, 3500, true);
        // A remote has no M: its next key, which lets sound play, turns it on.
        document.addEventListener('keydown', () => { if (!state.userMuted) video.muted = false; }, { once: true, capture: true });
        toast('Started muted. Press any key for sound.', 3500);
      }
    });
  });
}

function showNoSignal(ch, msg) {
  resetPlayer();
  const ns = $('#nosignal');
  f(ns, 'num').textContent = ch.number;
  f(ns, 'call').textContent = `${displayCall(ch)} ${ch.network ? '| ' + ch.network : ''}`;
  f(ns, 'msg').textContent = msg;
  ns.hidden = false;
}

function step(dir) {
  const list = state.lineup;
  if (!list.length) return;
  let i = list.findIndex((c) => c.key === (state.current && state.current.key));
  i = (i + dir + list.length) % list.length;
  tune(list[i]);
}

// ---------- banner ----------
function chipsFor(p, ch) {
  const out = [];
  for (const fl of (p && p.flags) || []) out.push(`<span class="chip ${fl === 'Live' ? 'live' : fl === 'New' ? 'new' : ''}">${esc(fl)}</span>`);
  if (p && p.stars) out.push(`<span class="chip" title="Gracenote's rating, out of 4">${stars(p.stars)}</span>`);
  if (p && p.rating) out.push(`<span class="chip">${esc(p.rating)}</span>`);
  if (p && p.runtime) out.push(`<span class="chip">${p.runtime >= 60 ? `${Math.floor(p.runtime / 60)} h ${p.runtime % 60 ? `${p.runtime % 60} min` : ''}` : `${p.runtime} min`}</span>`);
  for (const t of (p && p.tags) || []) out.push(`<span class="chip">${esc(t)}</span>`);
  return out.join('');
}

// imageURL is a picture's address: the server's own ("/guide-art/...")
// relative to the server, others as they are.
const imageURL = (u) => (u && u.startsWith('/') ? state.boot.serverUrl + u : u || '');

// stars draws a rating out of 4 in stars, halves included.
const stars = (n) => '\u2605'.repeat(Math.floor(n)) + (n % 1 >= 0.5 ? '\u00bd' : '');

function episodeLine(p) {
  if (!p) return '';
  const se = p.season && p.episode ? `S${p.season} E${p.episode}` : '';
  // A repeat says when it first aired, which says the year too.
  const first = p.originalAirDate && !(p.flags || []).includes('New') ? `first aired ${firstAired(p.originalAirDate)}` : '';
  const game = p.awayTeam && p.homeTeam && !p.episodeTitle ? `${p.awayTeam} at ${p.homeTeam}` : '';
  return [p.episodeTitle && `"${p.episodeTitle}"`, game, se, !first && p.year, first].filter(Boolean).join('   ');
}

// firstAired names an original air date ("2019-03-05") as "Mar 5, 2019".
function firstAired(d) {
  const [y, m, day] = d.split('-').map(Number);
  return new Date(y, m - 1, day).toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

// castLine names a program's cast: "With Missy Peregrym, Zeeko Zaki".
const castLine = (p, n = 5) => (p && p.cast && p.cast.length ? `With ${p.cast.slice(0, n).map((c) => c.replace(/ \(.*\)$/, '')).join(', ')}` : '');

function renderBanner() {
  updateMediaSession();
  showTimeshift();
  renderOSD();
  const ch = state.current;
  const b = $('#banner');
  if (state.recording) return (state.recording.kind === 'episode' ? renderEpisodeBanner : renderRecordingBanner)(b, state.recording);
  if (!ch) return;
  if (ch.weather) return renderWXBanner(b, ch);
  const now = Date.now();
  const p = airingAt(ch, now);
  const nx = p ? nextAfter(ch, p._e - 1) : nextAfter(ch, now);
  f(b, 'num').textContent = ch.number;
  f(b, 'num').classList.toggle('long', ch.number.length > 4);
  f(b, 'call').textContent = displayCall(ch);
  f(b, 'net').textContent = ch.network || '';
  showChannelLogo(b, ch);
  f(b, 'time').textContent = p ? `${clock(p._s)} to ${clock(p._e)}` : '';
  // What's on is being recorded, or will be.
  const rec = p && state.dvrKeys.get(airingKey(ch, p));
  f(b, 'chips').innerHTML = (rec ? `<span class="chip live">${rec.status === 'recording' ? 'Recording' : 'Will record'}</span>` : '') + chipsFor(p, ch);
  f(b, 'title').textContent = p ? p.title : 'No listings';
  f(b, 'ep').textContent = episodeLine(p);
  f(b, 'cast').textContent = castLine(p, 3);
  f(b, 'progress').style.width = p ? `${Math.min(100, ((now - p._s) / (p._e - p._s)) * 100)}%` : '0';
  f(b, 'desc').textContent = p ? p.description || '' : ch.description || '';
  f(b, 'next').textContent = nx ? `${clock(nx._s)}  ${nx.title}` : '';

  f(b, 'note').textContent = state.note || '';

  // The server's own channels have no reception to show. The meter is
  // rebuilt on the next antenna channel.
  if (ch.custom) {
    b.classList.remove('warn');
    f(b, 'meter').hidden = true;
    f(b, 'tier').textContent = ch.category && ch.category !== 'Other' ? ch.category : '';
    f(b, 'tier').style.color = '';
    f(b, 'tx').textContent = OWN_SOURCE[ch.kind] || OWN_SOURCE.folder;
    f(b, 'atsc3').textContent = '';
    return;
  }

  // The signal is shown when asked for (I twice, or the controls' I) or
  // when what was measured explains a bad picture.
  const sig = sigOf(sigFor(ch));
  b.classList.toggle('warn', trouble(sig));
  f(b, 'meter').outerHTML = meter(sig, true).replace('class="meter big', 'data-f="meter" class="meter big');
  f(b, 'meter').hidden = false;
  f(b, 'tier').textContent = sig.label;
  f(b, 'tier').style.color = sig.state === 'none' ? '' : sig.color;
  const st = stationFor(ch);
  const mux = state.byRF.get(ch.rf);
  const where = ch.rf ? `RF ${ch.rf} ${(mux && mux.band) || (st && st.band) || ''}`.trim() : '';
  const lines = [];
  if (st) {
    lines.push(`${ch.via ? `On ${esc(st.callSign)}'s transmitter, ` : `${esc(st.callSign)} `}${where}`);
    lines.push(`${st.distanceKm.toFixed(0)} km ${compass(st.bearingDeg)}, ${st.erpKw >= 10 ? st.erpKw.toFixed(0) : st.erpKw.toFixed(1)} kW`);
  } else if (where) {
    lines.push(`${where}, no licensed transmitter on record`);
  }
  lines.push(esc(sigDetail(sig)));
  f(b, 'tx').innerHTML = lines.join('<br>');
  f(b, 'atsc3').textContent = ch.atsc3 ? `Also in ATSC 3.0 on ${ch.atsc3.hostCall} RF ${ch.atsc3.rf}` : '';
}

// What the server's own channels play, for the banner's details.
const OWN_SOURCE = {
  folder: 'Videos from your server, on a loop',
  jellyfin: 'From your Jellyfin library, on a schedule',
  youtube: 'YouTube uploads, streamed as they air',
};

// showChannelLogo puts an own channel's logo under its number in the
// banner; stations' logos are for the guide.
function showChannelLogo(b, ch) {
  const el = f(b, 'logo');
  if (!el) return;
  const logo = ch && ch.own && !state.recording ? ch.logo : '';
  el.hidden = !logo;
  el.style.backgroundImage = logo ? `url('${logo}')` : '';
}

// The banner shows briefly on a channel change and on request. Pressing I
// cycles: banner, banner with the measured signal, hidden. With the
// on-screen controls up it stays as long as they do.
function showBanner(detail = false) {
  renderBanner();
  const b = $('#banner');
  b.classList.toggle('detail', detail);
  b.classList.add('show');
  if (osd.open) return osdTouch();
  return hideBannerSoon(detail ? 12000 : 4000);
}

// hideBanner puts the banner away, and the controls with it.
function hideBanner() {
  clearTimeout(state.bannerTimer);
  closeOSD();
  $('#banner').classList.remove('show', 'detail');
}

function hideBannerSoon(ms = 4000) {
  clearTimeout(state.bannerTimer);
  if (osd.open) return osdTouch();
  state.bannerTimer = setTimeout(hideBanner, ms);
  return undefined;
}

// cycleBanner is the info key. It also flashes where the picture is, in
// the playback badge. With the controls up it shows or hides reception.
function cycleBanner() {
  const b = $('#banner');
  if (osd.open) {
    if (!state.recording) b.classList.toggle('detail');
    return flashPlace();
  }
  if (b.classList.contains('show') && (b.classList.contains('detail') || state.recording)) return hideBanner();
  flashPlace();
  return showBanner(b.classList.contains('show'));
}

// ---------- on-screen controls ----------
// OK on live TV brings up the controls over the picture, as video players
// on a TV do (Media3's PlayerControlView, Leanback, Kodi's OSD, the Live
// TV app's menu): the channel banner, a timeline of the program and of what
// can be rewound, a row of labeled buttons, and the dock above as the way
// to every view. The focus starts on Guide, so OK twice opens the guide;
// Left and Right on live TV skip and bring them up with the timeline
// picked. Up and Down move between the rows, Left and Right along one, OK
// presses. They go after OSD_MS without a key, but stay while paused (as
// Media3's and Leanback's do); Back puts them away.
const OSD_MS = 6000;
const osd = { open: false, timer: 0, tick: 0, last: 'guide', lastButton: 'guide', hover: false };

// The buttons, in order: [action, label]. Labels change with the state.
const OSD_BUTTONS = [
  ['guide', 'Guide'], ['play', 'Pause'], ['back', '-10 s'], ['ahead', '+30 s'], ['live', 'Live'],
  ['captions', 'Captions'], ['audio', 'Audio'], ['record', 'Record'], ['favorite', 'Favorite'], ['last', 'Last channel'],
];
const OSD_KEYS = { guide: 'G', play: 'Space', back: 'Left', ahead: 'Right', live: 'End', captions: 'C', audio: 'V', record: 'R', favorite: 'Shift F', last: 'L' };
const ICON_TEXT = 'text-anchor="middle" font-family="Plex Mono, Menlo, monospace" font-weight="700"';
const OSD_ICONS = {
  guide: '<path d="M3 4h18v3.5H3zm0 6.25h7.5v3.5H3zm9.5 0H21v3.5h-8.5zM3 16.5h11V20H3zm13 0h5V20h-5z"/>',
  play: '<path d="M7 4.5v15L19.5 12z"/>',
  pause: '<path d="M6 4.5h4.2v15H6zm7.8 0H18v15h-4.2z"/>',
  back: `<path d="M12 5.5a7.5 7.5 0 1 1-7.5 7.5" fill="none" stroke="currentColor" stroke-width="2.2"/><path d="M12 1.5v8l-5-4z"/><text x="12.4" y="16.2" font-size="7.2" ${ICON_TEXT}>10</text>`,
  ahead: `<path d="M12 5.5a7.5 7.5 0 1 0 7.5 7.5" fill="none" stroke="currentColor" stroke-width="2.2"/><path d="M12 1.5v8l5-4z"/><text x="11.6" y="16.2" font-size="7.2" ${ICON_TEXT}>30</text>`,
  live: '<circle cx="12" cy="12" r="4.2"/><path d="M6.6 6.6a7.6 7.6 0 0 0 0 10.8M17.4 6.6a7.6 7.6 0 0 1 0 10.8" fill="none" stroke="currentColor" stroke-width="2"/>',
  tv: '<rect x="3" y="6.5" width="18" height="12.5" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 2.5l4 3.6 4-3.6" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="12" cy="12.75" r="2.4"/>',
  captions: `<rect x="2.5" y="5" width="19" height="14" rx="2.5" fill="none" stroke="currentColor" stroke-width="2"/><text x="12" y="15.1" font-size="7.6" ${ICON_TEXT}>CC</text>`,
  audio: '<path d="M3 9h4l5-4.5v15L7 15H3z"/><path d="M15.5 8.5a5 5 0 0 1 0 7M18.2 5.8a8.8 8.8 0 0 1 0 12.4" fill="none" stroke="currentColor" stroke-width="2"/>',
  record: '<circle cx="12" cy="12" r="7"/>',
  favorite: '<path d="M12 3.6l2.5 5.4 5.9.6-4.4 4 1.3 5.8L12 16.4l-5.3 3 1.3-5.8-4.4-4 5.9-.6z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/>',
  favorited: '<path d="M12 3.6l2.5 5.4 5.9.6-4.4 4 1.3 5.8L12 16.4l-5.3 3 1.3-5.8-4.4-4 5.9-.6z" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/>',
  last: '<path d="M8 3.5 3 8l5 4.5V9.3h9V6.7H8zM16 11.5v3.2H7v2.6h9v3.2l5-4.5z"/>',
};

const timelineEl = () => f($('#banner'), 'timeline');
const osdButtons = () => $$('#banner .osd-bar .ob').filter((b) => !b.hidden);

function wireOSD() {
  const b = $('#banner');
  f(b, 'osd').innerHTML = OSD_BUTTONS.map(([act, label]) => `<button type="button" class="ob fx" data-act="${act}" title="${esc(label)} (${OSD_KEYS[act]})"><svg viewBox="0 0 24 24" aria-hidden="true"></svg><span>${esc(label)}</span></button>`).join('');
  f(b, 'osd').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-act]');
    if (!btn) return;
    osdTouch();
    osdAct(btn.dataset.act);
  });
  b.addEventListener('focusin', (e) => {
    const btn = e.target.closest('.ob');
    if (btn) osd.last = osd.lastButton = btn.dataset.act;
    else if (e.target === timelineEl()) osd.last = 'timeline';
  });
  // The pointer on the controls keeps them up.
  b.addEventListener('mouseenter', () => { osd.hover = document.body.classList.contains('mouse-active'); osdTouch(); });
  b.addEventListener('mouseleave', () => { osd.hover = false; osdTouch(); });
  // A click on the timeline goes there.
  timelineEl().addEventListener('click', (e) => {
    const bar = $('.tl-bar', timelineEl()).getBoundingClientRect();
    if (bar.width > 0) seekTimeline((e.clientX - bar.left) / bar.width);
  });
}

// openOSD brings up the controls, with the focus on focus ("guide", another
// button's action, or "timeline"), or on none for a pointer.
function openOSD(focus = 'guide') {
  if (state.view !== 'tv' || (!state.current && !state.recording)) return;
  if (!osd.open) {
    osd.open = true;
    document.body.classList.add('osd-open');
    clearTimeout(state.bannerTimer);
    osd.tick = setInterval(renderTimeline, 1000);
  }
  renderBanner();
  $('#banner').classList.add('show', 'osd');
  if (focus) osdFocus(focus);
  osdTouch();
}

function closeOSD() {
  if (!osd.open) return;
  osd.open = false;
  clearTimeout(osd.timer);
  clearInterval(osd.tick);
  osd.tick = 0;
  osd.hover = false;
  document.body.classList.remove('osd-open');
  const b = $('#banner');
  b.classList.remove('osd', 'tl-on');
  if (state.dock >= 0 && state.view === 'tv') closeDock(false);
  blurIn(b);
}

// osdTouch starts the controls' time over: a key was pressed.
function osdTouch() {
  clearTimeout(osd.timer);
  if (!osd.open || osd.hover || pausedStream()) return;
  osd.timer = setTimeout(hideBanner, OSD_MS);
}

const hasPicture = () => !!(video.src || state.hls) && !video.hidden && $('#nosignal').hidden;
const pausedStream = () => hasPicture() && video.paused;

// osdFocus focuses a button by its action, or the timeline: the nearest
// there is when that one isn't showing.
function osdFocus(target) {
  const tl = timelineEl();
  if (target === 'timeline' && !tl.hidden) return focusEl(tl);
  const buttons = osdButtons();
  const want = target === 'timeline' ? osd.lastButton : target;
  return focusEl(buttons.find((b) => b.dataset.act === want) || buttons.find((b) => b.dataset.act === 'play') || buttons[0]);
}

// osdKey handles a key while the controls are up; it reports whether it
// did. Keys it leaves (letters, Space, channel up and down) do what they
// do on TV.
function osdKey(e) {
  const k = e.key;
  const el = document.activeElement;
  const onTl = el === timelineEl();
  const btn = el && el.closest ? el.closest('#banner .ob') : null;
  if ((k === 'ArrowLeft' || k === 'ArrowRight') && e.shiftKey) {
    e.preventDefault();
    skip(k === 'ArrowLeft' ? -60 : 60);
    renderTimeline();
    return true;
  }
  if (k === 'Escape') {
    // A channel number being typed goes first.
    e.preventDefault();
    if (state.entry) cancelEntry(); else hideBanner();
    return true;
  }
  if (!onTl && !btn) {
    // Up by a pointer, or the focus went: the first key finds it.
    if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Enter'].includes(k)) return false;
    e.preventDefault();
    osdFocus(osd.last);
    return true;
  }
  switch (k) {
    case 'ArrowLeft': case 'ArrowRight':
      if (onTl) {
        skip(k === 'ArrowLeft' ? -10 : 30);
        renderTimeline();
      } else {
        focusEl(neighbor(osdButtons(), btn, k === 'ArrowLeft' ? -1 : 1));
      }
      break;
    case 'ArrowUp':
      if (btn && !timelineEl().hidden) focusEl(timelineEl());
      else openDock();
      break;
    case 'ArrowDown':
      if (onTl) osdFocus(osd.lastButton);
      break;
    case 'Enter':
      if (onTl) togglePause(); else btn.click();
      break;
    default: return false;
  }
  e.preventDefault();
  return true;
}

function osdAct(act) {
  const after = (x) => Promise.resolve(x).then(renderOSD);
  switch (act) {
    case 'guide': return setView('guide');
    case 'play': return togglePause();
    case 'back': skip(-10); return renderTimeline();
    case 'ahead': skip(30); return renderTimeline();
    case 'live': return state.recording ? state.current && tune(state.current) : goLive();
    case 'captions': return after(toggleCaptions());
    case 'audio': return after(cycleAudio());
    case 'record': return after(recordNow('once'));
    case 'favorite': return after(toggleFavorite(state.current));
    case 'last': return state.previous && tune(state.previous);
    default: return null;
  }
}

// renderOSD shows the buttons that apply now, labeled for the state, and
// the timeline. The focus moves on from a button that went.
function renderOSD() {
  if (!osd.open) return;
  const b = $('#banner');
  const ch = state.current;
  const rec = state.recording;
  const now = Date.now();
  // Video controls for a stream, not the weather channel's display or a
  // channel that failed to tune.
  const stream = $('#nosignal').hidden && !!(rec || (ch && !ch.weather));
  const p = !rec && ch && !ch.own ? airingAt(ch, now) : null;
  const it = p && state.dvrKeys.get(airingKey(ch, p));
  const fav = !rec && isFav(ch);
  const was = document.activeElement;
  const before = osdButtons();
  const at = before.indexOf(was);
  const set = (act, on, label, icon = act, lit = false) => {
    const btn = $(`.ob[data-act="${act}"]`, b);
    btn.hidden = !on;
    if (!on) return;
    const span = btn.lastElementChild;
    if (span.textContent !== label) span.textContent = label;
    if (btn.dataset.icon !== icon) {
      btn.dataset.icon = icon;
      btn.firstElementChild.innerHTML = OSD_ICONS[icon];
    }
    btn.classList.toggle('on', lit);
    if (act !== 'guide' && act !== 'last') btn.setAttribute('aria-pressed', String(lit));
  };
  set('guide', true, 'Guide');
  set('play', stream, video.paused && hasPicture() ? 'Play' : 'Pause', video.paused && hasPicture() ? 'play' : 'pause');
  set('back', stream, '-10 s');
  set('ahead', stream, '+30 s');
  set('live', !!rec || stream, rec ? 'Live TV' : 'Live', rec ? 'tv' : 'live');
  set('captions', stream, 'Captions', 'captions', stream && captionsWanted());
  set('audio', stream && audioTracks().length > 1, 'Audio');
  // Recording is for the antenna's channels, on a server with a DVR.
  set('record', !!(state.info.dvr && p && p._e > now), it ? (it.status === 'recording' ? 'Recording' : 'Will record') : 'Record', 'record', !!it);
  set('favorite', !rec && !!ch, 'Favorite', fav ? 'favorited' : 'favorite', fav);
  set('last', !rec && !!state.previous, 'Last channel');
  const last = $('.ob[data-act="last"]', b);
  last.title = state.previous ? `Back to ${state.previous.number} ${displayCall(state.previous)} (L)` : '';
  renderTimeline();
  if (was && b.contains(was) && (was.hidden || document.activeElement !== was)) {
    const after = osdButtons();
    if (was === timelineEl() || at < 0) osdFocus(osd.lastButton);
    else focusEl(after[Math.min(at, after.length - 1)]);
  }
}

// timelineModel is what the timeline shows, in one scale: for live TV, ms
// since the epoch, from the program's start (or whatever can be rewound
// before it) to its end; for a recording, seconds into it.
function timelineModel() {
  if (!hasPicture()) return null;
  const r = video.seekable;
  const has = r && r.length > 0;
  if (state.recording) {
    const off = state.recOffset || 0;
    const pos = off + (video.currentTime || 0);
    const dur = Math.max(state.recording.duration || 0, pos);
    if (!dur) return null;
    return {
      lo: 0, hi: dur, past: 0, edge: null, head: pos, bufLo: has ? off + r.start(0) : pos, bufHi: has ? off + r.end(r.length - 1) : pos,
      left: mmss(pos), right: mmss(dur), at: mmss(pos), live: false,
    };
  }
  const now = Date.now();
  const behind = behindLive();
  const head = now - behind * 1000;
  const bufLo = has ? now - Math.max(0, livePoint() - r.start(0)) * 1000 : head;
  const p = airingAt(state.current, now);
  const lo = Math.min(p ? p._s : now - 15 * MIN, bufLo, head);
  const hi = p ? p._e : now;
  const live = behind <= 4;
  return { lo, hi, past: now, edge: now, head, bufLo, bufHi: now, left: clock(lo), right: p ? clock(hi) : 'Now', at: live ? 'Live' : `-${mmss(behind)}`, live };
}

function renderTimeline() {
  if (!osd.open) return;
  const b = $('#banner');
  const tl = timelineEl();
  const m = timelineModel();
  const show = !!m;
  if (tl.hidden === show) {
    // The timeline goes (a failed tune, the weather channel): its focus
    // moves down to the buttons.
    const had = document.activeElement === tl;
    tl.hidden = !show;
    if (had && !show) osdFocus(osd.lastButton);
  }
  b.classList.toggle('tl-on', show);
  const liveBtn = $('.ob[data-act="live"]', b);
  if (liveBtn) liveBtn.classList.toggle('at', !!(m && m.live));
  if (!m) return;
  const span = m.hi - m.lo || 1;
  const pct = (t) => `${Math.max(0, Math.min(100, ((t - m.lo) / span) * 100)).toFixed(2)}%`;
  const bar = $('.tl-bar', tl);
  const [past, buf, edge, head] = bar.children;
  past.style.width = pct(m.past);
  buf.style.left = pct(m.bufLo);
  buf.style.width = `calc(${pct(m.bufHi)} - ${pct(m.bufLo)})`;
  edge.hidden = m.edge == null;
  if (m.edge != null) edge.style.left = pct(m.edge);
  head.style.left = pct(m.head);
  f(tl, 'tl-lo').textContent = m.left;
  f(tl, 'tl-hi').textContent = m.right;
  f(tl, 'tl-at').textContent = m.at;
  tl.setAttribute('aria-valuetext', m.at);
}

// seekTimeline goes to a fraction of the timeline (a click on it).
function seekTimeline(frac) {
  const m = timelineModel();
  const r = video.seekable;
  if (!m || !r || !r.length) return;
  const t = m.lo + Math.max(0, Math.min(1, frac)) * (m.hi - m.lo);
  // Both scales count forward with the picture: ms for live, s for a recording.
  const delta = state.recording ? t - m.head : (t - m.head) / 1000;
  const from = video.currentTime;
  video.currentTime = Math.min(Math.max(from + delta, r.start(0)), state.recording ? r.end(r.length - 1) : livePoint());
  showTimeshift();
  renderTimeline();
  osdTouch();
}

// ---------- number entry ----------
function entryKey(k) {
  state.entry = (state.entry + k).slice(0, 6);
  const e = $('#entry');
  e.textContent = state.entry;
  e.classList.add('show');
  clearTimeout(state.entryTimer);
  state.entryTimer = setTimeout(commitEntry, 2200);
}

function commitEntry() {
  clearTimeout(state.entryTimer);
  const n = state.entry;
  state.entry = '';
  $('#entry').classList.remove('show');
  if (!n) return;
  const ch = findChannel(n);
  if (ch) tune(ch); else toast(`No channel ${n} in your lineup`);
}

function cancelEntry() {
  clearTimeout(state.entryTimer);
  state.entry = '';
  $('#entry').classList.remove('show');
}
