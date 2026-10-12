'use strict';

// Live rewind, the heartbeat, a recording's progress, playback's state and the system's media controls.

// ---------- live rewind, captions and audio ----------
function liveEdge() {
  const r = video.seekable;
  return r && r.length ? r.end(r.length - 1) : video.duration || 0;
}

// livePoint is where live TV plays from, and what counts as live: hls.js's
// place a little behind the edge, where it has the video (at the edge
// itself it has none yet, and WebKit drops back to the start), else the
// edge.
function livePoint() {
  const h = state.hls;
  if (h) {
    const p = h.liveSyncPosition;
    if (Number.isFinite(p) && p > 0) return Math.min(p, liveEdge());
    if (Number.isFinite(h.targetLatency)) return Math.max(0, liveEdge() - h.targetLatency);
  }
  return liveEdge();
}

function skip(seconds) {
  if (!video.src && !state.hls) return;
  const r = video.seekable;
  if (!r || !r.length) return;
  const from = video.currentTime;
  const t = Math.min(Math.max(from + seconds, r.start(0)), livePoint());
  video.currentTime = t;
  noteSkip(t - from, seconds);
  showTimeshift();
}

function togglePause() {
  if (video.paused && state.staleStream) return restartStream();
  if (video.paused) video.play().catch(() => {}); else video.pause();
  showTimeshift();
}

// ---------- heartbeat ----------
// While a stream is open, playing or paused, the app says so every 30 s:
// the server stops streams nobody fetches from for 2 minutes, freeing
// their tuners. One it stopped anyway (the app was asleep, or in the
// background) starts again at once if it was playing on TV, else on the
// next Play or return to TV.
async function heartbeat() {
  if (!(video.src || state.hls) || state.staleStream) return;
  let alive = true;
  try {
    alive = await api().Heartbeat();
  } catch (e) {
    return; // the server is away; the player has its own errors
  }
  if (alive) return;
  log('info', 'the server had stopped the stream');
  if (state.view === 'tv' && !video.paused) restartStream();
  else state.staleStream = true;
}

// restartStream starts the stream on again: the channel, or the recording
// from where it is.
function restartStream() {
  state.staleStream = false;
  if (state.recording) {
    const it = state.recording;
    return playOnDemand(it, (state.recOffset || 0) + (video.currentTime || 0));
  }
  return state.current ? tune(state.current, { quiet: true }) : undefined;
}

function goLive() {
  if (state.recording) return;
  video.currentTime = livePoint();
  video.play().catch(() => {});
  flashPlace(true);
  showTimeshift();
}

// behindLive is how far playback trails live: hls.js's live point, or for
// the webview's own HLS the edge less the delay measured when playback
// started.
function behindLive() {
  if (state.recording || !video.seekable || !video.seekable.length) return 0;
  if (state.hls) return Math.max(0, livePoint() - video.currentTime);
  return Math.max(0, liveEdge() - video.currentTime - (state.liveBaseline || 0));
}

const mmss = (sec) => {
  sec = Math.max(0, Math.round(sec));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = String(sec % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
};

// showTimeshift says in the banner's time line how far behind live the
// picture is, or where in a recording.
function showTimeshift() {
  let text = '';
  if (!video.hidden && (video.src || state.hls)) {
    if (state.recording) {
      text = `${video.paused ? 'Paused, ' : ''}${mmss((state.recOffset || 0) + video.currentTime)} of ${mmss(state.recording.duration || 0)}`;
    } else {
      const behind = behindLive();
      if (video.paused) text = behind > 2 ? `Paused, ${mmss(behind)} behind live` : 'Paused';
      else if (behind > 4) text = `${mmss(behind)} behind live`;
    }
  }
  const el = f($('#banner'), 'shift');
  if (el.textContent !== text) el.textContent = text;
}

// playOnDemand plays a recording, or a podcast's episode (kind 'episode'),
// from a point in it, on TV; the player shows positions in the whole.
async function playOnDemand(it, from = 0) {
  if (!it || it.status === 'failed') return toast('That recording failed and cannot be played');
  const token = ++state.tuneToken;
  state.staleStream = false;
  state.recording = it;
  showWX(false);
  setView('tv');
  stage.classList.add('tuning');
  $('#nosignal').hidden = true;
  showBanner();
  if (state.hls) state.hls.stopLoad();
  try {
    const pb = await (it.kind === 'episode' ? api().PlayEpisode(it.id, from) : api().PlayRecording(it.id, from));
    if (token !== state.tuneToken) return;
    state.recOffset = pb.offset || 0;
    await play(pb, token);
  } catch (e) {
    if (token !== state.tuneToken) return;
    showNoSignal({ number: it.kind === 'episode' ? 'POD' : 'REC', callSign: it.title, network: '' }, String(e && e.message ? e.message : e));
  } finally {
    if (token === state.tuneToken) setTimeout(() => stage.classList.remove('tuning'), 200);
  }
}

// ---------- recording progress ----------
let progressTimer = 0;
function saveRecordingProgress() {
  const it = state.recording;
  if (!it || !it.id) return;
  const pos = (state.recOffset || 0) + video.currentTime;
  if (pos < 1) return;
  it.position = pos;
  const saved = it.kind === 'episode'
    ? api().EpisodeProgress(it.id, pos, it.duration || 0, !!it.duration && pos > it.duration - 60)
    : api().SaveProgress(it.id, pos, it.duration || 0);
  saved.catch((e) => log('warn', `progress: ${e}`));
}

video.addEventListener('timeupdate', () => {
  if ($('#banner').classList.contains('show')) showTimeshift();
  if (osd.open) renderTimeline();
  if (state.recording && Date.now() - progressTimer > 15000) {
    progressTimer = Date.now();
    saveRecordingProgress();
  }
});
video.addEventListener('pause', () => { if (state.recording) saveRecordingProgress(); });
video.addEventListener('play', () => updatePlaybackState());
video.addEventListener('pause', () => updatePlaybackState());
// The controls stay while paused, and count down again once playing.
for (const ev of ['play', 'pause', 'playing', 'emptied']) {
  video.addEventListener(ev, () => {
    if (!osd.open) return;
    renderOSD();
    osdTouch();
  });
}
video.addEventListener('playing', () => {
  if (!state.recording && state.liveBaseline == null) state.liveBaseline = Math.max(0, liveEdge() - video.currentTime);
  applyCaptions();
  applyAudio();
});
if (video.textTracks) {
  video.textTracks.addEventListener('addtrack', applyCaptions);
  video.textTracks.addEventListener('removetrack', applyCaptions);
  video.textTracks.addEventListener('change', keepCaptions);
}
if (video.audioTracks) video.audioTracks.addEventListener('addtrack', applyAudio);

// ---------- playback state ----------
// A small badge at the picture's bottom right says what playback is doing,
// on the TV view. It stays only while there's something to do about it:
// paused (with how far behind live, or where in a recording), and
// buffering (once a stream has played, after 300 ms of waiting). The rest
// flashes and fades: a skip (the amount, adding up over quick presses) and
// then where it left the picture; how far behind live on resuming, or on
// the info key; "Live" on going back to live. Playing behind live, it's
// gone: the banner says how far. It keeps above the banner and the weather
// crawl, as the captions do.
const PS_ICONS = {
  pause: '<svg viewBox="0 0 16 16" aria-hidden="true"><rect x="3" y="2" width="3.6" height="12" rx="1"/><rect x="9.4" y="2" width="3.6" height="12" rx="1"/></svg>',
  back: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8.2 3 1.5 8l6.7 5zM15 3 8.3 8 15 13z"/></svg>',
  ahead: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M7.8 3l6.7 5-6.7 5zM1 3l6.7 5L1 13z"/></svg>',
  wait: '<i class="ps-spin"></i>',
  live: '<i class="ps-dot"></i>',
};
const PS_SKIP_MS = 1500;  // a skip shows this long after the last press
const PS_PLACE_MS = 2500; // then where it left the picture, this long
const PS_WAIT_MS = 300;   // shorter waits for data go unmarked
const ps = {
  el: $('#pstate'), skip: 0, dir: 0, skipUntil: 0, skipTimer: 0, placeUntil: 0, placeLive: false, placeTimer: 0,
  played: false, paused: false, waiting: false, waitTimer: 0, tick: 0, shown: '',
};

// noteSkip shows a skip of delta seconds (what playback moved, which is
// less than asked at either end of what can be rewound), then where it
// left the picture.
function noteSkip(delta, asked) {
  const now = Date.now();
  ps.skip = now < ps.skipUntil && Math.sign(asked) === ps.dir ? ps.skip + delta : delta;
  ps.dir = Math.sign(asked);
  ps.skipUntil = now + PS_SKIP_MS;
  clearTimeout(ps.skipTimer);
  ps.skipTimer = setTimeout(renderPlayState, PS_SKIP_MS + 20);
  flashPlace(ps.dir > 0, PS_SKIP_MS);
}

// flashPlace shows for a moment how far behind live the picture is, or
// where in a recording; at live, "Live" when live is set.
function flashPlace(live = false, after = 0) {
  ps.placeUntil = Date.now() + after + PS_PLACE_MS;
  ps.placeLive = live;
  clearTimeout(ps.placeTimer);
  ps.placeTimer = setTimeout(renderPlayState, after + PS_PLACE_MS + 20);
  renderPlayState();
}

function skipLabel() {
  const n = Math.round(ps.skip);
  if (!n) return ps.dir > 0 && !state.recording ? 'Live' : ps.dir > 0 ? 'End' : 'Start';
  const a = Math.abs(n);
  return `${n < 0 ? '-' : '+'}${a < 100 ? `${a} s` : mmss(a)}`;
}

// placeText is where the picture is: how far behind live (when it's more
// than the moment a pause or the stream's own delay accounts for), or the
// position in a recording.
function placeText(paused) {
  if (state.recording) return `${mmss((state.recOffset || 0) + video.currentTime)} / ${mmss(state.recording.duration || 0)}`;
  const behind = behindLive();
  return behind > (paused ? 2 : 4) ? `-${mmss(behind)}` : '';
}

function renderPlayState() {
  let kind = '';
  let text = '';
  const playing = !!(video.src || state.hls) && !video.hidden && $('#nosignal').hidden && !stage.classList.contains('tuning');
  if (playing) {
    const now = Date.now();
    if (now < ps.skipUntil) {
      kind = ps.dir < 0 ? 'back' : 'ahead';
      text = skipLabel();
    } else if (video.paused) {
      kind = 'pause';
      text = placeText(true);
    } else if (ps.waiting) {
      kind = 'wait';
    } else if (now < ps.placeUntil) {
      text = placeText(false);
      if (text) kind = 'place';
      else if (ps.placeLive && !state.recording) { kind = 'live'; text = 'Live'; }
    }
  }
  // While paused the live edge moves on with no timeupdate to say so.
  if (kind === 'pause' && !state.recording && !ps.tick) ps.tick = setInterval(renderPlayState, 1000);
  if (kind !== 'pause' && ps.tick) { clearInterval(ps.tick); ps.tick = 0; }
  const key = `${kind}|${text}`;
  if (key === ps.shown) return;
  ps.shown = key;
  if (!kind) {
    ps.el.classList.remove('show'); // fades out as it was
    return;
  }
  ps.el.className = `show ps-${kind}`;
  ps.el.innerHTML = `${PS_ICONS[kind] || ''}${text ? `<span>${esc(text)}</span>` : ''}`;
}

// placePlayState puts the badge at the picture's bottom right (inside any
// bars around it), or above the banner or the crawl: captionSizes'
// rule for the captions.
function placePlayState() {
  const w = stage.clientWidth;
  const h = stage.clientHeight;
  if (!w || !h) return;
  const ar = video.videoWidth && video.videoHeight ? video.videoWidth / video.videoHeight : 16 / 9;
  const pw = Math.min(w, h * ar);
  const ph = pw / ar;
  let bottom = (h - ph) / 2 + ph * 0.045;
  const banner = $('#banner');
  if (state.view === 'tv' && banner.classList.contains('show')) bottom = Math.max(bottom, h - banner.offsetTop + 14);
  const crawl = $('#crawl');
  if (crawl && document.body.classList.contains('has-crawl')) bottom = Math.max(bottom, crawl.offsetHeight + 14);
  ps.el.style.bottom = `${Math.round(bottom)}px`;
  ps.el.style.right = `${Math.round((w - pw) / 2 + pw * 0.025)}px`;
}

function startWaiting() {
  if (!ps.played || ps.waitTimer || ps.waiting) return;
  ps.waitTimer = setTimeout(() => {
    ps.waitTimer = 0;
    ps.waiting = true;
    renderPlayState();
  }, PS_WAIT_MS);
}

function stopWaiting() {
  clearTimeout(ps.waitTimer);
  ps.waitTimer = 0;
  if (ps.waiting) ps.waiting = false;
  renderPlayState();
}

video.addEventListener('waiting', startWaiting);
video.addEventListener('stalled', startWaiting);
for (const ev of ['canplay', 'pause', 'play', 'seeked']) video.addEventListener(ev, stopWaiting);
video.addEventListener('playing', () => { ps.played = true; stopWaiting(); });
// Resuming a paused stream says where it resumes.
video.addEventListener('pause', () => { if (ps.played) ps.paused = true; });
video.addEventListener('play', () => {
  if (!ps.paused) return;
  ps.paused = false;
  flashPlace();
});
// A new stream: its first wait for data is the tune, which has its static.
video.addEventListener('emptied', () => {
  ps.played = false;
  ps.paused = false;
  ps.skipUntil = 0;
  ps.placeUntil = 0;
  stopWaiting();
});
video.addEventListener('timeupdate', renderPlayState);
video.addEventListener('resize', placePlayState);
if (window.ResizeObserver) new ResizeObserver(placePlayState).observe(stage);
new MutationObserver(() => { placePlayState(); renderPlayState(); }).observe($('#banner'), { attributes: true, attributeFilter: ['class'] });
new MutationObserver(() => { placePlayState(); renderPlayState(); }).observe(document.body, { attributes: true, attributeFilter: ['class'] });
new MutationObserver(renderPlayState).observe(stage, { attributes: true, attributeFilter: ['class'] });
new MutationObserver(renderPlayState).observe($('#nosignal'), { attributes: true, attributeFilter: ['hidden'] });

// ---------- media session ----------
// What is on, for the system's media controls and keys: Now Playing on a
// Mac, MPRIS on Linux (which WebKitGTK provides).
const mediaSession = navigator.mediaSession;

function wireMediaSession() {
  if (!mediaSession) return;
  const on = (action, fn) => {
    try { mediaSession.setActionHandler(action, fn); } catch { /* not offered here */ }
  };
  on('play', () => { if (video.paused) togglePause(); });
  on('pause', () => { if (!video.paused) togglePause(); });
  on('nexttrack', () => step(1));
  on('previoustrack', () => step(-1));
  on('seekforward', () => skip(30));
  on('seekbackward', () => skip(-10));
}

let mediaShown = '';
function updateMediaSession() {
  if (!mediaSession || !window.MediaMetadata) return;
  const ch = state.current;
  const it = state.recording;
  let m = null;
  if (it) {
    m = { title: it.title, artist: 'Recording', album: it.subtitle || 'Airwaves', art: imageURL(it.image) };
  } else if (ch && ch.weather) {
    m = { title: 'Local Forecast', artist: `${ch.number} ${ch.name}`, album: 'Airwaves', art: ch.logo };
  } else if (ch) {
    const p = airingAt(ch, Date.now());
    const name = ch.network || displayCall(ch);
    m = { title: p ? p.title : name, artist: `${ch.number} ${name}`, album: (p && p.episodeTitle) || 'Airwaves', art: imageURL(p && p.image) || ch.logo };
  }
  const key = JSON.stringify(m);
  if (key === mediaShown) return;
  mediaShown = key;
  mediaSession.metadata = m && new window.MediaMetadata({ title: m.title, artist: m.artist, album: m.album, artwork: m.art ? [{ src: m.art }] : [] });
  updatePlaybackState();
}

function updatePlaybackState() {
  if (!mediaSession) return;
  const wx = state.current && state.current.weather && !state.recording;
  mediaSession.playbackState = !state.current && !state.recording ? 'none' : wx || !video.paused ? 'playing' : 'paused';
}
