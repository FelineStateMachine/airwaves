'use strict';

// ---------- helpers ----------
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const f = (root, name) => root.querySelector(`[data-f="${name}"]`);
const api = () => window.go && window.go.main && window.go.main.App;
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const MIN = 60_000;
const floor30 = (t) => Math.floor(t / (30 * MIN)) * 30 * MIN;
// One formatter for every time shown: toLocaleTimeString makes a new one
// each call, which is most of the guide's build time.
const clockFormat = new Intl.DateTimeFormat([], { hour: 'numeric', minute: '2-digit' });
const clock = (t) => clockFormat.format(new Date(t));
const COMPASS = ['N', 'NNE', 'NE', 'ENE', 'E', 'ESE', 'SE', 'SSE', 'S', 'SSW', 'SW', 'WSW', 'W', 'WNW', 'NW', 'NNW'];
const compass = (deg) => COMPASS[Math.round(deg / 22.5) % 16];

function log(level, msg) {
  try { api().Log(level, String(msg)); } catch { /* not in Wails */ }
}

// ---------- signal ----------
// Everything shown of reception was measured by the server's tuners (its
// /api/signal): per RF channel, the latest reading and the latest stretch
// of readings (recent: the last Measure now, or the last while something
// watched it), with lows and means. The stretch is what's shown: a single
// reading of a marginal signal jumps around. An HDHomeRun reports strength
// and quality (its signal-to-noise quality) as percentages; other tuners
// may report SNR in dB.
const SIG_COLOR = { lock: 'var(--amber)', part: 'var(--amber-2)', off: 'var(--red)', none: 'var(--faint)' };

// sigOf sums up a measurement ({ signal, recent }, of a channel or an RF
// channel): its state ('lock' throughout, 'part' some of the time, 'off'
// none, 'none' never measured), bars to show (0 to 5, from the measured
// quality), a label and the details.
function sigOf(m) {
  const r = m && m.recent;
  const sig = m && m.signal;
  if (!r && !sig) return { state: 'none', bars: 0, label: 'Not measured yet', color: SIG_COLOR.none, quality: '', strength: '', errors: null, at: 0, source: '', lockedPct: 0 };
  const lockedPct = r ? r.lockedPct : sig.lock ? 100 : 0;
  const state = lockedPct >= 100 ? 'lock' : lockedPct > 0 ? 'part' : 'off';
  // From the stretch, its means; else the one reading.
  const val = (key) => (r ? (r[key] ? r[key].avg : null) : sig[key]);
  const src = r || sig;
  const q = val('qualityPct');
  const snr = val('snrDb');
  const strengthPct = val('strengthPct');
  const strengthDbm = val('strengthDbm');
  let bars = 0;
  if (state !== 'off') {
    if (q != null) bars = q >= 70 ? 5 : q >= 55 ? 4 : q >= 40 ? 3 : q >= 25 ? 2 : q > 0 ? 1 : 0;
    else if (snr != null) bars = snr >= 30 ? 5 : snr >= 25 ? 4 : snr >= 20 ? 3 : snr >= 17 ? 2 : snr > 0 ? 1 : 0;
    else bars = 3;
  }
  const quality = q != null ? `${Math.round(q)}%` : snr != null ? `${snr.toFixed(1)} dB` : '';
  const strength = strengthPct != null ? `${Math.round(strengthPct)}%` : strengthDbm != null ? `${strengthDbm.toFixed(1)} dBm` : '';
  const errors = sig && sig.errorsPerSec != null ? sig.errorsPerSec : null;
  const label = state === 'off' ? 'No lock'
    : state === 'part' ? `Locked ${Math.round(lockedPct)}% of the time`
      : quality ? `Quality ${quality}` : 'Locked';
  return { state, bars, label, color: SIG_COLOR[state], quality, strength, errors, at: Date.parse(src.to || src.at), source: src.source, lockedPct };
}

// meter draws a measurement's bars.
function meter(s, big) {
  let html = '';
  for (let i = 1; i <= 5; i++) html += `<i class="${i <= s.bars ? 'on' : ''}"></i>`;
  return `<span class="meter${big ? ' big' : ''} m-${s.state}" style="--c:${s.color}">${html}</span>`;
}

// ago says how long ago a time was, roughly.
function ago(t) {
  const m = Math.round((Date.now() - t) / MIN);
  if (m < 1) return 'just now';
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  return new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' });
}

// agoShort is ago for a table: "10 min", "3 h", "Oct 3".
function agoShort(t) {
  const m = Math.round((Date.now() - t) / MIN);
  if (m < 1) return 'now';
  if (m < 60) return `${m} min`;
  if (m < 24 * 60) return `${Math.round(m / 60)} h`;
  return new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' });
}

// sigDetail is a measurement's details in words: "strength 82%, 18
// errors a second, 5 min ago".
function sigDetail(s) {
  if (s.state === 'none') return 'Measure now in Reception reads it';
  const parts = [];
  if (s.state !== 'lock' && s.quality && s.state !== 'off') parts.push(`quality ${s.quality}`);
  if (s.strength) parts.push(`strength ${s.strength}`);
  if (s.errors != null && s.state !== 'off') parts.push(s.errors >= 1 ? `${Math.round(s.errors)} errors a second` : 'no errors');
  parts.push(`${ago(s.at)}${s.source === 'active' ? ', while tuned' : ''}`);
  return parts.join(', ');
}

// trouble is a measurement that explains a bad picture: no lock, a lock
// that comes and goes, or a stream breaking up (5 errors a second or more).
const trouble = (s) => s.state === 'off' || s.state === 'part' || (s.errors != null && s.errors >= 5);

let toastTimer = 0;
function toast(msg, ms = 3500, html = false) {
  const t = $('#toast');
  if (html) t.innerHTML = msg; else t.textContent = msg;
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), ms);
}

// Lite mode is for Linux, where the app is WebKitGTK on a TV box: the same
// look without the blurs, film grain, big soft shadows and animations that
// cost a weak CPU and GPU most (style.css), and less video kept in memory.
// AIRWAVES_LITE=1 or 0 (in Boot) turns it on or off anywhere.
let lite = false;
function setLite(on) {
  lite = on;
  document.body.classList.toggle('lite', on);
}
setLite(/Linux/.test(navigator.userAgent) && !/Android/.test(navigator.userAgent));

// The Android TV app is a WebView whose user agent ends in
// AirwavesTV/<version>, showing this page from the server (web.js): a TV
// with a remote, which has arrows, OK, Back and media keys but no letters
// or pointer.
const androidTV = /\bAirwavesTV\//.test(navigator.userAgent);
document.body.classList.toggle('android-tv', androidTV);

// ---------- state ----------
const state = {
  boot: null,
  settings: null,
  report: null,
  guide: null,
  byFacility: new Map(), // facilityId -> nearest station
  bySite: new Map(),     // "facilityId:rf" -> station
  antenna: null,   // the tuner and its RF channels, as last measured (/api/signal)
  antennaChans: [], // the tuner's channels, hidden ones too
  byRF: new Map(), // RF channel -> what's known of it (antenna.muxes)
  sigStamp: 0,     // counts signal updates, for redraws
  lineup: [],
  custom: [],      // the server's own channels: the weather channel, folder, Jellyfin and YouTube channels
  current: null,
  previous: null,
  note: '',
  view: 'tv',
  tuneToken: 0,
  hls: null,
  entry: '',
  entryTimer: 0,
  bannerTimer: 0,
  guideFilter: 'all',
  gStart: 0,
  gSpan: 150,
  gRow: 0,
  gTime: 0,
  aRF: 0,          // the RF channel picked in Reception
  info: null,      // backend: local engine or a server
  config: null,    // location the lineup is built for
  dvr: null,       // recording state from the server
  dvrKeys: new Map(), // "7.1@1791058239" -> upcoming item
  recording: null, // recording being played instead of live TV
  captionsFor: null, // captions turned off or on for one program: { key, on }
  recTab: 'library',
  recSelBy: null,
  dock: -1,        // dock button picked with the arrow keys, or -1
};

const video = $('#video');
const stage = $('#stage');

// ---------- data ----------
async function scan(refresh) {
  const snap = await api().Scan(refresh);
  state.report = snap.report;
  state.guide = snap.guide || { programs: {} };
  // The antenna channels are the tuner's: those its scan found, described
  // from the FCC's records and the listings. Their keys (for favorites and
  // hidden channels) carry the listings' call sign where there is one
  // ("2.1|KWGNDT"), as keys always have, else the station's.
  state.antennaChans = (snap.antenna ? snap.antenna.channels : snap.report.channels || [])
    .map((c) => ({ ...c, key: `${c.number}|${c.guideCallSign || c.callSign}` }));
  if (snap.antenna) applySignal(snap.antenna);
  // The server's own channels, with the details set on its admin page. The
  // weather channel plays here, from the WeatherStar display; the others'
  // schedules join the guide like a station's, under their own guide id,
  // their category counting as a genre of each program. A server from
  // before the weather channel was listed here sends no list, or channels
  // without a kind: then the weather channel is the old WX.
  const listed = Array.isArray(snap.custom) && snap.custom.every((c) => c.kind);
  state.custom = (snap.custom || []).map((c) => {
    const ch = ownChannel(c);
    if (!ch.weather) {
      const genres = categoryGenres(ch.category);
      state.guide.programs[ch.guideId] = (c.programs || []).map((p) => (genres.length ? { ...p, genres: [...new Set([...(p.genres || []), ...genres])] } : p));
    }
    return ch;
  });
  if (!listed && state.info.weatherStar && !state.custom.some((c) => c.weather)) state.custom.unshift(OLD_WX);
  indexData();
  await migrateKeys();
  buildLineup();
}

function indexData() {
  state.byFacility.clear();
  state.bySite.clear();
  const stations = [...state.report.stations].sort((a, b) => a.distanceKm - b.distanceKm);
  for (const s of stations) {
    if (!state.byFacility.has(s.facilityId)) state.byFacility.set(s.facilityId, s);
    state.bySite.set(`${s.facilityId}:${s.rfChannel}`, s);
  }
  const progs = (state.guide && state.guide.programs) || {};
  for (const list of Object.values(progs)) {
    for (const p of list) { p._s = Date.parse(p.start); p._e = Date.parse(p.end); }
  }
}

// ---------- the server's own channels ----------
// Keys (for favorites and hidden channels) stay with a channel through a new
// number or name: the weather channel has one key, and a custom channel's
// carries its name as well as its number (migrateKeys follows either).
const WEATHER_KEY = 'weather';

// A custom channel's category counts as these genres for its programs, so
// the guide's filters find it; categories with no filter of their own get
// one while a channel has them (guideFilters).
const CATEGORY_GENRES = {
  Sports: ['sports'], Movies: ['movie'], News: ['news'], Family: ['family'], Kids: ['kids', 'family'],
  Music: ['music'], Pets: ['pets'], Gaming: ['gaming'], Documentary: ['documentary'], Weather: ['weather'],
};
const categoryGenres = (category) => CATEGORY_GENRES[category] || [];

// ownChannel is a channel the server lists as its own, for the lineup. Its
// call sign is the short label (call); its name stands where a station's
// network does.
function ownChannel(c) {
  const [major, minor] = c.number.split('.').map(Number);
  const weather = c.kind === 'weather';
  return {
    key: weather ? WEATHER_KEY : `${c.number}|custom|${c.name}`,
    number: c.number, major: major || 0, minor: minor || 0, name: c.name, network: c.name,
    call: c.callSign || '', callSign: c.callSign || '', baseCall: c.callSign || '',
    kind: c.kind || 'folder', category: c.category || 'Other', description: c.description || '',
    logo: c.logo ? state.boot.serverUrl + c.logo : '',
    own: true, weather, custom: !weather, guideId: weather ? '' : `custom:${c.number}`,
  };
}

// OLD_WX is the weather channel of a server from before it listed it.
const OLD_WX = {
  key: WEATHER_KEY, number: 'WX', major: 0, minor: 0, name: 'Airwaves Weather', network: 'Local Forecast',
  call: 'WX', callSign: 'WX', baseCall: 'WX', kind: 'weather', category: 'Weather', description: '', logo: '',
  own: true, weather: true, custom: false, guideId: '',
};

const weatherChannel = () => state.custom.find((c) => c.weather) || null;

// migrateKeys moves saved favorites and hidden channels to where the
// server's own channels are now: the weather channel's from the old WX key,
// and a custom channel's to its current number and name (found by name, or
// else by number). An antenna channel's whose call sign changed moves to
// the one channel with its number. Keys of channels not listed now are kept
// for when they are. A saved last channel of WX becomes the weather
// channel's number, and one no longer in use the new number of the custom
// channel a saved key names with it.
async function migrateKeys() {
  const s = state.settings;
  const custom = state.custom.filter((c) => !c.weather);
  const known = new Set([...state.custom, ...state.antennaChans].map((c) => c.key));
  const fix = (k) => {
    if (k === 'WX|WX') return WEATHER_KEY;
    if (known.has(k)) return k;
    const [num, kind, ...rest] = String(k).split('|');
    if (kind !== 'custom') {
      const same = state.antennaChans.filter((c) => c.number === num);
      return same.length === 1 && kind ? same[0].key : k;
    }
    const name = rest.join('|').toLowerCase();
    const named = name ? custom.filter((c) => c.name.toLowerCase() === name) : [];
    if (named.length === 1) return named[0].key;
    const numbered = custom.find((c) => numberKey(c) === numberKey({ number: num }));
    return numbered ? numbered.key : k;
  };
  const favorites = [...new Set((s.favorites || []).map(fix))];
  const hidden = [...new Set((s.hidden || []).map(fix))];
  const wx = weatherChannel();
  let lastChannel = s.lastChannel;
  const numbers = new Set([...state.custom, ...state.antennaChans].map((c) => c.number));
  if (lastChannel === 'WX' && wx) lastChannel = wx.number;
  else if (lastChannel && !numbers.has(lastChannel)) {
    const old = [...(s.favorites || []), ...(s.hidden || [])].find((k) => k.startsWith(`${lastChannel}|custom|`));
    const now = old && state.custom.find((c) => c.key === fix(old));
    if (now) lastChannel = now.number;
  }
  const same = (a, b) => a.length === (b || []).length && a.every((k, i) => k === b[i]);
  if (same(favorites, s.favorites) && same(hidden, s.hidden) && lastChannel === s.lastChannel) return;
  log('info', 'settings: moved saved channels to their current numbers');
  try {
    await saveAppSettings({ ...s, favorites, hidden, lastChannel });
  } catch (e) {
    log('warn', `settings: ${e}`);
    state.settings = { ...s, favorites, hidden, lastChannel };
  }
}

const isFav = (ch) => !!ch && (state.settings.favorites || []).includes(ch.key);
const isHidden = (ch) => !!ch && (state.settings.hidden || []).includes(ch.key);

// numberKey is a channel number's value: "1.05" and "1.5" are one number.
function numberKey(c) {
  const [major, minor] = String(c.number).split('.').map((x) => parseInt(x, 10) || 0);
  return `${major}.${minor}`;
}
const byNumber = (a, b) => (a.major - b.major) || (a.minor - b.minor);

// buildLineup is the channels to show, by number: the server's own and the
// tuner's, less those hidden. A tuner's channel stays when its signal is
// weak now; tuning it then says so. A custom channel takes the place of an
// antenna channel with its number, as it does on the server, which plays it
// for that number.
function buildLineup() {
  const taken = new Set(state.custom.map(numberKey));
  const antenna = state.antennaChans.filter((c) => !taken.has(numberKey(c)) && !isHidden(c));
  state.lineup = [...state.custom.filter((c) => !isHidden(c)), ...antenna].sort(byNumber);
  if (state.current) state.current = state.lineup.find((c) => c.key === state.current.key) || state.current;
}

const programsFor = (ch) => (ch && ch.weather ? wxPrograms() : (ch && ch.guideId && state.guide && state.guide.programs[ch.guideId]) || []);
const airingAt = (ch, t) => programsFor(ch).find((p) => p._s <= t && t < p._e);
const nextAfter = (ch, t) => programsFor(ch).find((p) => p._s > t);
// stationFor is the licensed transmitter a channel comes from, on its RF
// channel.
const stationFor = (ch) => ch && (state.bySite.get(`${ch.facilityId}:${ch.rf}`) || state.byFacility.get(ch.facilityId));
// sigFor is what was measured of a channel's RF channel.
const sigFor = (ch) => (ch && !ch.own ? { signal: ch.signal, recent: ch.recent } : null);
// nextGen reports whether the tuners take in ATSC 3.0: through Tvheadend
// they never do, so NextGen viewing (its guide filter) stays out of sight.
const nextGen = () => !!(state.info && state.info.tuner && state.info.tuner.atsc3);
// displayCall is a channel's short label: a station's call sign, or an own
// channel's call sign, "Airwaves" when it has none.
const displayCall = (ch) => (ch && ch.own ? ch.call || 'Airwaves' : (ch && (ch.baseCall || ch.callSign)) || '');

function findChannel(number) {
  if (!number) return null;
  const n = number;
  return state.lineup.find((c) => c.number === n)
    || state.lineup.find((c) => c.major === Number(n) && (c.minor === 1 || c.minor === 0))
    || state.antennaChans.find((c) => c.number === n)
    || state.custom.find((c) => c.number === n);
}

// ---------- boot ----------
function bootLog(msg, err) {
  const li = document.createElement('li');
  li.textContent = msg;
  if (err) li.className = 'err';
  const ol = $('.boot-log');
  ol.append(li);
  while (ol.children.length > 7) ol.firstChild.remove();
}

async function init() {
  wireKeys();
  wireOSD();
  wireDock();
  wireMouse();
  wireGuide();
  wireAntenna();
  wireSettings();
  wireRecordings();
  wireWeather();
  wirePads();
  wireMediaSession();
  renderHints();
  // Keys work from launch, without a click first.
  window.focus();
  if (document.activeElement && document.activeElement !== document.body && !document.activeElement.matches(TEXT_INPUT)) document.activeElement.blur();
  if (!api()) {
    bootLog('Wails runtime not found. Run this page inside the Airwaves app.', true);
    return;
  }
  window.runtime.EventsOn('progress', (msg) => {
    if (document.body.classList.contains('booting')) bootLog(msg);
  });
  applyBoot(await api().Boot());
  if (!state.settings.server) return showConnect('');
  f($('#boot'), 'sub').textContent = `Connecting to ${state.settings.server}`;
  try {
    if (state.boot.error) throw new Error(state.boot.error);
    await scan(false);
  } catch (e) {
    return showConnect(String(e && e.message ? e.message : e));
  }
  f($('#boot'), 'sub').textContent = state.info.name;
  for (const w of state.report.warnings || []) bootLog(w, true);
  bootLog(`Ready: ${state.lineup.length} channels`);
  setTimeout(() => document.body.classList.remove('booting'), 700);
  // Gone once faded out, so its title stops animating.
  setTimeout(() => { $('#boot').hidden = true; }, 700 + 1200);

  renderAntenna();
  renderSettings();
  const view = state.boot.view;
  const start = findChannel(state.settings.lastChannel) || state.lineup.find((c) => c.major >= 2) || state.lineup[0];
  if (state.info.playback) toast(`Playback unavailable: ${state.info.playback}`, 8000);
  if (start) tune(start, { quiet: !!view && view !== 'tv' });
  if (view === 'info') {
    // Pinned banner, for screenshots.
    showBanner();
    clearTimeout(state.bannerTimer);
  } else if (view) setView(view);
  setInterval(tick, 30_000);
  setInterval(() => scan(false).then(renderAll).catch((e) => log('warn', e)), 30 * MIN);
  setInterval(() => { if (state.view === 'guide' || state.view === 'recordings') loadDVR(); }, MIN);
  setInterval(loadWeather, 2 * MIN);
  setInterval(signalTick, 2000);
  loadDVR();
  loadWeather();
}

// showConnect asks for the server address on first launch or when the
// configured server cannot be reached.
function showConnect(err) {
  const form = $('.boot-connect');
  f($('#boot'), 'sub').textContent = 'Connect to your Airwaves server';
  if (err) bootLog(err, true);
  form.elements.server.value = state.settings.server || '';
  form.elements.token.value = state.settings.token || '';
  form.hidden = false;
  // In a browser the server is the page's own (web.js): only a token to give.
  const fixed = !!(state.boot && state.boot.fixedServer);
  form.elements.server.readOnly = fixed;
  form.elements.server.parentElement.classList.toggle('fx', !fixed);
  bootField(form.elements[fixed ? 'token' : 'server'].parentElement);
  f($('#boot'), 'hint').innerHTML = hints([['Arrows', 'Arrows', 'move'], ['Enter', 'Enter', androidTV ? 'types, or connects' : 'connects']]);
  form.onsubmit = async (e) => {
    e.preventDefault();
    const server = form.elements.server.value.trim();
    if (!server) return;
    bootLog(`Connecting to ${server}`);
    const b = await api().SaveSettings({ ...state.settings, server, token: form.elements.token.value });
    if (b.error) return bootLog(b.error, true);
    location.reload();
  };
}

function applyBoot(b) {
  state.boot = b;
  state.settings = b.settings;
  state.info = b.info || { name: '', tuner: {}, dvr: false };
  state.config = b.config || {};
  document.body.classList.toggle('has-dvr', !!state.info.dvr);
  // A server without an antenna (custom channels only) has no reception.
  $('#dock [data-view="antenna"]').style.display = state.info.antenna === false ? 'none' : '';
  if (b.lite === '1' || b.lite === '0') setLite(b.lite === '1');
  applyScale();
}

// ---------- recordings ----------
async function loadDVR() {
  if (!state.info || !state.info.dvr) return;
  try {
    state.dvr = await api().DVR();
  } catch (e) {
    log('warn', `dvr: ${e}`);
    return;
  }
  state.dvrKeys = new Map((state.dvr.upcoming || []).map((it) => [it.key, it]));
  if (state.view === 'guide') renderGuide();
  if (state.view === 'recordings') renderRecordings();
}

const airingKey = (ch, p) => `${ch.number}@${Math.floor(p._s / 1000)}`;

function seriesRuleFor(ch, p) {
  return p && p.seriesId && state.dvr && (state.dvr.rules || []).find((r) => r.kind === 'series' && r.seriesId === p.seriesId && r.channel === ch.number);
}

function renderAll() {
  renderBanner();
  if (state.view === 'guide') renderGuide();
  renderAntenna();
  renderSettings();
}

function tick() {
  renderBanner();
  if (state.view === 'guide') renderGuide();
  applyCaptions(); // a new program may want them otherwise
}

// ---------- views ----------
function setView(v) {
  if (state.dock >= 0) closeDock(false);
  if (v === state.view) return restoreFocus();
  const prev = state.view;
  // The banner and the controls go with TV (their timers with them).
  if (prev === 'tv') hideBanner();
  // The focus starts over in the new view.
  blurIn(document.body);
  state.view = v;
  document.body.classList.remove(`mode-${prev}`);
  document.body.classList.add(`mode-${v}`);
  $$('#dock button').forEach((b) => b.classList.toggle('on', b.dataset.view === v));
  if (v === 'guide') { openGuide(); loadDVR(); }
  if (v === 'recordings') { openRecordings(); loadDVR(); }
  if (v === 'weather') {
    renderWeather();
    wxUI.at = 0;
    wxFocus();
    loadWeather();
    if (state.info.weatherStar && !(state.current && state.current.weather)) {
      state.wxPreview = true;
      video.muted = true;
      showWX(true, true);
    }
  }
  if (prev === 'weather' && state.wxPreview) {
    state.wxPreview = false;
    video.muted = !!state.userMuted;
    if (!(state.current && state.current.weather)) showWX(false);
  }
  if (v === 'antenna') openAntenna();
  if (v === 'settings') openSettings();
  return undefined;
}

function wireDock() {
  $$('#dock button').forEach((b) => b.addEventListener('click', () => setView(b.dataset.view)));
  $('#dock button[data-view="tv"]').classList.add('on');
  stage.addEventListener('click', () => {
    if (state.view === 'weather' && state.wxPreview) tune(weatherChannel());
    if (state.view !== 'tv') setView('tv');
  });
  stage.addEventListener('dblclick', () => { if (state.view === 'tv') toggleFullscreen(); });
}

let mouseTimer = 0;
function wireMouse() {
  // WebKitGTK focuses a button when it is clicked, and Enter or Space then
  // pressed it again besides doing what the key does here. Clicks leave
  // the focus where it was, as on a Mac.
  document.addEventListener('mousedown', (e) => {
    if (!e.target.closest(TEXT_INPUT) && e.target.closest('button, .fx')) e.preventDefault();
  });
  // Only a pointer that really moved counts. WebKit also sends moves (and
  // mouseenter) when content appears under a pointer left still, as on a
  // TV where it rests wherever it was; the banner then never hid.
  document.addEventListener('mousemove', (e) => {
    if (!e.movementX && !e.movementY) return;
    document.body.classList.add('mouse-active');
    clearTimeout(mouseTimer);
    mouseTimer = setTimeout(() => document.body.classList.remove('mouse-active'), 2500);
    // The controls, for a pointer near the bottom; no focus mark.
    if (state.view === 'tv' && e.clientY > window.innerHeight * 0.55) openOSD(null);
  });
}

async function toggleFullscreen() {
  const rt = window.runtime;
  if (!rt) return;
  if (await rt.WindowIsFullscreen()) rt.WindowUnfullscreen(); else rt.WindowFullscreen();
}

// ---------- tuning & playback ----------
async function tune(ch, { quiet = false } = {}) {
  if (!ch) return;
  if (state.recording) saveRecordingProgress();
  if (state.current && state.current.key !== ch.key) state.previous = state.current;
  state.current = ch;
  state.note = '';
  state.recording = null;
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
  try {
    const pb = await api().Tune(ch.number);
    if (token !== state.tuneToken) return;
    state.note = pb.note || '';
    await play(pb, token);
  } catch (e) {
    if (token !== state.tuneToken) return;
    log('warn', `tune ${ch.number}: ${e}`);
    showNoSignal(ch, String(e && e.message ? e.message : e));
    // The server read the tuner while it tried.
    if (!ch.own) loadSignal();
  } finally {
    if (token === state.tuneToken) setTimeout(() => stage.classList.remove('tuning'), 200);
  }
  renderBanner();
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
  if (p && p.rating) out.push(`<span class="chip">${esc(p.rating)}</span>`);
  for (const t of (p && p.tags) || []) out.push(`<span class="chip">${esc(t)}</span>`);
  return out.join('');
}

function episodeLine(p) {
  if (!p) return '';
  const se = p.season && p.episode ? `S${p.season} E${p.episode}` : '';
  return [p.episodeTitle && `"${p.episodeTitle}"`, se, p.year].filter(Boolean).join('   ');
}

function renderBanner() {
  updateMediaSession();
  showTimeshift();
  renderOSD();
  const ch = state.current;
  const b = $('#banner');
  if (state.recording) return renderRecordingBanner(b, state.recording);
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
  f(b, 'atsc3').textContent = ch.atsc3 ? `Also in ATSC 3.0 on ${ch.atsc3.hostCall} RF ${ch.atsc3.rf}${nextGen() ? '' : ', which needs a NextGen TV tuner'}` : '';
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
const OSD_KEYS = { guide: 'G', play: 'Space', back: 'Left', ahead: 'Right', live: 'End', captions: 'C', audio: 'V', record: 'R', favorite: 'F', last: 'L' };
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

// ---------- keyboard ----------
// Text fields keep their keys (but Esc).
const TEXT_INPUT = 'input:not([type=checkbox]):not([type=radio]), textarea, select';

// The media keys of a TV remote (over HDMI-CEC, or an Android TV remote)
// or a keyboard, as the keys the views handle: [key, shift]. Page Down is
// channel up.
const KEY_ALIASES = {
  MediaPlayPause: [' '], MediaPlay: [' '], MediaPause: [' '],
  MediaFastForward: ['ArrowRight'], MediaRewind: ['ArrowLeft'],
  MediaTrackNext: ['PageDown'], MediaTrackPrevious: ['PageUp'],
  ChannelUp: ['PageDown'], ChannelDown: ['PageUp'],
  MediaRecord: ['r'], BrowserBack: ['Escape'], GoBack: ['Escape'],
  Guide: ['g'], Info: ['i'], ClosedCaptionToggle: ['c'], MediaLast: ['l'], MediaAudioTrack: ['v'],
};

// pressKey does what a key does, for a controller button or a media key:
// an untrusted keydown runs the same handler and nothing else (no button
// is pressed, no text typed).
function pressKey(key, { shiftKey = false, repeat = false } = {}) {
  (document.activeElement || document.body).dispatchEvent(new KeyboardEvent('keydown', { key, shiftKey, repeat, bubbles: true, cancelable: true }));
}

// Keys held down: the Android TV app's WebView sends a held key again
// without marking it a repeat, so another keydown before its keyup is one.
const held = new Map(); // key: when its last keydown came
const HELD_MS = 700;

function wireKeys() {
  document.addEventListener('keydown', (e) => {
    let again = e.repeat;
    if (e.isTrusted) {
      setPrompts(false);
      const at = held.get(e.key);
      again = again || (at !== undefined && e.timeStamp - at < HELD_MS);
      held.set(e.key, e.timeStamp);
    }
    const alias = KEY_ALIASES[e.key];
    if (alias) {
      e.preventDefault();
      return pressKey(alias[0], { shiftKey: !!alias[1], repeat: again });
    }
    const typing = e.target.matches && e.target.matches(TEXT_INPUT);
    if (document.body.classList.contains('booting')) return bootKey(e, typing);
    if (typing && e.key !== 'Escape') return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const k = e.key;
    // A held OK, or Space, acts once; held, OK is a long press.
    if ((k === 'Enter' || k === ' ') && again) {
      e.preventDefault();
      if (k === 'Enter') holdLong();
      return;
    }
    if (osd.open) osdTouch();
    if (state.dock >= 0 && dockKey(e)) return;
    if (typing) {
      // Esc puts a field away, back to its row.
      e.preventDefault();
      e.target.blur();
      return restoreFocus();
    }
    if (state.view === 'settings' && settingsKey(e)) return;
    if (state.view === 'tv' && osd.open && osdKey(e)) return;
    if (k === 'Escape') {
      if (state.entry) return cancelEntry();
      if (viewBack()) return;
      if (state.view !== 'tv') return setView('tv');
      if (state.recording && state.current) return tune(state.current);
      return hideBanner();
    }
    if (k === 'g' || k === 'G') return setView(state.view === 'guide' ? 'tv' : 'guide');
    if ((k === 'a' || k === 'A') && state.info.antenna !== false) return setView(state.view === 'antenna' ? 'tv' : 'antenna');
    if ((k === 'd' || k === 'D') && state.info.dvr) return setView(state.view === 'recordings' ? 'tv' : 'recordings');
    if ((k === 'w' || k === 'W') && state.view !== 'recordings') return setView(state.view === 'weather' ? 'tv' : 'weather');
    if (k === ',' || k === 's' || k === 'S') return setView(state.view === 'settings' ? 'tv' : 'settings');
    if (k === 'm' || k === 'M') {
      const onWX = state.current && state.current.weather;
      const muted = !(onWX ? state.userMuted : video.muted);
      state.userMuted = muted;
      if (!state.wxPreview) video.muted = muted;
      wxMusic.muted = muted;
      return toast(muted ? 'Muted' : 'Sound on', 1200);
    }
    if ((k === 'u' || k === 'U') && state.lastHidden) {
      const key = state.lastHidden;
      state.lastHidden = null;
      return unhide([key]).then(() => toast('Channel shown again', 1800));
    }
    if (k === 'F') return toggleFullscreen();

    if (state.view === 'guide') return guideKey(e);
    if (state.view === 'antenna') return antennaKey(e);
    if (state.view === 'recordings') return recordingsKey(e);
    if (state.view === 'weather') return weatherKey(e);
    if (state.view !== 'tv') return;

    if (/^[0-9]$/.test(k) || (k === '.' && state.entry)) { e.preventDefault(); return entryKey(k); }
    switch (k) {
      case 'ArrowUp': case 'PageUp': e.preventDefault(); return step(-1);
      case 'ArrowDown': case 'PageDown': e.preventDefault(); return step(1);
      // Left and Right skip, and bring up the controls with the timeline
      // picked, for more of the same.
      case 'ArrowLeft': e.preventDefault(); skip(e.shiftKey ? -60 : -10); return openOSD('timeline');
      case 'ArrowRight': e.preventDefault(); skip(e.shiftKey ? 60 : 30); return openOSD('timeline');
      case ' ': e.preventDefault(); return togglePause();
      case 'End': e.preventDefault(); return goLive();
      case 'Enter': e.preventDefault(); return state.entry ? commitEntry() : openOSD();
      case 'i': case 'I': return cycleBanner();
      case 'l': case 'L': case 'Backspace': return state.previous && tune(state.previous);
      case 'r': return recordNow('once');
      case 'R': return recordNow('series', false);
      case 'n': case 'N': return recordNow('series', true);
      case 'c': case 'C': return toggleCaptions();
      case 'v': case 'V': return cycleAudio();
      case 'f': return toggleFavorite(state.current);
      default:
    }
  });
  document.addEventListener('keyup', (e) => {
    held.delete(e.key);
    holdEnd(e);
  });
  window.addEventListener('blur', () => held.clear());
}

// ---------- holding OK ----------
// Holding OK (or Enter) for more, as on Android TV and in Kodi, where a long
// press brings up a context menu: in the guide, a program's actions. A
// press acts on its keyup; a hold once the key comes again (Android sends
// a held key again at its long-press timeout, a keyboard repeats) or it's
// been down HOLD_MS. A controller's button (pressKey) has no keyup, and
// acts at once.
const HOLD_MS = 500;
const hold = { timer: 0, short: null, long: null };

function holdStart(short, long) {
  clearTimeout(hold.timer);
  hold.short = short;
  hold.long = long;
  hold.timer = setTimeout(holdLong, HOLD_MS);
}

function holdLong() {
  if (!hold.timer) return;
  clearTimeout(hold.timer);
  hold.timer = 0;
  const long = hold.long;
  hold.short = null;
  hold.long = null;
  if (long) long();
}

function holdEnd(e) {
  if (e.key !== 'Enter' || !hold.timer) return;
  clearTimeout(hold.timer);
  hold.timer = 0;
  const short = hold.short;
  hold.short = null;
  hold.long = null;
  if (short) short();
}

// bootKey is a key while starting: on the connect prompt the arrows move
// between its fields and Connect (Up and Down only, while typing), and OK
// on a field puts the cursor in it.
function bootKey(e, typing) {
  const form = $('.boot-connect');
  if (form.hidden) {
    if (!typing && (e.key === 'r' || e.key === 'R')) location.reload();
    return;
  }
  const items = $$('.fx', form);
  const el = document.activeElement;
  const i = items.indexOf(el && el.closest ? el.closest('.fx') : null);
  if (e.key === 'Enter' && el && el.matches('.boot-field')) {
    e.preventDefault();
    const input = $('input', el);
    input.focus();
    input.select();
    return;
  }
  const dir = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 }[e.key];
  if (!dir || (typing && (e.key === 'ArrowLeft' || e.key === 'ArrowRight'))) return;
  e.preventDefault();
  bootField(items[i < 0 ? 0 : Math.max(0, Math.min(items.length - 1, i + dir))]);
}

// bootField moves to a field of the connect prompt: on a TV to its row, as
// a field with the cursor in it would bring up the keyboard at the next
// key; elsewhere into it, to type.
function bootField(el) {
  if (!el) return;
  const input = el.matches('.boot-field') && !androidTV ? $('input', el) : null;
  if (input) { input.focus(); input.select(); } else focusEl(el);
}

// On the Android TV app the keyboard comes up over the page (the visual
// viewport shrinks); put away with Back, it leaves its field with the
// cursor, where the next arrow would bring it up again. So the field lets
// go then, back to its row, as Esc does: a setting's text goes back to
// what it was (Enter, the keyboard's own key, keeps it).
const ime = { open: false };
if (androidTV && window.visualViewport) {
  visualViewport.addEventListener('resize', () => {
    const el = document.activeElement;
    const typing = !!(el && el.matches && el.matches(TEXT_INPUT));
    if (visualViewport.height < innerHeight * 0.9) { ime.open = typing; return; }
    if (!ime.open) return;
    ime.open = false;
    if (!typing) return;
    if (el.dataset.was !== undefined) el.value = el.dataset.was;
    const row = el.closest('.fx');
    el.blur();
    if (row) focusEl(row); else restoreFocus();
  });
}

// airwavesBack is the Android TV app's Back key: it does what Esc does and
// tells whether that went back from anything. False (live TV with nothing
// open, or still starting) lets the app close.
window.airwavesBack = () => {
  if (!state.settings || document.body.classList.contains('booting')) return false;
  const el = document.activeElement;
  const typing = !!(el && el.matches && el.matches(TEXT_INPUT));
  const open = state.dock >= 0 || typing || !!state.entry || state.view !== 'tv' || osd.open
    || !!(state.recording && state.current) || $('#banner').classList.contains('show');
  if (!open) return false;
  pressKey('Escape');
  return true;
};

// In the background (the Home key) the Android TV app's live TV stops, so
// the server's tuner is free, and tunes again on return; a recording
// pauses where it is.
document.addEventListener('visibilitychange', () => {
  if (!androidTV || !state.settings || document.body.classList.contains('booting')) return;
  if (document.hidden && state.recording) {
    video.pause();
  } else if (document.hidden && state.current) {
    state.away = true;
    state.tuneToken++;
    resetPlayer();
    wxMusic.pause();
    api().StopTV();
  } else if (!document.hidden && state.away) {
    state.away = false;
    if (!state.recording) tune(state.current, { quiet: true });
  }
});

// ---------- dock ----------
// Without letter keys (a TV remote, a controller) the dock leads to every
// view: Left from its left edge, or Up from the top of a view, moves to
// it, and on TV it's the row above the on-screen controls. Left and Right
// pick, Enter opens, Esc or Down goes back to where the focus was.
const dockButtons = () => $$('#dock button').filter((b) => b.offsetParent);

function openDock() {
  const el = document.activeElement;
  if (el && el !== document.body && !el.matches(TEXT_INPUT)) el.blur();
  state.dock = Math.max(0, dockButtons().findIndex((b) => b.dataset.view === state.view));
  renderDock();
}

function closeDock(restore = true) {
  state.dock = -1;
  renderDock();
  if (restore) restoreFocus();
}

function renderDock() {
  document.body.classList.toggle('dock-nav', state.dock >= 0);
  dockButtons().forEach((b, i) => b.classList.toggle('pick', i === state.dock));
}

// dockKey handles a key while the dock is picked; other keys leave it.
function dockKey(e) {
  const buttons = dockButtons();
  switch (e.key) {
    case 'ArrowLeft': state.dock = Math.max(0, state.dock - 1); break;
    case 'ArrowRight': state.dock = Math.min(buttons.length - 1, state.dock + 1); break;
    case 'ArrowUp': break;
    case 'Enter': case ' ': {
      const b = buttons[state.dock];
      closeDock(false);
      if (b) setView(b.dataset.view);
      e.preventDefault();
      return true;
    }
    case 'Escape': case 'ArrowDown': closeDock(); e.preventDefault(); return true;
    default: closeDock(); return false;
  }
  e.preventDefault();
  renderDock();
  return true;
}

// ---------- focus ----------
// Where the remote, a controller or the arrow keys are is always marked,
// the same way in every view (style.css, Focus). Buttons, fields and the
// weather's parts take the page's focus (.fx); the guide's grid and the
// lists of Recordings, Reception and Settings mark their pick with a class,
// while that part of the view has the focus (its data-zone). Only keys
// focus: a click leaves the focus where it was (wireMouse).
const isShown = (el) => !!el && !el.hidden && el.getClientRects().length > 0;

function focusEl(el, scroll = true) {
  if (!el) return false;
  if (document.activeElement !== el) el.focus({ preventScroll: true });
  if (scroll && el.closest('.wx-main, .r-grid, .r-list, .s-list, .a-tablewrap')) el.scrollIntoView({ block: 'nearest' });
  return document.activeElement === el;
}

// blurIn takes the focus off whatever has it inside root.
function blurIn(root) {
  const el = document.activeElement;
  if (el && el !== document.body && root.contains(el)) el.blur();
}

// neighbor is the item dir (-1 or 1) from el in items, or null past either
// end: rows of buttons stop at their ends.
function neighbor(items, el, dir) {
  const i = items.indexOf(el);
  return i < 0 ? items[0] || null : items[i + dir] || null;
}

// restoreFocus puts the focus back where it was in the view, after the dock
// or a field let it go.
function restoreFocus() {
  if (state.dock >= 0) return;
  if (state.view === 'tv' && osd.open) osdFocus(osd.last);
  else if (state.view === 'guide') guideZone(guideUI.zone);
  else if (state.view === 'recordings') recZone(recUI.zone);
  else if (state.view === 'antenna') antZone(antUI.zone);
  else if (state.view === 'weather') wxFocus();
}

// viewBack is Back in an inner part of a view (a program's or a
// recording's actions, the guide's filters): it goes to the view's main
// part, and reports whether it did.
function viewBack() {
  if (state.view === 'guide' && guideUI.zone !== 'grid') { guideZone('grid'); return true; }
  if (state.view === 'recordings' && recUI.zone === 'actions') { recZone('list'); return true; }
  return false;
}

// ---------- game controllers ----------
// A game controller does what the keyboard does: each button stands for a
// key and goes through the same handler. Indexes are the standard gamepad
// mapping (A, B, X, Y, LB, RB, LT, RT, Back, Start, L3, R3, d-pad); Guide is
// Steam's. WebKit lists a controller once a button is pressed; while one is
// connected the pads are read every frame, and not while the window is
// hidden. Pads are merged, so one controller seen twice presses once.
//
// The pads are read raw, so Steam's own menus don't hide them from the app.
// Guide opens Steam's menu, and Guide with A its quick access menu, which
// leave the window focused under gamescope: from Guide until Guide again or
// B (which closes them), the pads are Steam's. Nothing acts while the window
// is unfocused either, and a button held when the app gets the pads back
// acts only once let go and pressed again.
const PAD_KEYS = [
  ['Enter'], ['Escape'], ['i'], ['g'],
  ['PageUp'], ['PageDown'], ['ArrowLeft', true], ['ArrowRight', true],
  ['w'], [','], ['m'], [' '],
  ['ArrowUp'], ['ArrowDown'], ['ArrowLeft'], ['ArrowRight'],
];
const PAD_ARROWS = 12; // the d-pad (and the left stick) repeat when held
// A direction held longer than this stops repeating until it's let go: a
// pad whose state froze mid-press (asleep, or taken by Steam) would
// otherwise move the guide or the dock on its own.
const PAD_REPEAT_MAX = 4000;
const pad = { raf: 0, held: [], stick: -1, greeted: false, guide: false, away: false, mute: false };
const PAD_GUIDE = 16;
const PAD_B = 1;

function wirePads() {
  if (!navigator.getGamepads) return;
  window.addEventListener('gamepadconnected', (e) => {
    log('info', `controller connected: ${e.gamepad.id} (${e.gamepad.mapping || 'no'} mapping)`);
    setPadFamily(e.gamepad.id);
    if (!pad.greeted) {
      pad.greeted = true;
      toast('Controller connected', 2500);
    }
    padWatch();
  });
  window.addEventListener('gamepaddisconnected', (e) => log('info', `controller disconnected: ${e.gamepad.id}`));
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) {
      pad.mute = true;
      return padWatch();
    }
    cancelAnimationFrame(pad.raf);
    pad.raf = 0;
  });
  window.addEventListener('focus', () => {
    pad.away = false;
    pad.mute = true;
  });
  padWatch(); // pads already listed, after a reload
}

// padWatch reads the pads on the next frame, unless the window is hidden.
function padWatch() {
  if (!pad.raf && !document.hidden) pad.raf = requestAnimationFrame(padPoll);
}

function padPoll(now) {
  pad.raf = 0;
  const pads = [...navigator.getGamepads()].filter(Boolean);
  if (!pads.length) {
    pad.held = [];
    pad.stick = -1;
    return;
  }
  const down = padButtons(pads);
  const guide = pads.some((p) => p.buttons[PAD_GUIDE] && p.buttons[PAD_GUIDE].pressed);
  if (guide && !pad.guide) pad.away = !pad.away;
  else if (pad.away && pad.held[PAD_B] && !down[PAD_B]) pad.away = false;
  pad.guide = guide;
  const live = !pad.away && !pad.mute && document.hasFocus();
  pad.mute = false;
  down.forEach((on, i) => {
    const h = pad.held[i];
    if (!on) {
      pad.held[i] = null;
    } else if (!live) {
      pad.held[i] = h || { next: Infinity }; // acts once let go and pressed again
    } else if (!h) {
      pad.held[i] = { next: now + 400, since: now };
      padPress(i, false);
    } else if (i >= PAD_ARROWS && now >= h.next && now - h.since < PAD_REPEAT_MAX) {
      h.next = now + 120;
      padPress(i, true);
    }
  });
  padWatch();
}

// padButtons merges the pads' buttons, with the left stick as the d-pad: it
// points along its stronger axis once pushed past 0.6, until back under 0.35.
function padButtons(pads) {
  const down = PAD_KEYS.map(() => false);
  let x = 0;
  let y = 0;
  for (const p of pads) {
    for (let i = 0; i < down.length && i < p.buttons.length; i++) {
      if (p.buttons[i].pressed || p.buttons[i].value > 0.5) down[i] = true;
    }
    if (Math.abs(p.axes[0] || 0) > Math.abs(x)) x = p.axes[0];
    if (Math.abs(p.axes[1] || 0) > Math.abs(y)) y = p.axes[1];
  }
  const along = { 12: -y, 13: y, 14: -x, 15: x };
  if (pad.stick >= 0 && along[pad.stick] < 0.35) pad.stick = -1;
  if (pad.stick < 0 && Math.max(Math.abs(x), Math.abs(y)) > 0.6) {
    pad.stick = Math.abs(x) > Math.abs(y) ? (x < 0 ? 14 : 15) : (y < 0 ? 12 : 13);
  }
  if (pad.stick >= 0) down[pad.stick] = true;
  return down;
}

function padPress(i, repeat) {
  setPrompts(true);
  // On the TV, A plays and pauses: the guide has its own button, and the
  // remote's OK (Enter) brings up the controls. With them up, A presses.
  if (i === 0 && state.view === 'tv' && !state.entry && !osd.open && state.dock < 0) return pressKey(' ', { repeat });
  const [key, shiftKey] = PAD_KEYS[i];
  pressKey(key, { shiftKey: !!shiftKey, repeat });
}

// ---------- key and button hints ----------
// Hints name keys, or a controller's buttons once one was used (Kenney's
// CC0 Input Prompts, in vendor/kenney-prompts), until a key is pressed
// again. Glyphs by the key a button stands for (PAD_KEYS); the face buttons
// are where the standard mapping puts them, so a Switch pad's read B A Y X.
const GLYPHS = {
  xbox: ['xbox_button_a', 'xbox_button_b', 'xbox_button_x', 'xbox_button_y', 'xbox_lb', 'xbox_rb', 'xbox_lt', 'xbox_rt',
    'xbox_button_view', 'xbox_button_menu', 'xbox_ls', 'xbox_rs', 'xbox_dpad_none', 'xbox_dpad_vertical', 'xbox_dpad_horizontal'],
  playstation: ['playstation_button_cross', 'playstation_button_circle', 'playstation_button_square', 'playstation_button_triangle',
    'playstation_trigger_l1', 'playstation_trigger_r1', 'playstation_trigger_l2', 'playstation_trigger_r2', 'playstation5_button_create',
    'playstation5_button_options', 'playstation_button_l3', 'playstation_button_r3', 'playstation_dpad_none', 'playstation_dpad_vertical', 'playstation_dpad_horizontal'],
  switch: ['switch_button_b', 'switch_button_a', 'switch_button_y', 'switch_button_x', 'switch_button_l', 'switch_button_r', 'switch_button_zl',
    'switch_button_zr', 'switch_button_minus', 'switch_button_plus', 'switch_stick_l_press', 'switch_stick_r_press', 'switch_dpad_none', 'switch_dpad_vertical', 'switch_dpad_horizontal'],
  steamdeck: ['steamdeck_button_a', 'steamdeck_button_b', 'steamdeck_button_x', 'steamdeck_button_y', 'steamdeck_button_l1', 'steamdeck_button_r1',
    'steamdeck_button_l2', 'steamdeck_button_r2', 'steamdeck_button_view', 'steamdeck_button_options', 'steamdeck_stick_l_press', 'steamdeck_stick_r_press',
    'steamdeck_dpad_none', 'steamdeck_dpad_vertical', 'steamdeck_dpad_horizontal'],
};
// The key each glyph above stands for, in order.
const GLYPH_KEYS = ['Enter', 'Escape', 'i', 'g', 'PageUp', 'PageDown', 'ShiftLeft', 'ShiftRight', 'w', ',', 'm', ' ', 'Arrows', 'UpDown', 'LeftRight'];
const prompts = { pad: false, family: 'xbox', detected: 'xbox' };

// An 8BitDo pad's X and Y arrive swapped (its X, on the left, reads as the
// standard mapping's top button), so it gets Xbox glyphs with X and Y
// traded.
GLYPHS.xboxswap = GLYPHS.xbox.map((g, i) => GLYPHS.xbox[i === 2 ? 3 : i === 3 ? 2 : i]);

// PAD_LABELS name the glyph sets, for the Settings choice that overrides
// what the pad's id suggests.
const PAD_LABELS = [['', 'Auto'], ['xbox', 'Xbox'], ['xboxswap', 'Xbox, X and Y swapped'], ['switch', 'Nintendo'], ['playstation', 'PlayStation'], ['steamdeck', 'Steam Deck']];
const padLabelsKey = 'airwaves.padLabels';

function setPadFamily(id) {
  prompts.detected = /054c|sony|playstation|dualsense|dualshock/i.test(id) ? 'playstation'
    : /057e|nintendo|switch/i.test(id) ? 'switch'
      : /2dc8|8bitdo/i.test(id) ? 'xboxswap'
        : /28de|valve|steam deck/i.test(id) ? 'steamdeck' : 'xbox';
  applyPadFamily();
}

function applyPadFamily() {
  const forced = localStorage.getItem(padLabelsKey) || '';
  const family = GLYPHS[forced] ? forced : prompts.detected;
  if (family === prompts.family) return;
  prompts.family = family;
  renderHints();
}

function setPrompts(pad) {
  if (pad === prompts.pad) return;
  prompts.pad = pad;
  document.body.classList.toggle('pad-input', pad);
  renderHints();
}

// glyph is the controller button for key, or '' when there is none.
function glyph(key, label) {
  const i = GLYPH_KEYS.indexOf(key);
  if (i < 0) return '';
  const file = GLYPHS[prompts.family][i];
  return `<img class="glyph" src="vendor/kenney-prompts/${file}${/press|dpad_none/.test(file) ? '' : '_outline'}.svg" alt="${esc(label)}">`;
}

// The keys of the Android TV app's remote, as hints name them.
const REMOTE_KEYS = { Enter: 'OK', Escape: 'Back', ' ': 'Play/Pause', Arrows: 'Arrows', UpDown: 'Up, Down', LeftRight: 'Left, Right' };

// keyHint shows how to press key: its button after a controller was used,
// else its label (on the Android TV app, the remote's); null when the
// controller or the remote has no such button.
const keyHint = (key, label) => {
  if (prompts.pad) return glyph(key, label) || null;
  if (androidTV) return REMOTE_KEYS[key] ? esc(REMOTE_KEYS[key]) : null;
  return esc(label);
};

// hints joins [key, label, what] into a line of hints, leaving out what the
// controller can't do.
const hints = (items) => items.map(([key, label, what]) => {
  const k = keyHint(key, label);
  return k == null ? '' : `${k} ${what}`;
}).filter(Boolean).join('  |  ');

// renderHints redraws whatever shows keys, after the input changed.
function renderHints() {
  for (const k of $$('#dock kbd')) {
    const h = keyHint(k.dataset.key, k.dataset.label);
    k.innerHTML = h || '';
    k.hidden = h == null;
  }
  renderSettings();
  if (!state.report) return;
  if (state.view === 'guide') { guideShown = []; renderGuide(); }
  if (state.view === 'recordings') renderRecordings();
  renderBanner();
}

// ---------- guide ----------
const FILTERS = [
  ['all', 'All'],
  ['favorites', 'Favorites'],
  ['nextgen', 'NextGen'],
  ['sports', 'Sports'],
  ['movie', 'Movies'],
  ['news', 'News'],
  ['family', 'Family'],
];

// guideFilters adds a filter for each category of the lineup's own channels
// that none of FILTERS covers ("Kids", "Gaming"), in the order categories
// are listed on the admin page. Not Weather: the weather channel leads the
// guide anyway. NextGen only with a tuner that takes in ATSC 3.0.
function guideFilters() {
  const base = FILTERS.filter(([k]) => k !== 'nextgen' || nextGen());
  const keys = new Set([...base.map(([k]) => k), 'weather']);
  const extra = [];
  for (const category of Object.keys(CATEGORY_GENRES)) {
    const k = categoryGenres(category)[0];
    if (keys.has(k) || !state.lineup.some((c) => c.own && c.category === category)) continue;
    keys.add(k);
    extra.push([k, category]);
  }
  return [...base, ...extra];
}

function guideChannels() {
  const lo = state.gStart;
  const hi = state.gStart + state.gSpan * MIN;
  switch (state.guideFilter) {
    case 'all': return state.lineup;
    case 'favorites': return state.lineup.filter(isFav);
    case 'nextgen': return state.lineup.filter((c) => c.atsc3);
    default:
      return state.lineup.filter((c) => programsFor(c).some((p) => p._e > lo && p._s < hi && (p.genres || []).includes(state.guideFilter)));
  }
}

function openGuide() {
  const now = Date.now();
  state.gStart = floor30(now);
  state.gTime = now;
  guideZone('grid');
  const list = guideChannels();
  state.gRow = Math.max(0, list.findIndex((c) => state.current && c.key === state.current.key));
  renderGuide();
  scrollRowIntoView(true);
}

// The guide's parts take the focus in turn (data-zone): the grid; the
// filters above it, Up from its first row; and the selected program's
// actions in the detail (Watch, Record, Series, New only, Favorite, Hide),
// which OK on a program to come brings up, as holding OK or I does on any
// (Kodi's and Android TV's long press for more). OK on what's on now
// watches it, as in the Live TV app's guide.
const guideUI = { zone: 'grid', act: '' };

function guideZone(zone, act = '') {
  guideUI.zone = zone;
  $('#guide').dataset.zone = zone;
  if (act) guideUI.act = act;
  if (state.dock >= 0 || state.view !== 'guide') return;
  if (zone === 'grid') return blurIn($('#guide'));
  if (zone === 'filters') {
    const btns = $$('.g-filters button');
    focusEl(btns.find((b) => b.dataset.filter === state.guideFilter) || btns[0]);
    return;
  }
  const acts = guideActs();
  if (!acts.length) return guideZone('grid');
  focusEl(acts.find((b) => b.dataset.act === guideUI.act) || acts[0]);
}

const guideActs = () => $$('#guide .gd-actions .act').filter(isShown);

// guideRefocus puts the focus back on the filters or actions after they
// were drawn again.
function guideRefocus() {
  if (state.view !== 'guide' || state.dock >= 0 || guideUI.zone === 'grid') return;
  if (!$('#guide').contains(document.activeElement)) guideZone(guideUI.zone);
}

function guideOpenActions() {
  if (state.view === 'guide' && guideChannels()[state.gRow]) guideZone('actions', '-');
}

function wireGuide() {
  const actions = f($('#guide'), 'actions');
  actions.addEventListener('click', (e) => {
    const b = e.target.closest('[data-act]');
    if (!b) return;
    const list = guideChannels();
    const ch = list[state.gRow];
    switch (b.dataset.act) {
      case 'watch': return watchChannel(ch);
      case 'once': return recordSelected('once');
      case 'series': return recordSelected('series', false);
      case 'new': return recordSelected('series', true);
      case 'fav': return toggleFavorite(ch);
      case 'hide': guideZone('grid'); return hideChannel(ch);
      default: return null;
    }
  });
  actions.addEventListener('focusin', (e) => {
    const b = e.target.closest('[data-act]');
    if (b) guideUI.act = b.dataset.act;
  });
  const rows = $('.g-rows');
  rows.addEventListener('click', (e) => {
    const cell = e.target.closest('[data-row]');
    if (!cell) return;
    guideZone('grid');
    state.gRow = Number(cell.dataset.row);
    if (cell.dataset.t) state.gTime = Number(cell.dataset.t);
    renderGuide();
  });
  rows.addEventListener('dblclick', (e) => {
    const cell = e.target.closest('[data-row]');
    if (cell) guideActivate();
  });
}

function guideKey(e) {
  if (guideUI.zone === 'filters' && guideFiltersKey(e)) return;
  if (guideUI.zone === 'actions' && guideActionsKey(e)) return;
  const list = guideChannels();
  const sel = selectedProgram(list);
  switch (e.key) {
    // Down from the last row wraps to the first; Up from the first goes to
    // the filters. The dock is Left from the earliest program.
    case 'ArrowUp':
      if (state.gRow <= 0) { e.preventDefault(); return guideZone('filters'); }
      state.gRow -= 1;
      break;
    case 'ArrowDown': state.gRow = list.length ? (state.gRow + 1) % list.length : 0; break;
    case 'PageUp': state.gRow = state.gRow === 0 ? list.length - 1 : Math.max(0, state.gRow - 8); break;
    case 'PageDown': state.gRow = state.gRow === list.length - 1 ? 0 : Math.min(list.length - 1, state.gRow + 8); break;
    case 'ArrowRight':
      state.gTime = sel ? sel._e : state.gTime + 30 * MIN;
      while (state.gTime >= state.gStart + (state.gSpan - 30) * MIN) state.gStart += 30 * MIN;
      break;
    case 'ArrowLeft': {
      const min = floor30(Date.now()) - 30 * MIN;
      // From the earliest program, Left moves on to the dock.
      if (!sel || sel._s <= min) { e.preventDefault(); return openDock(); }
      state.gTime = Math.max(min, (sel ? sel._s : state.gTime) - 1);
      while (state.gTime < state.gStart && state.gStart > min) state.gStart -= 30 * MIN;
      break;
    }
    case 'Home': state.gStart = floor30(Date.now()); state.gTime = Date.now(); break;
    case 'Enter':
      e.preventDefault();
      // A key's press acts on its keyup, a hold brings up the actions; a
      // controller's A, at once.
      if (e.isTrusted) return holdStart(() => { if (state.view === 'guide' && guideUI.zone === 'grid') guideActivate(); }, guideOpenActions);
      return guideActivate();
    case 'i': case 'I': e.preventDefault(); return guideOpenActions();
    case 'r': e.preventDefault(); recordSelected('once'); return;
    case 'f': e.preventDefault(); toggleFavorite(list[state.gRow]); return;
    case 'h': case 'H': e.preventDefault(); hideChannel(list[state.gRow]); return;
    case 'R': e.preventDefault(); recordSelected('series', false); return;
    case 'n': case 'N': e.preventDefault(); recordSelected('series', true); return;
    default: {
      if (e.key === ']' || e.key === '[') {
        const filters = guideFilters();
        const i = filters.findIndex(([k]) => k === state.guideFilter);
        state.guideFilter = filters[(i + (e.key === ']' ? 1 : filters.length - 1)) % filters.length][0];
        state.gRow = 0;
        break;
      }
      return;
    }
  }
  e.preventDefault();
  renderGuide();
  scrollRowIntoView();
}

// guideFiltersKey: Left and Right pick a filter (as a tab does), Down or OK
// goes to the grid, Up (or Left from the first) to the dock.
function guideFiltersKey(e) {
  const btns = $$('.g-filters button');
  const i = Math.max(0, btns.findIndex((b) => b.dataset.filter === state.guideFilter));
  switch (e.key) {
    case 'ArrowLeft': case 'ArrowRight': {
      const j = i + (e.key === 'ArrowLeft' ? -1 : 1);
      if (j < 0) { openDock(); break; }
      if (j >= btns.length) break;
      state.guideFilter = btns[j].dataset.filter;
      state.gRow = 0;
      renderGuide();
      $('.g-rows').scrollTop = 0;
      break;
    }
    case 'ArrowUp': openDock(); break;
    case 'ArrowDown': case 'Enter': guideZone('grid'); break;
    default: return false;
  }
  e.preventDefault();
  return true;
}

// guideActionsKey: Left and Right along the actions, OK presses, Down (or
// Back) goes back to the grid, Up to the dock.
function guideActionsKey(e) {
  const acts = guideActs();
  const el = document.activeElement;
  switch (e.key) {
    case 'ArrowLeft': case 'ArrowRight': focusEl(neighbor(acts, el, e.key === 'ArrowLeft' ? -1 : 1) || (acts.includes(el) ? null : acts[0])); break;
    case 'ArrowUp': openDock(); break;
    case 'ArrowDown': guideZone('grid'); break;
    case 'Enter':
      if (acts.includes(el)) el.click(); else guideZone('actions');
      break;
    default: return false;
  }
  e.preventDefault();
  return true;
}

// guideActivate is OK on a program: what's on now (or was, a little
// before) is watched on its channel, and a program to come shows what can
// be done with it.
function guideActivate() {
  const list = guideChannels();
  const ch = list[state.gRow];
  if (!ch) return;
  const p = selectedProgram(list);
  if (!p || p._s <= Date.now()) watchChannel(ch);
  else guideOpenActions();
}

// watchChannel goes back to TV on a channel. Choosing what's already
// playing goes back to it without tuning again (and losing the rewind
// buffer); a failed channel retries.
function watchChannel(ch) {
  if (!ch) return;
  const playing = state.current && state.current.key === ch.key && !state.recording && $('#nosignal').hidden;
  if (!playing) tune(ch);
  setView('tv');
  if (playing) showBanner();
}

function selectedProgram(list) {
  const ch = list[state.gRow];
  if (!ch) return null;
  const t = Math.max(state.gTime, state.gStart);
  return airingAt(ch, t) || null;
}

function scrollRowIntoView(center) {
  const rows = $('.g-rows');
  const rowH = 58;
  const top = state.gRow * rowH;
  if (center) rows.scrollTop = top - rows.clientHeight / 2 + rowH;
  else if (top < rows.scrollTop) rows.scrollTop = top;
  else if (top + rowH > rows.scrollTop + rows.clientHeight) rows.scrollTop = top + rowH - rows.clientHeight;
}

// guideShown is what the guide's rows were last built from. They are built
// again only when that changes; moving the selection moves two classes, as
// rebuilding thousands of cells on every key is slow on a TV box.
let guideShown = [];

function renderGuide() {
  if (!state.report) return;
  const g = $('#guide');
  const list = guideChannels();
  state.gRow = Math.min(state.gRow, Math.max(0, list.length - 1));
  const rows = $('.g-rows');
  const lo = state.gStart;
  const now = Date.now();
  const shown = [state.guideFilter, lo, rows.clientWidth, Math.floor(now / MIN), state.lineup, state.guide, state.dvrKeys, state.settings, state.current && state.current.key, state.wx, state.sigStamp];
  if (shown.every((v, i) => v === guideShown[i])) return moveGuideSelection(list, lo);
  guideShown = shown;
  const chw = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--chw')) || 260;
  const width = Math.max(300, rows.clientWidth - chw);
  const ppm = width / state.gSpan;
  const hi = lo + state.gSpan * MIN;

  // filters
  const filters = guideFilters();
  if (!filters.some(([k]) => k === state.guideFilter)) state.guideFilter = 'all';
  $('.g-filters').innerHTML = filters.map(([k, label]) => {
    const saved = state.guideFilter;
    state.guideFilter = k;
    const n = guideChannels().length;
    state.guideFilter = saved;
    return `<button type="button" role="tab" aria-selected="${k === state.guideFilter}" data-filter="${k}" class="fx${k === state.guideFilter ? ' on' : ''}">${label}<span class="n">${n}</span></button>`;
  }).join('');
  $$('.g-filters button').forEach((b) => b.addEventListener('click', () => {
    state.guideFilter = b.dataset.filter;
    state.gRow = 0;
    renderGuide();
  }));

  // time header
  const d = new Date(lo);
  const today = new Date(now).toDateString() === d.toDateString();
  f(g, 'day').textContent = today ? 'Today' : d.toLocaleDateString([], { weekday: 'long' });
  let ticks = '';
  for (let t = lo; t < hi; t += 30 * MIN) ticks += `<span style="left:${(t - lo) / MIN * ppm}px">${clock(t)}</span>`;
  $('.g-ticks').innerHTML = ticks;

  // rows
  const sel = selectedProgram(list);
  let html = '';
  list.forEach((ch, r) => {
    // A channel's measured signal shows only when it explains a bad picture.
    const sig = ch.own ? null : sigOf(sigFor(ch));
    const bad = sig && trouble(sig);
    const cls = ['g-row', r === state.gRow ? 'sel' : '', state.current && ch.key === state.current.key ? 'cur' : ''].join(' ');
    html += `<div class="${cls}"><div class="g-ch" data-row="${r}">
      <div class="num${ch.number.length > 4 ? ' long' : ''}">${esc(ch.number)}</div>
      <div class="who"><div class="net">${isFav(ch) ? '<span class="fav">★</span>' : ''}${esc(ch.network || displayCall(ch))}</div><div class="call"${bad ? ` title="${esc(`${sig.label}: ${sigDetail(sig)}`)}"` : ''}>${bad ? meter(sig) : ''}<span>${esc(displayCall(ch))}</span></div></div>
      ${ch.logo ? `<div class="logo${ch.own ? ' own' : ''}" style="background-image:url('${esc(ch.logo)}')"></div>` : '<div></div>'}
    </div><div class="g-progs">`;
    const progs = programsFor(ch).filter((p) => p._e > lo && p._s < hi);
    if (!progs.length) {
      const label = ch.weather ? 'Local forecast, around the clock' : ch.custom ? ch.description || 'Nothing scheduled yet' : 'No listings';
      html += `<div class="g-cell empty${r === state.gRow ? ' sel' : ''}" data-row="${r}" style="left:2px;width:${width - 4}px"><div class="t">${label}</div></div>`;
    }
    for (const p of progs) {
      const left = Math.max(0, (p._s - lo) / MIN * ppm);
      const right = Math.min(width, (p._e - lo) / MIN * ppm);
      const w = Math.max(0, right - left - 3);
      const isSel = r === state.gRow && sel === p;
      const rec = state.dvrKeys.get(airingKey(ch, p));
      const recCls = rec ? (rec.status === 'unavailable' ? 'rec rec-x' : rec.status === 'recording' ? 'rec rec-on' : 'rec') : '';
      const c = ['g-cell', p._e <= now ? 'past' : '', p._s <= now && now < p._e ? 'now' : '', isSel ? 'sel' : '', recCls].join(' ');
      const tag = (p.flags || []).includes('Live') ? '<span class="tag">LIVE </span>' : (p.flags || []).includes('New') ? '<span class="tag">NEW </span>' : '';
      const sub = p.episodeTitle || `${clock(p._s)} to ${clock(p._e)}`;
      html += `<div class="${c}" data-row="${r}" data-t="${Math.max(p._s, lo)}" style="left:${left + 2}px;width:${w}px"><div class="t">${tag}${esc(p.title)}</div>${w > 90 ? `<div class="s">${esc(sub)}</div>` : ''}</div>`;
    }
    html += '</div></div>';
  });
  const keep = rows.scrollTop;
  rows.innerHTML = html || '<div style="padding:28px;color:var(--muted)">No channels match this filter.</div>';
  rows.scrollTop = keep;

  // now line
  const nl = $('.g-now');
  if (now >= lo && now < hi) {
    nl.style.display = '';
    nl.style.left = `${chw + (now - lo) / MIN * ppm}px`;
  } else nl.style.display = 'none';

  renderGuideDetail(list[state.gRow], sel);
  f(g, 'status').innerHTML = `${list.length} channels  |  ${guideStatusHints()}`;
  guideRefocus();
}

// guideStatusHints names the guide's keys beside its filters.
function guideStatusHints() {
  if (androidTV && !prompts.pad) return 'Up from the top row for filters';
  return hints([['i', 'I', 'more'], ['f', 'F', 'favorite'], ['h', 'H', 'hide'], ['[', '[ ]', 'filter']]);
}

// moveGuideSelection marks the selected row and program in rows already
// built, as renderGuide would.
function moveGuideSelection(list, lo) {
  const rows = $('.g-rows');
  const sel = selectedProgram(list);
  for (const el of rows.querySelectorAll('.sel')) el.classList.remove('sel');
  const row = list.length ? rows.children[state.gRow] : null;
  if (row) {
    row.classList.add('sel');
    const cell = sel ? row.querySelector(`.g-cell[data-t="${Math.max(sel._s, lo)}"]`) : row.querySelector('.g-cell.empty');
    if (cell) cell.classList.add('sel');
  }
  renderGuideDetail(list[state.gRow], sel);
}

function humanMinutes(m) {
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
}

// renderGuideActions draws the selected program's actions: Watch first for
// what's on now, the recording ones where recording applies (the
// antenna's channels, on a server with a DVR), then the channel's.
function renderGuideActions(ch, p) {
  const box = f($('#guide'), 'actions');
  if (!ch) { box.innerHTML = ''; return; }
  const now = Date.now();
  const airing = !p || p._s <= now;
  const act = (name, label, cls = '') => `<button type="button" class="act fx${cls ? ` ${cls}` : ''}" data-act="${name}">${label}</button>`;
  const parts = [];
  if (airing) parts.push(act('watch', !p || now < p._e ? 'Watch' : `Watch ${esc(ch.number)}`));
  if (state.info.dvr && p && p._e > now && !ch.weather && !ch.custom) {
    const it = state.dvrKeys.get(airingKey(ch, p));
    const rule = seriesRuleFor(ch, p);
    if (it) {
      const label = it.status === 'recording' ? 'Recording now' : it.status === 'unavailable' ? 'Channel not tuned on server' : 'Recording scheduled';
      parts.push(`<span class="chip ${it.status === 'unavailable' ? '' : 'live'}">${label}</span>`);
      if (it.id || !rule) parts.push(act('once', 'Cancel'));
    } else {
      parts.push(act('once', '<i></i>Record', 'rec'));
    }
    if (rule) {
      parts.push(`<span class="chip new">Series${rule.newOnly ? ', new only' : ''}</span>${act('series', 'Stop series')}`);
    } else if (p.seriesId) {
      parts.push(act('series', 'Record series') + act('new', 'New episodes only'));
    }
  }
  if (!airing) parts.push(act('watch', `Watch ${esc(ch.number)}`));
  parts.push(act('fav', isFav(ch) ? 'Unfavorite' : 'Favorite'));
  parts.push(act('hide', 'Hide channel'));
  box.innerHTML = parts.join('');
  guideRefocus();
}

function recordSelected(kind, newOnly) {
  const list = guideChannels();
  return recordAiring(list[state.gRow], selectedProgram(list), kind, newOnly);
}

// recordNow records what is on the channel being watched.
function recordNow(kind, newOnly) {
  const ch = state.current;
  if (state.recording || !ch) return;
  const p = airingAt(ch, Date.now());
  if (!p) return toast('No listing to record on this channel');
  return recordAiring(ch, p, kind, newOnly).then(renderBanner);
}

async function recordAiring(ch, p, kind, newOnly) {
  if (!state.info.dvr) return toast('Recording is not available on this server');
  if (!ch || !p) return;
  if (ch.weather) return toast('The weather channel is always on; nothing to record');
  if (ch.custom) return toast(`${ch.name} is made by your server; nothing to record`);
  if (p._e <= Date.now()) return toast('That has already aired');
  const it = state.dvrKeys.get(airingKey(ch, p));
  const rule = seriesRuleFor(ch, p);
  try {
    if (kind === 'once' && it) {
      if (it.id) await api().DeleteRecording(it.id);
      else if (!rule && it.ruleId) await api().DeleteRule(it.ruleId);
      toast(`Won't record ${p.title}`);
    } else if (kind === 'series' && rule) {
      await api().DeleteRule(rule.id);
      toast(`Stopped the series recording of ${p.title}`);
    } else if (kind === 'series' && !p.seriesId) {
      return toast(`${p.title} has no series information. Press R to record this airing.`);
    } else {
      await api().Record({ kind, channel: ch.number, callSign: ch.callSign, start: p.start, newOnly: !!newOnly });
      toast(kind === 'series'
        ? `Recording ${newOnly ? 'new episodes' : 'every episode'} of ${p.title} on ${ch.number}`
        : `Recording ${p.title} at ${clock(p._s)} on ${ch.number}`);
    }
  } catch (e) {
    toast(String(e && e.message ? e.message : e), 6000);
  }
  await loadDVR();
}

function renderGuideDetail(ch, p) {
  const g = $('#guide');
  if (!ch) {
    // No channels under this filter.
    for (const k of ['kicker', 'title', 'ep', 'meta', 'desc', 'actions', 'hint']) f(g, k).textContent = '';
    f(g, 'art').classList.remove('has');
    return;
  }
  const now = Date.now();
  f(g, 'kicker').innerHTML = `<span>${esc(ch.number)} ${esc(ch.own ? ch.call : displayCall(ch))}</span><span style="color:var(--muted)">${p ? `${clock(p._s)} to ${clock(p._e)}` : ''}</span>`;
  f(g, 'title').textContent = p ? p.title : ch.own ? ch.name : 'No listings';
  f(g, 'ep').textContent = episodeLine(p);
  // An own channel's category is among its programs' genres; with nothing
  // listed it shows on its own, with the channel's description.
  const genres = p ? p.genres || [] : ch.own && ch.category !== 'Other' ? [ch.category] : [];
  f(g, 'meta').innerHTML = chipsFor(p, ch) + genres.map((x) => `<span class="chip">${esc(x)}</span>`).join('');
  f(g, 'desc').textContent = p ? p.description || '' : ch.description || '';
  const live = !p || p._s <= now;
  const ok = keyHint('Enter', 'Enter');
  const when = !live ? `Starts in ${humanMinutes(Math.round((p._s - now) / MIN))}  |  ${ok} for more` : p && p._e <= now ? `Ended ${clock(p._e)}  |  ${ok} to watch ${esc(ch.number)}` : `${ok} to watch`;
  const more = !live ? '' : prompts.pad ? hints([['i', 'I', 'for more']]) : `hold ${ok} for more`;
  const rec = state.info.dvr && p && p._e > now && !ch.weather && !ch.custom && !prompts.pad && !androidTV ? '  |  R record, Shift R series, N new only' : '';
  f(g, 'hint').innerHTML = `${when}${more ? `, ${more}` : ''}${rec}`;
  renderGuideActions(ch, p);
  const art = f(g, 'art');
  if (p && p.image) { art.style.backgroundImage = `url('${p.image}')`; art.classList.add('has'); } else { art.style.backgroundImage = ''; art.classList.remove('has'); }
}

// ---------- reception ----------
// Reception is what the tuners measured, by RF channel, with what the FCC
// licenses on each; nothing in it is estimated. The rail has the tuner and
// Measure now, which has the server read every RF channel worth reading
// on a tuner nobody is using, the ones its scan found first. The table
// lists the RF channels, those found first; the radar places their
// transmitters, and the detail under it is the picked RF channel's
// readings over time and the stations on it.

// applySignal takes in what the server measured: a snapshot's antenna
// section, or /api/signal.
function applySignal(sig) {
  const was = state.antenna && state.antenna.sweep;
  state.antenna = { tuner: sig.tuner || {}, muxes: sig.muxes || [], sweep: sig.sweep || {} };
  state.byRF = new Map(state.antenna.muxes.map((m) => [m.rf, m]));
  const by = new Map((sig.channels || []).map((c) => [c.number, c]));
  for (const ch of state.antennaChans) {
    const c = by.get(ch.number);
    if (c) [ch.signal, ch.recent] = [c.signal, c.recent];
  }
  state.sigStamp++;
  const sw = state.antenna.sweep;
  if (was && was.running && !sw.running && state.view === 'antenna') {
    toast(sw.note ? `Measure now ${sw.note}` : `Measured ${sw.done} RF channels`, 4000);
  }
}

let signalAt = 0;
let signalBusy = false;

// loadSignal fetches what was measured and redraws what shows it.
async function loadSignal() {
  if (!state.info || state.info.antenna === false || signalBusy || !api().Signal) return;
  signalBusy = true;
  try {
    applySignal(await api().Signal());
  } catch (e) {
    log('warn', `signal: ${e}`);
    return;
  } finally {
    signalBusy = false;
    signalAt = Date.now();
  }
  if (state.view === 'antenna') renderAntenna();
  if (state.view === 'guide') renderGuide();
  if (state.view === 'tv' && $('#banner').classList.contains('show')) renderBanner();
}

// signalTick reads the signal again while it's on screen: every 2 s while
// Measure now runs, every 10 s in Reception or with an antenna channel's
// banner up, every 30 s in the guide.
function signalTick() {
  if (!state.info || state.info.antenna === false || document.hidden) return;
  const running = !!(state.antenna && state.antenna.sweep.running);
  const banner = state.view === 'tv' && state.current && !state.current.own && $('#banner').classList.contains('show');
  const every = running ? 2000 : state.view === 'antenna' || banner ? 10_000 : state.view === 'guide' ? 30_000 : 0;
  if (every && Date.now() - signalAt >= every - 100) loadSignal();
}

// measureNow starts Measure now on the server.
async function measureNow() {
  const sw = state.antenna && state.antenna.sweep;
  if (sw && sw.running) return toast(`Measuring: ${sw.done} of ${sw.total} RF channels`, 2000);
  try {
    const st = await api().Measure();
    if (state.antenna) state.antenna.sweep = st;
    signalAt = 0;
    renderMeasure();
  } catch (e) {
    toast(String(e && e.message ? e.message : e), 6000);
  }
  return undefined;
}

function wireAntenna() {
  const a = $('#antenna');
  f(a, 'measure').addEventListener('click', () => measureNow());
  $('.a-table tbody').addEventListener('click', (e) => {
    const tr = e.target.closest('tr[data-rf]');
    if (tr) pickRF(Number(tr.dataset.rf));
  });
  $('.radar').addEventListener('click', (e) => {
    const dot = e.target.closest('[data-rf]');
    if (dot) pickRF(Number(dot.dataset.rf));
  });
}

// Reception's parts take the focus in turn (data-zone): the RF channels'
// table, and the rail beside it, Left from the table: the tuner's card and
// Measure now. OK on an RF channel with channels watches the first.
const antUI = { zone: 'table', key: 'measure' };

function openAntenna() {
  renderAntenna();
  signalAt = 0;
  antZone(antRows().length ? 'table' : 'rail');
}

const antRail = () => $$('#antenna .rail .fx').filter(isShown);

function antZone(zone, key = '') {
  antUI.zone = zone;
  $('#antenna').dataset.zone = zone;
  if (key) antUI.key = key;
  if (state.dock >= 0 || state.view !== 'antenna') return;
  renderRFDetail(); // its OK hint is the table's
  if (zone === 'table') {
    blurIn($('#antenna'));
    scrollRF();
    return;
  }
  const items = antRail();
  focusEl(items.find((el) => el.dataset.act === antUI.key) || items[items.length - 1]);
}

function antRefocus() {
  if (state.view !== 'antenna' || state.dock >= 0 || antUI.zone !== 'rail') return;
  const el = document.activeElement;
  if (!$('#antenna').contains(el) || !isShown(el)) antZone('rail');
}

function antennaKey(e) {
  if (antUI.zone === 'rail') {
    const items = antRail();
    const el = document.activeElement;
    const i = items.indexOf(el);
    switch (e.key) {
      case 'ArrowUp': if (i > 0) focusEl(items[i - 1]); else openDock(); break;
      case 'ArrowDown': if (i < 0) antZone('rail'); else if (i < items.length - 1) focusEl(items[i + 1]); break;
      case 'ArrowRight': if (antRows().length) antZone('table'); break;
      case 'ArrowLeft': openDock(); break;
      case 'Enter':
        if (el && el.dataset.act === 'measure') measureNow();
        else if (i < 0) antZone('rail');
        break;
      default: return;
    }
    // Where the focus comes back to, from the dock.
    const now = document.activeElement;
    if (now && now.dataset.act && $('#antenna').contains(now)) antUI.key = now.dataset.act;
    e.preventDefault();
    return;
  }
  const rows = antRows();
  let i = rows.indexOf(state.aRF);
  switch (e.key) {
    case 'ArrowUp':
      if (i <= 0) { e.preventDefault(); openDock(); return; }
      i--;
      break;
    case 'ArrowDown': i = Math.min(rows.length - 1, i + 1); break;
    case 'PageDown': i = Math.min(rows.length - 1, i + 8); break;
    case 'PageUp': i = Math.max(0, i - 8); break;
    case 'ArrowLeft': e.preventDefault(); antZone('rail'); return;
    case 'Enter': {
      e.preventDefault();
      const m = state.byRF.get(state.aRF);
      const ch = m && m.channels.length && findChannel(m.channels[0]);
      if (ch) watchChannel(ch);
      return;
    }
    default: return;
  }
  e.preventDefault();
  pickRF(rows[Math.max(0, i)]);
}

// antMuxes is the RF channels to list: those the scan found (by RF), then
// those a licensed station nearby uses; others hold nothing to see.
function antMuxes() {
  const all = state.antenna ? state.antenna.muxes : [];
  const found = all.filter(muxFound);
  const rest = all.filter((m) => !muxFound(m) && m.stations.length);
  return [...found, ...rest];
}
const muxFound = (m) => m.channels.length > 0 || !!(m.scan && m.scan.lock);
const antRows = () => antMuxes().map((m) => m.rf);
const muxSig = (m) => sigOf(m && { signal: m.signal, recent: m.history && m.history.windows.length ? m.history.windows[m.history.windows.length - 1] : null });

// tunerName names the tuner from its model as Tvheadend reports it.
function tunerName(t) {
  if (!t || !t.model) return 'Tuner';
  if (/^hdhomerun/i.test(t.model)) return 'HDHomeRun';
  return t.model;
}

function renderAntenna() {
  if (!state.report || !state.info || state.info.antenna === false) return;
  const a = $('#antenna');
  const muxes = antMuxes();
  if (!muxes.some((m) => m.rf === state.aRF)) state.aRF = muxes.length ? muxes[0].rf : 0;
  renderTunerCard();
  renderMeasure();
  const found = muxes.filter(muxFound);
  const locked = found.filter((m) => muxSig(m).state === 'lock').length;
  const chans = state.lineup.filter((c) => !c.own).length;
  f(a, 'stats').innerHTML = `
    <div><dt>Channels</dt><dd>${chans}<small>on ${found.length} RF channels the scan found</small></dd></div>
    ${found.length ? `<div><dt>Locked</dt><dd>${locked}<small>of those ${found.length} at their last reading</small></dd></div>` : ''}`;
  renderRFTable(muxes);
  renderRadar(muxes);
  renderRFDetail();
  renderNextGen();
  antRefocus();
}

function renderTunerCard() {
  const t = (state.antenna && state.antenna.tuner) || state.info.tuner || {};
  const n = t.tuners || 0;
  const use = t.inUse ? `${t.inUse} of ${n} in use` : n ? `${n > 1 ? 'Both' : 'It'} free` : '';
  f($('#antenna'), 'tuner').innerHTML = `
    <div class="at-label">Tuner</div>
    <div class="at-name">${esc(tunerName(t))}</div>
    <div class="at-line">${n ? `${n} tuner${n > 1 ? 's' : ''}, ${esc((t.standards || []).join(', ') || 'ATSC')}` : 'No tuner'}</div>
    ${use ? `<div class="at-line">${esc(use)}</div>` : ''}
    ${t.scanning ? '<div class="at-line at-warn">Scanning for channels</div>' : ''}
    ${t.reason ? `<div class="at-line at-warn">${esc(t.reason)}</div>` : ''}
    ${n && !t.atsc3 ? '<div class="at-note">NextGen TV (ATSC 3.0) needs another tuner</div>' : ''}
    ${t.model ? `<div class="at-model">${esc(t.model)}</div>` : ''}`;
}

// renderMeasure shows Measure now's progress on its button: the RF
// channel being read, how many are done, the found ones first.
function renderMeasure() {
  const a = $('#antenna');
  const sw = (state.antenna && state.antenna.sweep) || {};
  const btn = f(a, 'measure');
  btn.classList.toggle('busy', !!sw.running);
  f(a, 'mlabel').textContent = sw.running ? (sw.rf ? `Measuring RF ${sw.rf}` : 'Measuring') : 'Measure now';
  f(a, 'mbar').style.width = sw.running && sw.total ? `${Math.round((sw.done / sw.total) * 100)}%` : '0';
  f(a, 'mprog').hidden = !sw.running;
  let note = 'Reads every RF channel on a free tuner, a few seconds each, the ones the scan found first';
  if (sw.running) {
    const found = sw.found || 0;
    note = sw.done < found
      ? `${sw.done} of the ${found} found by the scan, then ${sw.total - found} more nearby stations use`
      : `All ${found} found, now ${sw.done - found} of ${sw.total - found} more nearby stations use`;
  } else if (sw.finishedAt) {
    note = `Last measured ${ago(Date.parse(sw.finishedAt))}, ${sw.done} of ${sw.total} RF channels${sw.note ? `: ${sw.note}` : ''}`;
  }
  f(a, 'mnote').textContent = note;
}

function renderRFTable(muxes) {
  const rows = [];
  let shownRest = false;
  for (const m of muxes) {
    const found = muxFound(m);
    if (!found && !shownRest) {
      shownRest = true;
      rows.push('<tr class="a-sect"><td colspan="7">Not found by the scan: RF channels nearby stations use</td></tr>');
    }
    const s = muxSig(m);
    const st = muxStation(m);
    const more = m.stations.length > 1 ? `, +${m.stations.length - 1} more` : '';
    rows.push(`<tr data-rf="${m.rf}" class="${m.rf === state.aRF ? 'sel' : ''}${found ? '' : ' nf'}">
      <td class="rf"><b>${m.rf}</b><div class="sub">${m.frequencyMhz} MHz ${esc(m.band)}</div></td>
      <td>${st ? `<div class="call">${esc(st.callSign)}${st.atsc3 ? ' <span class="chip ng">3.0</span>' : ''}</div><div class="sub">${esc(st.city)}, ${st.distanceKm.toFixed(0)} km ${compass(st.bearingDeg)}${more}</div>` : '<div class="sub">No licensed station nearby</div>'}</td>
      <td class="carries">${compactCarries(m.channels).split('  ').filter(Boolean).map((g) => `<span>${esc(g)}</span>`).join(' ')}</td>
      <td class="c">${meter(s)}</td>
      <td class="r mono" style="${s.state === 'off' ? 'color:var(--red)' : ''}">${s.state === 'none' ? '' : s.state === 'off' ? 'No lock' : s.quality || 'Lock'}<div class="sub">${s.strength ? `strength ${s.strength}` : ''}</div></td>
      <td class="r mono">${s.state === 'none' || s.state === 'off' || s.errors == null ? '' : s.errors >= 1 ? `<span class="errs">${Math.round(s.errors)}/s</span>` : '0'}</td>
      <td class="r mono">${s.state === 'none' ? '' : esc(agoShort(s.at))}</td>
    </tr>`);
  }
  $('.a-table tbody').innerHTML = rows.join('') || `<tr class="a-sect"><td colspan="7">${esc((state.antenna && state.antenna.tuner.reason) || 'No RF channels yet')}</td></tr>`;
}

// muxStation is the transmitter an RF channel's channels come from, else
// the nearest station licensed on it.
function muxStation(m) {
  const tx = state.antennaChans.find((c) => c.rf === m.rf && c.transmitter);
  return (tx && m.stations.find((x) => x.callSign === tx.transmitter)) || m.stations[0] || null;
}

// pickRF picks an RF channel in the table.
function pickRF(rf) {
  state.aRF = rf;
  $$('.a-table tr[data-rf]').forEach((tr) => tr.classList.toggle('sel', Number(tr.dataset.rf) === rf));
  scrollRF();
  renderRFDetail();
  markRadar();
}

function scrollRF() {
  const tr = $(`.a-table tr[data-rf="${state.aRF}"]`);
  if (tr) tr.scrollIntoView({ block: 'nearest' });
}

// renderRFDetail is the picked RF channel: its last reading, its readings
// over time, its channels and the stations licensed on it.
function renderRFDetail() {
  const box = f($('#antenna'), 'detail');
  const m = state.byRF.get(state.aRF);
  if (!m) {
    box.innerHTML = '';
    return;
  }
  const s = muxSig(m);
  const scan = m.scan ? `${m.scan.lock ? 'Found by the scan' : 'Not found by the scan'}, ${new Date(m.scan.at).toLocaleDateString([], { month: 'short', day: 'numeric' })}` : 'Not scanned yet';
  const wins = (m.history && m.history.windows) || [];
  const bars = wins.map((w) => {
    const q = w.qualityPct ? w.qualityPct.avg : w.snrDb ? Math.min(100, w.snrDb.avg * 3) : 0;
    const lo = w.qualityPct ? w.qualityPct.min : 0;
    const st = w.lockedPct >= 100 ? 'lock' : w.lockedPct > 0 ? 'part' : 'off';
    const tip = `${new Date(w.from).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })}: ${w.samples} reading${w.samples > 1 ? 's' : ''}${w.source === 'active' ? ' while watched' : ''}, locked ${Math.round(w.lockedPct)}%${w.qualityPct ? `, quality ${w.qualityPct.min} to ${w.qualityPct.max}%` : ''}`;
    return `<i class="h-${st}" style="--h:${Math.max(4, q)}%;--lo:${lo}%" title="${esc(tip)}"></i>`;
  }).join('');
  const chans = m.channels.map((n) => {
    const c = state.antennaChans.find((x) => x.number === n);
    return `<span><b>${esc(n)}</b> ${esc(c ? displayCall(c) : '')}${c && c.network ? ` ${esc(c.network)}` : ''}</span>`;
  }).join('');
  const stations = m.stations.map((x) => `<li><b>${esc(x.callSign)}</b>${x.virtualChannel ? ` (${x.virtualChannel})` : ''} ${esc(x.city)}, ${x.distanceKm.toFixed(0)} km ${compass(x.bearingDeg)}, ${x.erpKw >= 10 ? x.erpKw.toFixed(0) : x.erpKw.toFixed(1)} kW${x.haatM ? `, ${x.haatM.toFixed(0)} m up` : ''}${x.atsc3 ? ' <span class="chip ng">ATSC 3.0</span>' : ''}</li>`).join('');
  const first = m.channels.length ? m.channels[0] : '';
  box.innerHTML = `
    <div class="ad-head"><b>RF ${m.rf}</b><span>${m.frequencyMhz} MHz ${esc(m.band)}</span><span class="ad-scan">${esc(scan)}</span></div>
    <div class="ad-sig">${meter(s, true)}<div><div class="ad-label" style="color:${s.state === 'none' ? 'var(--muted)' : s.color}">${esc(s.label)}</div><div class="ad-sub">${esc(sigDetail(s))}</div></div></div>
    ${bars ? `<div class="ad-hist" aria-label="Readings over time">${bars}</div><div class="ad-axis"><span>${esc(ago(Date.parse(wins[0].from)))}</span><span>${esc(ago(Date.parse(wins[wins.length - 1].to)))}</span></div>` : ''}
    ${chans ? `<div class="ad-chans">${chans}</div>` : ''}
    ${stations ? `<ul class="ad-stations">${stations}</ul>` : ''}
    ${first && antUI.zone === 'table' ? `<div class="ad-hint">${hints([['Enter', 'Enter', `watches ${first}`]])}</div>` : ''}`;
}

// compactCarries turns ["4.1","4.2","2.1 (3.0)"] into "4.1-2  3.0: 2.1".
function compactCarries(list) {
  const fold = (items) => {
    const majors = new Map();
    for (const c of items) {
      const [maj, min] = c.split('.');
      if (!majors.has(maj)) majors.set(maj, []);
      majors.get(maj).push(Number(min || 0));
    }
    return [...majors].map(([maj, mins]) => (mins.length > 1 ? `${maj}.${Math.min(...mins)}-${Math.max(...mins)}` : `${maj}${mins[0] ? '.' + mins[0] : ''}`)).join('  ');
  };
  const v1 = list.filter((c) => !c.endsWith('(3.0)'));
  const v3 = list.filter((c) => c.endsWith('(3.0)')).map((c) => c.replace(' (3.0)', ''));
  return [fold(v1), v3.length ? `3.0: ${fold(v3)}` : ''].filter(Boolean).join('  ');
}

const svgEl = (tag, attrs, text) => {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v);
  if (text != null) el.textContent = text;
  return el;
};

// renderRadar places the listed RF channels' transmitters around home, by
// tower site, colored by what was measured on their RF channels.
function renderRadar(muxes) {
  const svg = $('.radar');
  svg.innerHTML = '';
  const R = 200;
  const maxKm = state.report.radiusKm || 160;
  const rad = (km) => R * Math.sqrt(Math.min(km, maxKm) / maxKm);
  const pos = (km, deg) => {
    const a = (deg - 90) * Math.PI / 180;
    return [rad(km) * Math.cos(a), rad(km) * Math.sin(a)];
  };
  for (const km of [25, 50, 100, maxKm]) {
    svg.append(svgEl('circle', { class: `ring${km === maxKm ? ' edge' : ''}`, r: rad(km) }));
    svg.append(svgEl('text', { x: 3, y: -rad(km) - 3 }, `${km} km`));
  }
  svg.append(svgEl('line', { class: 'axis', x1: -R, y1: 0, x2: R, y2: 0 }));
  svg.append(svgEl('line', { class: 'axis', x1: 0, y1: -R, x2: 0, y2: R }));
  for (const [t, x, y] of [['N', 0, -R - 8], ['E', R + 10, 4], ['S', 0, R + 16], ['W', -R - 10, 4]]) {
    svg.append(svgEl('text', { class: 'card', x, y, 'text-anchor': 'middle' }, t));
  }
  // Tower sites: the stations on the listed RF channels, by place.
  const rank = { lock: 3, part: 2, off: 1, none: 0 };
  const sites = new Map();
  for (const m of muxes) {
    const sig = muxSig(m);
    for (const x of m.stations) {
      const full = state.bySite.get(`${x.facilityId}:${m.rf}`);
      if (!full || !full.point) continue;
      const k = `${full.point.lat.toFixed(3)},${full.point.lon.toFixed(3)}`;
      const site = sites.get(k) || { x, rfs: [], calls: [], state: 'none', found: 0 };
      site.rfs.push(m.rf);
      site.calls.push(x.callSign);
      if (rank[sig.state] > rank[site.state]) site.state = sig.state;
      if (muxFound(m)) site.found++;
      sites.set(k, site);
    }
  }
  const labelled = new Map();
  for (const site of sites.values()) {
    if (site.found < 2) continue;
    const city = site.x.city;
    if (!labelled.has(city) || labelled.get(city).found < site.found) labelled.set(city, site);
  }
  const ordered = [...sites.values()].sort((a, b) => rank[a.state] - rank[b.state]);
  const labels = [];
  for (const site of ordered) {
    const [x, y] = pos(site.x.distanceKm, site.x.bearingDeg);
    const r = 3 + Math.min(7, Math.sqrt(site.rfs.length) * 2);
    const dot = svgEl('circle', { class: `site-dot s-${site.state}`, cx: x, cy: y, r, 'data-rf': site.rfs[0] });
    dot.dataset.rfs = site.rfs.join(' ');
    dot.append(svgEl('title', {}, `${site.calls.join(', ')}\n${site.x.city}, ${site.x.distanceKm.toFixed(0)} km ${compass(site.x.bearingDeg)}, RF ${site.rfs.join(', ')}`));
    svg.append(dot);
    if ([...labelled.values()].includes(site)) labels.push({ x, y, r, text: `${site.x.city} ${site.found}` });
  }
  const placed = [];
  for (const l of labels.sort((a, b) => a.text.length - b.text.length)) {
    const left = l.x < 0;
    const w = l.text.length * 7.8;
    const x0 = left ? l.x - l.r - 5 - w : l.x + l.r + 5;
    const box = { x0, x1: x0 + w, y0: l.y - 9, y1: l.y + 6 };
    if (box.x0 < -224 || box.x1 > 224 || placed.some((b) => box.x0 < b.x1 && b.x0 < box.x1 && box.y0 < b.y1 && b.y0 < box.y1)) continue;
    placed.push(box);
    svg.append(svgEl('text', { class: 'site', x: left ? l.x - l.r - 5 : l.x + l.r + 5, y: l.y + 4, 'text-anchor': left ? 'end' : 'start' }, l.text));
  }
  svg.append(svgEl('circle', { class: 'home', r: 4 }));
  svg.append(svgEl('line', { class: 'ray', id: 'ray', x1: 0, y1: 0, x2: 0, y2: 0 }));
  markRadar();
}

// markRadar marks the picked RF channel's transmitter, with a ray to it.
function markRadar() {
  $$('.radar .site-dot').forEach((d) => d.classList.toggle('sel', (d.dataset.rfs || '').split(' ').includes(String(state.aRF))));
  const ray = $('#ray');
  const m = state.byRF.get(state.aRF);
  const st = m && muxStation(m);
  if (!ray) return;
  if (!st) {
    ray.setAttribute('x2', 0);
    ray.setAttribute('y2', 0);
    return;
  }
  const maxKm = state.report.radiusKm || 160;
  const rr = 200 * Math.sqrt(Math.min(st.distanceKm, maxKm) / maxKm);
  const a = (st.bearingDeg - 90) * Math.PI / 180;
  ray.setAttribute('x2', rr * Math.cos(a));
  ray.setAttribute('y2', rr * Math.sin(a));
}

// renderNextGen lists the stations broadcasting ATSC 3.0 here, as facts
// about them: what this tuner can't take in.
function renderNextGen() {
  const box = f($('#antenna'), 'nextgen');
  const hosts = state.report.atsc3 || [];
  if (!hosts.length) {
    box.innerHTML = '';
    return;
  }
  const items = hosts.map((h) => {
    const svcs = (h.services || []).map((v) => `<span><i>${esc(v.display.replace('-', '.').replace(/^0/, ''))}</i> ${esc(v.network || v.name)}</span>`).join(' ');
    return `<div class="ng-host"><b>${esc(h.callSign)}</b> <em>RF ${esc(h.rf)}</em> ${svcs}</div>`;
  }).join('');
  box.innerHTML = `<div class="ng-label">NextGen TV (ATSC 3.0) on the air here${nextGen() ? '' : '<small>This tuner takes in ATSC 1.0 only</small>'}</div><div class="ng-hosts">${items}</div>`;
}

// ---------- settings ----------
// Settings is one list of rows by section, for a remote or a controller
// first (a mouse and a keyboard work too): Up and Down move, Left and Right
// change a choice or a switch, Enter switches, runs or opens a list (hidden
// channels, the controls, data sources), Esc goes back. Up from the first
// row, or Left on a row with nothing to change, moves to the dock. Changes
// are saved as they're made. The server's address and token need a
// keyboard: Enter on their row puts the cursor in them.
const settingsUI = { at: 0, sub: null, from: 0, rows: [], sections: [], pending: {}, timers: {}, tested: '' };

const AUDIO_LANGS = [['', 'As broadcast'], ['en', 'English'], ['es', 'Spanish'], ['fr', 'French'], ['ko', 'Korean'],
  ['vi', 'Vietnamese'], ['zh', 'Chinese'], ['ru', 'Russian'], ['pt', 'Portuguese']];
const GUIDE_HOURS = [6, 12, 24, 36, 48, 72];
const WATCHED = [[0, 'Keep them'], [1, 'Delete after a day'], [7, 'Delete after a week'], [30, 'Delete after a month']];
const SETTINGS_LISTS = { hidden: 'Hidden channels', controls: 'Remote, controller and keys', sources: 'Data sources' };
// How to hide a channel, with the keys there are.
const HIDE_HOW = () => (androidTV && !prompts.pad ? 'In the guide, hold OK on a channel\'s program: Hide channel' : 'In the guide, H hides a channel');

// padNames names face buttons ("A, Y") as the pad in use labels them, by
// their place in the standard mapping.
const FACES = {
  xbox: ['A', 'B', 'X', 'Y'], xboxswap: ['A', 'B', 'Y', 'X'], switch: ['B', 'A', 'Y', 'X'],
  playstation: ['Cross', 'Circle', 'Square', 'Triangle'], steamdeck: ['A', 'B', 'X', 'Y'],
};
const padNames = (names) => names.replace(/\b[ABXY]\b/g, (n) => (FACES[prompts.family] || FACES.xbox)['ABXY'.indexOf(n)]);

// CONTROLS is the legend of the controls list: glyph keys, the controller's
// buttons by name (for a keyboard user), the keys, and what they do.
const CONTROLS = [
  ['Arrows', 'D-pad, left stick', 'Arrows', 'Move. On TV, Up and Down change channel, Left and Right go back 10 s or ahead 30 s and bring up the controls'],
  ['Enter', 'A', 'Enter', 'Choose. On TV, Enter (the remote\'s OK) brings up the controls, Guide first, and A plays and pauses'],
  ['Escape', 'B', 'Esc, Back', 'Back, and puts the controls away'],
  ['g', 'Y', 'G', 'Guide'],
  ['i', 'X', 'I', 'Program info, twice for the measured signal. In the guide, more for a program: record, favorite, hide'],
  ['PageUp PageDown', 'LB, RB', 'Page Up, Page Down', 'Channel down or up, a page of the guide or of Settings'],
  ['ShiftLeft ShiftRight', 'LT, RT', 'Shift Left, Shift Right', 'Back or ahead a minute'],
  ['Space', 'R3', 'Space, Play', 'Pause'],
  ['m', 'L3', 'M', 'Mute'],
  [',', 'Start', ',', 'Settings'],
  ['w', 'Back', 'W', 'Weather'],
];

function wireSettings() {
  const v = $('#settings');
  const list = f(v, 'list');
  list.addEventListener('click', (e) => {
    const el = e.target.closest('.s-row');
    if (!el) return;
    const i = Number(el.dataset.i);
    if (i !== settingsUI.at) moveSetting(i, false);
    if (e.target.closest('input')) return;
    const r = settingsUI.rows[i];
    const step = e.target.closest('[data-step]');
    if (r.kind === 'choice') stepSetting(r, step ? Number(step.dataset.step) : 1, !step);
    else activateSetting(r);
  });
  // The text rows: Enter keeps what was typed, Esc puts it back.
  list.addEventListener('keydown', (e) => {
    const input = e.target.closest('input');
    if (!input) return;
    if (e.key === 'Enter') { e.preventDefault(); input.blur(); }
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      input.value = input.dataset.was;
      input.blur();
    }
  });
  list.addEventListener('change', (e) => {
    if (e.target.name === 'server' || e.target.name === 'token') saveServer(e.target.name, e.target.value);
  });
  list.addEventListener('focusout', () => setTimeout(renderSettings));
  f(v, 'index').addEventListener('click', (e) => {
    const b = e.target.closest('[data-section]');
    if (!b) return;
    if (settingsUI.sub) closeSettingsList();
    moveSetting(settingsUI.rows.findIndex((r) => r.section === b.dataset.section));
  });
}

// openSettings shows the list from the top level, where it was left.
function openSettings() {
  settingsUI.sub = null;
  renderSettings();
  moveSetting(settingsUI.at);
}

const settingsValue = (r) => (r.id in settingsUI.pending ? settingsUI.pending[r.id] : r.get());

// settingsRows lists what can be set, by section. A row is a choice (Left
// and Right), a switch, an action, a list it opens, text, or information.
function settingsRows() {
  const s = state.settings;
  const info = state.info || {};
  const rows = [];
  const add = (section, row) => rows.push({ section, ...row });

  add('Display', {
    id: 'scale', kind: 'choice', label: 'Interface size', sub: 'Larger reads better across a room',
    choices: SCALES.map((x) => [x, x ? `${x}%` : `Auto, ${autoScale()}%`]), get: () => s.scale || 0, set: setScale,
  });
  const detected = PAD_LABELS.find(([k]) => k === prompts.detected)[1];
  add('Display', {
    id: 'padlabels', kind: 'choice', label: 'Button labels', sub: 'How hints name a controller\'s buttons',
    choices: PAD_LABELS.map(([k, label]) => [k, k ? label : `Auto, ${detected}`]), get: () => localStorage.getItem(padLabelsKey) || '', set: setPadLabels,
  });

  const hours = (state.config && state.config.guideHours) || 24;
  add('Watching', {
    id: 'guideHours', kind: 'choice', delay: true, label: 'Listings', sub: 'How far ahead the guide goes',
    choices: [...new Set([...GUIDE_HOURS, hours])].sort((a, b) => a - b).map((h) => [h, `${h} hours`]), get: () => hours, set: setGuideHours,
  });
  add('Watching', {
    id: 'captions', kind: 'switch', label: 'Captions', sub: 'Programs that aren\'t in English start with them on',
    get: () => !!s.captions, set: setCaptionsDefault,
  });
  const lang = s.audioLang || '';
  add('Watching', {
    id: 'audioLang', kind: 'choice', label: 'Audio language', sub: 'When a channel has more than one',
    choices: AUDIO_LANGS.some(([k]) => k === lang) ? AUDIO_LANGS : [...AUDIO_LANGS, [lang, lang.toUpperCase()]], get: () => lang, set: setAudioLang,
  });

  if (info.dvr) {
    add('Recordings', {
      id: 'watched', kind: 'choice', delay: true, label: 'Watched recordings', sub: 'Unwatched ones are always kept',
      choices: WATCHED, get: () => (state.dvr && state.dvr.prefs && state.dvr.prefs.deleteWatchedAfterDays) || 0, set: setWatched,
    });
  }

  const hidden = (s.hidden || []).length;
  add('Channels', { id: 'hidden', kind: 'open', label: 'Hidden channels', sub: HIDE_HOW(), value: hidden ? `${hidden} hidden` : 'None', open: 'hidden' });
  add('Channels', { id: 'hideshop', kind: 'action', verb: 'hides them', label: 'Hide shopping channels', sub: 'QVC, HSN, Jewelry TV and the like', value: 'Hide', run: hideShopping });

  // In a browser the server is the one the page came from (web.js).
  const typeIt = androidTV ? 'OK brings up the keyboard' : 'Type it with a keyboard';
  if (state.boot && state.boot.fixedServer) add('Server', { id: 'server', kind: 'info', label: 'Address', sub: 'Where this page comes from', value: s.server || '' });
  else add('Server', { id: 'server', kind: 'text', label: 'Address', sub: typeIt, name: 'server', text: s.server || '', placeholder: 'nas.local' });
  add('Server', { id: 'token', kind: 'text', label: 'Token', sub: `If the server needs one. ${typeIt}`, name: 'token', text: s.token || '', password: true });
  add('Server', {
    id: 'test', kind: 'action', verb: 'tests it', label: 'Test connection',
    value: settingsUI.tested || (info.name ? `Connected to ${info.name}` : 'Test'), run: testServer,
  });
  add('Server', { id: 'refresh', kind: 'action', verb: 'refreshes', label: 'Refresh all data', sub: 'Transmitters and listings, fetched again', value: 'Refresh', run: refreshAll });

  add('About', { id: 'app', kind: 'info', label: 'Airwaves', value: state.boot && state.boot.version ? `Version ${state.boot.version}` : '' });
  if (info.name) {
    add('About', { id: 'server-info', kind: 'info', label: 'Server', value: info.name, sub: [info.version && `Version ${info.version}`, s.server].filter(Boolean).join(', ') });
  }
  if (info.antenna !== false && info.tuner) {
    const t = info.tuner;
    const what = t.tuners ? `${t.tuners} tuner${t.tuners > 1 ? 's' : ''}, ${(t.standards || []).join(', ')}` : 'No tuner yet';
    add('About', { id: 'tuner', kind: 'info', label: 'Tuner', value: t.tuners ? tunerName(t) : what, sub: [t.tuners && what, t.model, t.tuners && !t.atsc3 && 'NextGen TV (ATSC 3.0) needs another tuner'].filter(Boolean).join(', ') });
  }
  add('About', { id: 'controls', kind: 'open', label: SETTINGS_LISTS.controls, open: 'controls' });
  if (state.report && state.report.sources.length) add('About', { id: 'sources', kind: 'open', label: SETTINGS_LISTS.sources, value: `${state.report.sources.length}`, open: 'sources' });
  for (const [i, w] of ((state.report && state.report.warnings) || []).entries()) add('About', { id: `warning${i}`, kind: 'info', label: 'Note', sub: w });
  return rows;
}

// settingsList is the rows of a list opened from a row.
function settingsList(name) {
  const title = SETTINGS_LISTS[name];
  const rows = [];
  const add = (row) => rows.push({ section: title, ...row });
  if (name === 'hidden') {
    const known = [...state.custom, ...state.antennaChans];
    const hidden = (state.settings.hidden || []).map((k) => known.find((c) => c.key === k) || unlistedChannel(k));
    if (hidden.length > 1) add({ id: 'all', kind: 'action', verb: 'shows them', label: 'All of them', value: 'Show', run: () => showHidden(hidden.map((c) => c.key)) });
    for (const c of hidden) {
      add({ id: c.key, kind: 'action', verb: 'shows it', label: `${c.number} ${c.network || displayCall(c)}`.trim(), value: 'Show', run: () => showHidden([c.key]) });
    }
    if (!hidden.length) add({ id: 'none', kind: 'info', label: 'No hidden channels', sub: HIDE_HOW() });
  }
  if (name === 'controls') {
    for (const [keys, pad, label, what] of CONTROLS) {
      add({ id: label, kind: 'info', label: what, html: `<span class="s-glyphs" title="${esc(padNames(pad))}">${keys.split(' ').map((k) => glyph(k === 'Space' ? ' ' : k, pad)).join('')}</span><kbd>${esc(label)}</kbd>` });
    }
  }
  if (name === 'sources') {
    for (const x of (state.report && state.report.sources) || []) {
      add({ id: x.url, kind: 'action', verb: 'opens it', label: x.name, sub: x.use, value: 'Open', run: () => api().OpenURL(x.url) });
    }
  }
  return rows;
}

function renderSettings() {
  if (!state.settings) return;
  const v = $('#settings');
  const list = f(v, 'list');
  // Not under the cursor while something is typed.
  if (list.contains(document.activeElement) && document.activeElement.matches('input')) return;
  if (state.view !== 'settings') settingsUI.sub = null;
  const main = settingsRows();
  settingsUI.sections = [...new Set(main.map((r) => r.section))];
  const rows = settingsUI.rows = settingsUI.sub ? settingsList(settingsUI.sub) : main;
  settingsUI.at = Math.min(settingsUI.at, Math.max(0, rows.length - 1));
  let html = '';
  let section = '';
  rows.forEach((r, i) => {
    if (r.section !== section) {
      html += `${section ? '</div>' : ''}<h2 class="s-head">${esc(r.section)}</h2><div class="s-group">`;
      section = r.section;
    }
    html += settingsRowHTML(r, i);
  });
  if (section) html += '</div>';
  if (settingsUI.sub === 'controls' && androidTV) {
    html += '<p class="s-note">On the remote: OK brings up the controls over the picture, with Guide first, so OK twice opens the guide. In them Left and Right move along a row, Up and Down between rows: the timeline (Left and Right skip), the buttons (play, captions, record, last channel and more), and the views at the top. On live TV Up and Down change channel, and Left and Right skip. In the guide OK watches what\'s on, and holding OK offers more: record, series, favorite, hide. Back goes back. Remotes with them: digits tune a channel, Channel up and down change channel, Play/Pause pauses.</p>';
  } else if (settingsUI.sub === 'controls') {
    html += '<p class="s-note">Enter on TV brings up the controls, with the guide, captions, recording, the last channel and the views; Esc puts them away. Digits tune a channel. The remote\'s Fast forward and Rewind skip, Record records. Keyboard only: L last channel, C captions, V audio track, F favorite, R record, D recordings, A reception, Shift F full screen. In the guide hold Enter, or press I, for a program\'s actions. In Steam, give Airwaves the Gamepad controller layout, not a keyboard one.</p>';
  }
  const keep = list.scrollTop;
  list.innerHTML = html;
  list.scrollTop = keep;
  renderSettingsIndex();
  renderSettingsHint();
}

function settingsRowHTML(r, i) {
  let value = '';
  let sub = r.sub || '';
  if (r.kind === 'choice') {
    const x = settingsValue(r);
    const at = r.choices.findIndex(([k]) => k === x);
    if (r.subFor) sub = r.subFor(x);
    value = `<i class="s-step${at <= 0 ? ' end' : ''}" data-step="-1">‹</i><span class="s-choice">${esc(at >= 0 ? r.choices[at][1] : x)}</span><i class="s-step${at >= r.choices.length - 1 ? ' end' : ''}" data-step="1">›</i>`;
  } else if (r.kind === 'switch') {
    const on = settingsValue(r);
    value = `<span>${on ? 'On' : 'Off'}</span><span class="s-switch${on ? ' on' : ''}"><i></i></span>`;
  } else if (r.kind === 'text') {
    value = `<input name="${r.name}" type="${r.password ? 'password' : 'text'}" value="${esc(r.text)}" data-was="${esc(r.text)}" placeholder="${esc(r.placeholder || '')}" autocapitalize="off" autocomplete="off" spellcheck="false">`;
  } else {
    value = `${r.html || `<span>${esc(r.value || '')}</span>`}${r.kind === 'open' ? '<i class="s-more">›</i>' : ''}`;
  }
  return `<div class="s-row k-${r.kind}${i === settingsUI.at ? ' focus' : ''}" data-i="${i}" role="listitem">
    <div class="s-label"><b>${esc(r.label)}</b>${sub ? `<small>${esc(sub)}</small>` : ''}</div><div class="s-value">${value}</div></div>`;
}

// renderSettingsIndex lists the sections beside the rows, marking where the
// focus is. It follows the focus; the arrows never land on it.
function renderSettingsIndex() {
  const r = settingsUI.sub ? null : settingsUI.rows[settingsUI.at];
  const cur = r ? r.section : (settingsUI.from >= 0 && settingsRows()[settingsUI.from] || {}).section;
  f($('#settings'), 'index').innerHTML = settingsUI.sections.map((s) => `<button type="button" class="rm-item ${s === cur ? 'on' : ''}" data-section="${esc(s)}" tabindex="-1">
    <span class="rm-label">${esc(s)}</span></button>`).join('');
}

function renderSettingsHint() {
  const r = settingsUI.rows[settingsUI.at];
  const top = settingsUI.at === 0 && !settingsUI.sub;
  const items = [['UpDown', 'Up, Down', top ? 'move, Up to the menu' : 'move']];
  if (r && (r.kind === 'choice' || r.kind === 'switch')) items.push(['LeftRight', 'Left, Right', 'change']);
  if (r && r.kind === 'switch') items.push(['Enter', 'Enter', 'switches']);
  if (r && r.kind === 'open') items.push(['Enter', 'Enter', 'opens']);
  if (r && r.kind === 'action') items.push(['Enter', 'Enter', r.verb || 'runs']);
  if (r && r.kind === 'text') items.push(['Enter', 'Enter', androidTV ? 'types in it' : 'types in it, with a keyboard']);
  items.push(['Escape', 'Esc', settingsUI.sub ? 'back' : 'leaves Settings']);
  f($('#settings'), 'hint').innerHTML = hints(items);
}

function moveSetting(i, scroll = true) {
  const rows = settingsUI.rows;
  if (!rows.length) return;
  settingsUI.at = Math.max(0, Math.min(rows.length - 1, i));
  const list = f($('#settings'), 'list');
  for (const el of list.querySelectorAll('.s-row.focus')) el.classList.remove('focus');
  const el = list.querySelector(`.s-row[data-i="${settingsUI.at}"]`);
  if (el) {
    el.classList.add('focus');
    if (scroll && settingsUI.at === 0) list.scrollTop = 0;
    else if (scroll) el.scrollIntoView({ block: 'nearest' });
  }
  renderSettingsIndex();
  renderSettingsHint();
}

// settingsKey handles a key in Settings, or returns false to leave it to
// the keys every view has (Esc at the top level goes back to TV).
function settingsKey(e) {
  const r = settingsUI.rows[settingsUI.at];
  switch (e.key) {
    case 'ArrowUp':
      if (settingsUI.at > 0) moveSetting(settingsUI.at - 1);
      else if (!settingsUI.sub) openDock();
      break;
    case 'ArrowDown': moveSetting(settingsUI.at + 1); break;
    case 'PageUp': case 'PageDown': moveSetting(settingsPage(e.key === 'PageDown' ? 1 : -1)); break;
    case 'ArrowLeft':
      if (r && r.kind === 'choice') stepSetting(r, -1);
      else if (r && r.kind === 'switch') { if (settingsValue(r)) changeSetting(r, false); }
      else if (settingsUI.sub) closeSettingsList();
      else openDock();
      break;
    case 'ArrowRight':
      if (r && r.kind === 'choice') stepSetting(r, 1);
      else if (r && r.kind === 'switch') { if (!settingsValue(r)) changeSetting(r, true); }
      else if (r && r.kind === 'open') activateSetting(r);
      break;
    case 'Enter':
      if (r && r.kind === 'choice') stepSetting(r, 1, true);
      else if (r) activateSetting(r);
      break;
    case 'Escape':
      if (!settingsUI.sub) return false;
      closeSettingsList();
      break;
    default: return false;
  }
  e.preventDefault();
  return true;
}

// settingsPage is where Page Up or Down goes: the next section, or the
// start of this one (the one before when already there); in a list, eight
// rows on.
function settingsPage(dir) {
  const rows = settingsUI.rows;
  const at = settingsUI.at;
  if (settingsUI.sub) return at + dir * 8;
  const cur = rows[at].section;
  if (dir > 0) {
    const i = rows.findIndex((r, j) => j > at && r.section !== cur);
    return i < 0 ? rows.length - 1 : i;
  }
  let i = at;
  while (i > 0 && rows[i - 1].section === cur) i--;
  if (i < at || i === 0) return i;
  const prev = rows[i - 1].section;
  while (i > 0 && rows[i - 1].section === prev) i--;
  return i;
}

function stepSetting(r, dir, wrap = false) {
  const x = settingsValue(r);
  const n = r.choices.length;
  let i = r.choices.findIndex(([k]) => k === x);
  i = wrap ? (i + dir + n) % n : Math.max(0, Math.min(n - 1, i + dir));
  if (r.choices[i][0] !== x) changeSetting(r, r.choices[i][0]);
}

// changeSetting shows the new value at once and saves it; a row with delay
// (a rescan or a call to the server) saves once the value rests.
function changeSetting(r, value) {
  settingsUI.pending[r.id] = value;
  renderSettings();
  clearTimeout(settingsUI.timers[r.id]);
  const save = async () => {
    try {
      await r.set(value);
    } catch (err) {
      toast(String(err && err.message ? err.message : err), 6000);
    }
    if (settingsUI.pending[r.id] === value) delete settingsUI.pending[r.id];
    renderSettings();
  };
  if (r.delay) settingsUI.timers[r.id] = setTimeout(save, 900);
  else save();
}

function activateSetting(r) {
  if (r.kind === 'switch') return changeSetting(r, !settingsValue(r));
  if (r.kind === 'open') return openSettingsList(r.open);
  if (r.kind === 'text') {
    const input = f($('#settings'), 'list').querySelector(`input[name="${r.name}"]`);
    if (input) { input.focus(); input.select(); }
    return null;
  }
  if (r.run) return Promise.resolve(r.run()).catch((err) => toast(String(err && err.message ? err.message : err), 6000));
  return null;
}

function openSettingsList(name) {
  settingsUI.from = settingsUI.at;
  settingsUI.sub = name;
  settingsUI.at = 0;
  renderSettings();
  f($('#settings'), 'list').scrollTop = 0;
}

function closeSettingsList() {
  settingsUI.sub = null;
  settingsUI.at = settingsUI.from;
  renderSettings();
  moveSetting(settingsUI.at);
}

// ---------- what the settings rows do ----------

async function setGuideHours(hours) {
  toast('Updating the listings...', 30000);
  state.config = await api().SetConfig({ ...state.config, guideHours: hours });
  await scan(false);
  renderAll();
  toast(`${hours} hours of listings`, 2000);
}

async function setCaptionsDefault(on) {
  await saveAppSettings({ ...state.settings, captions: on });
  applyCaptions();
  toast(on ? 'Captions on' : 'Captions off', 1800);
}

async function setAudioLang(lang) {
  await saveAppSettings({ ...state.settings, audioLang: lang });
  applyAudio();
  toast(`Audio: ${(AUDIO_LANGS.find(([k]) => k === lang) || [lang, lang.toUpperCase()])[1]}`, 1800);
}

async function setWatched(days) {
  const prefs = await api().SetDVRPrefs({ deleteWatchedAfterDays: days });
  if (state.dvr) state.dvr.prefs = prefs;
  toast(prefs.deleteWatchedAfterDays ? `Watched recordings are deleted after ${prefs.deleteWatchedAfterDays} day${prefs.deleteWatchedAfterDays > 1 ? 's' : ''}` : 'Watched recordings are kept', 2500);
}

async function hideShopping() {
  if (!state.report) return;
  // By the listings' network, or the broadcast's own name ("ShopLC").
  const shop = state.antennaChans.filter((c) => SHOPPING.test(c.network || '') || SHOPPING.test(c.name || '')).map((c) => c.key);
  const was = new Set(state.settings.hidden || []);
  const added = shop.filter((k) => !was.has(k)).length;
  if (!added) return toast(shop.length ? 'Shopping channels are hidden already' : 'No shopping channels to hide', 2500);
  await saveAppSettings({ ...state.settings, hidden: [...was, ...shop.filter((k) => !was.has(k))] });
  buildLineup();
  renderAll();
  return toast(`Hid ${added} shopping channel${added > 1 ? 's' : ''}`);
}

async function showHidden(keys) {
  await unhide(keys);
  toast(keys.length > 1 ? 'Every channel shown again' : 'Channel shown again', 1800);
  if (!(state.settings.hidden || []).length) closeSettingsList();
}

async function testServer() {
  const s = state.settings;
  settingsUI.tested = `Connecting to ${s.server}...`;
  renderSettings();
  try {
    const info = await api().TestServer(s.server, s.token || '');
    settingsUI.tested = `Connected to ${info.name}`;
  } catch (err) {
    settingsUI.tested = `Can't reach ${s.server}`;
    toast(`Can't reach ${s.server}: ${err && err.message ? err.message : err}`, 6000);
  }
  renderSettings();
}

async function refreshAll() {
  toast('Fetching everything again...', 60000);
  await scan(true);
  renderAll();
  toast(`Updated. ${state.lineup.length} channels.`, 2500);
}

// saveServer switches to a new server address or token, after checking it
// answers; the app starts over with it.
async function saveServer(name, value) {
  const prev = state.settings;
  const next = { ...prev, [name]: name === 'server' ? value.trim() : value };
  if (next.server === prev.server && next.token === prev.token) return;
  if (!next.server) {
    toast('Enter the server address', 3000);
    return renderSettings();
  }
  toast(`Connecting to ${next.server}...`, 15000);
  const boot = await api().SaveSettings(next);
  if (boot.error) {
    applyBoot(await api().SaveSettings(prev));
    toast(`Can't reach ${next.server}: ${boot.error}`, 6000);
    return renderSettings();
  }
  location.reload();
  return null;
}

// ---------- interface size ----------
// Larger for a TV across the room. On Linux WebKitGTK zooms the page (Go
// sets it at launch, SetZoom after); elsewhere CSS zoom on the root scales
// everything together, and style.css undoes it for viewport units. Auto
// is 125% under gamescope, where the window is 1080p on a TV, else 100%.
// On the Android TV app sizes are a 1080p screen's, whatever the WebView's
// (fit, from web.js).
const SCALES = [0, 100, 115, 130, 150]; // 0 is Auto
const autoScale = () => (state.boot && state.boot.autoScale) || 100;
let pageZoom = 0;

function applyScale() {
  const fit = (state.boot && state.boot.fit) || 1;
  const z = (((state.settings && state.settings.scale) || autoScale()) / 100) * fit;
  const root = document.documentElement;
  const native = !!(state.boot && state.boot.nativeZoom);
  if (native && z !== pageZoom) {
    pageZoom = z;
    api().SetZoom(z);
  } else if (!native) {
    root.style.zoom = z === 1 ? '' : String(z);
    root.style.setProperty('--zoom', String(z));
  }
  // Larger sizes leave less room above the guide (.tight in style.css):
  // the size chosen, not the zoom, which on the Android TV app also fits a
  // 1080p screen's interface into the WebView.
  root.classList.toggle('tight', z / fit > 1 && (native ? innerHeight : innerHeight / z) < 900);
  fitWX();
  if (state.view === 'guide' && state.report) renderGuide();
}

function setPadLabels(k) {
  if (k) localStorage.setItem(padLabelsKey, k); else localStorage.removeItem(padLabelsKey);
  applyPadFamily();
  toast(`Button labels: ${PAD_LABELS.find(([x]) => x === k)[1]}`, 1800);
}

async function setScale(s) {
  await saveAppSettings({ ...state.settings, scale: s });
  toast(s ? `Interface size ${s}%` : `Interface size automatic, ${autoScale()}%`, 1800);
}

// unlistedChannel stands for a hidden channel that isn't listed now (an
// antenna channel out of range, or one of the server's turned off), by its
// key: "7.1|KMGH", "104.0|custom|Cartoons" or the weather channel's.
function unlistedChannel(k) {
  if (k === WEATHER_KEY) return { key: k, number: '', network: 'Weather channel' };
  const [number, call, ...rest] = String(k).split('|');
  return call === 'custom' ? { key: k, number, network: rest.join('|') || 'Custom channel' } : { key: k, number, callSign: call };
}

window.addEventListener('resize', applyScale);
document.addEventListener('DOMContentLoaded', init);

// ---------- recordings view ----------
function renderRecordingBanner(b, it) {
  const d = new Date(it.start);
  showChannelLogo(b, null);
  f(b, 'num').classList.remove('long');
  f(b, 'num').textContent = 'REC';
  f(b, 'call').textContent = `${it.channel} ${it.callSign || ''}`;
  f(b, 'net').textContent = d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
  b.classList.remove('warn', 'detail');
  f(b, 'time').textContent = `Recorded ${clock(d)}${it.sizeBytes ? '  |  ' + sizeLabel(it.sizeBytes) : ''}`;
  f(b, 'chips').innerHTML = '';
  f(b, 'title').textContent = it.title;
  f(b, 'ep').textContent = it.subtitle || '';
  const pos = (state.recOffset || 0) + (video.currentTime || 0);
  f(b, 'progress').style.width = it.duration ? `${Math.min(100, (pos / it.duration) * 100)}%` : '0';
  f(b, 'desc').textContent = it.description || '';
  f(b, 'next').innerHTML = hints([['LeftRight', 'Arrows', 'skip'], ...(androidTV || prompts.pad ? [] : [[' ', 'Space', 'pauses']]), ['Enter', 'Enter', 'controls'], ['Escape', 'Esc', 'returns to live TV']]);
  f(b, 'meter').innerHTML = '';
  f(b, 'tier').textContent = sizeLabel(it.sizeBytes);
  f(b, 'tier').style.color = '';
  f(b, 'tx').textContent = `On ${state.info.name}`;
  f(b, 'atsc3').textContent = '';
  f(b, 'note').textContent = '';
}

function sizeLabel(bytes) {
  if (!bytes) return '';
  return bytes > 1e9 ? `${(bytes / 1e9).toFixed(1)} GB` : `${Math.round(bytes / 1e6)} MB`;
}

const dayLabel = (t) => {
  const d = new Date(t);
  const today = new Date();
  const tomorrow = new Date(Date.now() + 86_400_000);
  if (d.toDateString() === today.toDateString()) return 'Today';
  if (d.toDateString() === tomorrow.toDateString()) return 'Tomorrow';
  return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
};

// A delete, a skip or a stopped series asks for a second press: the
// button says so (armed) until then, for a few seconds.
let pendingDelete = '';
let pendingTimer = 0;

const REC_TABS = [['library', 'Library'], ['upcoming', 'Coming up'], ['series', 'Series']];

function recLists() {
  const st = state.dvr || {};
  return {
    library: st.recorded || [],
    upcoming: st.upcoming || [],
    series: (st.rules || []).filter((r) => r.kind === 'series'),
  };
}

// The view's parts take the focus in turn (data-zone): the list, the menu
// of lists in the rail (Left from the list's left edge), and the selected
// item's actions (OK): for a recording Play (or Resume), From the start,
// Watched and Delete, above the grid; for one to come, Don't record; for a
// series, how many to keep, new episodes only, and Stop.
const recUI = { zone: 'list', act: '' };

function openRecordings() {
  const tab = state.recTab || 'library';
  renderRecordings();
  recZone(recLists()[tab].length ? 'list' : 'menu');
}

function recZone(zone, act = '') {
  recUI.zone = zone;
  $('#recordings').dataset.zone = zone;
  if (act) recUI.act = act;
  if (state.dock >= 0 || state.view !== 'recordings') return;
  if (zone === 'list') return blurIn($('#recordings'));
  if (zone === 'menu') return void focusEl($(`#recordings [data-tab="${state.recTab || 'library'}"]`));
  const acts = recActs();
  if (!acts.length) return recZone('list');
  focusEl(acts.find((b) => b.dataset.act === recUI.act) || acts[0]);
}

const recActs = () => $$('#recordings .r-panel.on .act.fx').filter(isShown);

function recRefocus() {
  if (state.view !== 'recordings' || state.dock >= 0 || recUI.zone === 'list') return;
  if (!$('#recordings').contains(document.activeElement) || !isShown(document.activeElement)) recZone(recUI.zone);
}

// armed is whether a delete-like action waits for its second press; ARMED
// says what the second does.
const armed = (act, it) => pendingDelete === `${act}:${it && it.id}`;
const ARMED = { delete: 'delete', skip: 'skip it', stop: 'stop it' };

function renderRecordings() {
  const v = $('#recordings');
  const lists = recLists();
  const tab = state.recTab || 'library';
  const sel = state.recSelBy || (state.recSelBy = { library: 0, upcoming: 0, series: 0 });
  for (const k of Object.keys(sel)) sel[k] = Math.min(sel[k], Math.max(0, lists[k].length - 1));
  const act = (name, label, it, cls = '') => `<button type="button" class="act fx${cls ? ` ${cls}` : ''}${armed(name, it) ? ' armed' : ''}" data-act="${name}">${armed(name, it) ? `${androidTV ? 'OK' : 'Press'} again to ${ARMED[name]}` : label}</button>`;

  f(v, 'menu').innerHTML = REC_TABS.map(([k, label]) => `<button type="button" class="rm-item fx ${k === tab ? 'on' : ''}" data-tab="${k}"><span class="rm-label">${label}</span><span class="rm-count">${lists[k].length}</span></button>`).join('');
  $$('.r-panel', v).forEach((p) => p.classList.toggle('on', p.dataset.panel === tab));

  // Library: the selected recording above a grid of everything recorded.
  const lib = lists.library;
  const cur = lib[sel.library];
  f(v, 'detail').innerHTML = cur ? `
    <div>
      <div class="rd-kicker">${dayLabel(cur.start)} ${clock(cur.start)}  |  ${esc(cur.channel)}  |  ${Math.round((Date.parse(cur.end) - Date.parse(cur.start)) / MIN)} min${cur.sizeBytes ? '  |  ' + sizeLabel(cur.sizeBytes) : ''}</div>
      <div class="rd-title">${esc(cur.title)}</div>
      <div class="rd-ep">${esc(cur.subtitle || '')}</div>
      <p class="rd-desc">${esc(cur.status === 'failed' ? cur.detail || 'This recording failed' : cur.description || '')}</p>
      <div class="rd-actions">${cur.status === 'failed' ? '' : act('play', resumable(cur) ? `Resume at ${mmss(cur.position)}` : 'Play', cur)}${resumable(cur) ? act('restart', 'From the start', cur) : ''}${cur.status === 'failed' ? '' : act('watched', cur.watched ? 'Mark unwatched' : 'Mark watched', cur)}${act('delete', 'Delete', cur)}</div>
      <div class="rd-hint">${recHint(cur)}</div>
    </div>
    <div class="rd-art" style="${cur.image ? `background-image:url('${esc(cur.image)}')` : ''}"></div>`
    : '<div class="rd-empty">Nothing recorded yet. In the guide, OK on a program to come records it, or every episode.</div><div></div>';
  f(v, 'library').innerHTML = lib.map((it, i) => `
    <article class="r-card ${i === sel.library ? 'sel' : ''} ${it.status === 'failed' ? 'failed' : ''} ${it.watched ? 'watched' : ''}" data-i="${i}">
      <div class="r-art" style="${it.image ? `background-image:url('${esc(it.image)}')` : ''}">${it.image ? '' : `<span>${esc(it.title)}</span>`}
        ${resumable(it) ? `<div class="r-prog"><i style="width:${Math.min(100, (it.position / it.duration) * 100)}%"></i></div>` : ''}
        ${it.watched ? '<b class="r-badge">Watched</b>' : ''}</div>
      <div class="r-meta">
        <div class="r-name">${esc(it.title)}</div>
        <div class="r-sub">${esc(it.subtitle || '')}</div>
        <div class="r-when">${dayLabel(it.start)}  |  ${esc(it.channel)}</div>
      </div>
    </article>`).join('');

  f(v, 'upcoming').innerHTML = lists.upcoming.length ? lists.upcoming.map((it, i) => `
    <li class="r-item ${it.status} ${i === sel.upcoming ? 'sel' : ''}" data-i="${i}">
      <div class="r-time">${dayLabel(it.start)}<b>${clock(it.start)}</b></div>
      <div class="r-what"><div class="r-name">${esc(it.title)}</div><div class="r-sub">${esc(it.channel)} ${esc(it.callSign ? it.callSign.replace(/(DT|LD|CD|LP|CA|D)\d*$/, '') : '')}${it.subtitle ? '  |  ' + esc(it.subtitle) : ''}</div>
        ${it.status === 'recording' ? '<span class="chip live">Recording now</span>' : it.status === 'unavailable' ? '<span class="chip">Channel not available</span>' : ''}${!it.id ? '<span class="chip">Part of a series</span>' : ''}</div>
      <div class="r-acts">${i === sel.upcoming && it.id ? act('skip', "Don't record", it) : ''}</div>
    </li>`).join('') : '<li class="r-empty">Nothing scheduled.</li>';

  f(v, 'rules').innerHTML = lists.series.length ? lists.series.map((r, i) => `
    <li class="r-item ${i === sel.series ? 'sel' : ''}" data-i="${i}">
      <div class="r-what"><div class="r-name">${esc(r.title)}</div><div class="r-sub">${esc(r.channel)}  |  ${r.newOnly ? 'New episodes' : 'Every episode'}  |  ${r.keep ? `Keep newest ${r.keep}` : 'Keep all'}</div></div>
      <div class="r-acts">${i === sel.series ? act('keep', r.keep ? `Keeps newest ${r.keep}` : 'Keeps all', r) + act('newonly', r.newOnly ? 'New episodes only' : 'Every episode', r) + act('stop', 'Stop series', r) : ''}</div>
    </li>`).join('') : '<li class="r-empty">No series recordings. In the guide, OK on a show to come, then Record series.</li>';
  if (lists.series.length && !prompts.pad && !androidTV) f(v, 'rules').insertAdjacentHTML('beforeend', '<li class="r-hint">K changes how many to keep  |  N new episodes only  |  Delete stops the series</li>');
  recRefocus();
}

function recHint(cur) {
  if (androidTV && !prompts.pad) return 'OK for the actions, Left for the other lists';
  return hints([['Enter', 'Enter', 'for the actions'], ...(resumable(cur) ? [['', 'Shift Enter', 'plays from the start']] : []), ['w', 'W', cur.watched ? 'unwatched' : 'watched'], ['', 'Delete', 'removes']]);
}

function wireRecordings() {
  const v = $('#recordings');
  f(v, 'menu').addEventListener('click', (e) => {
    const b = e.target.closest('[data-tab]');
    if (!b) return;
    state.recTab = b.dataset.tab;
    renderRecordings();
  });
  v.addEventListener('click', (e) => {
    const b = e.target.closest('[data-act]');
    if (b) return recAct(b.dataset.act);
    const item = e.target.closest('[data-i]');
    if (item && state.recSelBy) {
      state.recSelBy[state.recTab || 'library'] = Number(item.dataset.i);
      renderRecordings();
    }
    return null;
  });
  v.addEventListener('focusin', (e) => {
    const b = e.target.closest('[data-act]');
    if (b) recUI.act = b.dataset.act;
  });
  f(v, 'library').addEventListener('dblclick', (e) => {
    const card = e.target.closest('[data-i]');
    if (card) {
      const it = recLists().library[Number(card.dataset.i)];
      playRecording(it, resumable(it) ? it.position : 0);
    }
  });
}

// recAct does one of the selected item's actions.
function recAct(act) {
  const tab = state.recTab || 'library';
  const it = recLists()[tab][(state.recSelBy || {})[tab] || 0];
  if (!it) return null;
  const fail = (err) => toast(String(err && err.message ? err.message : err), 6000);
  switch (act) {
    case 'play': return playRecording(it, resumable(it) ? it.position : 0);
    case 'restart': return playRecording(it, 0);
    case 'watched': return api().MarkWatched(it.id, !it.watched).then(loadDVR).catch(fail);
    case 'keep': case 'newonly': {
      const steps = [0, 3, 5, 10];
      const u = act === 'keep' ? { keep: steps[(steps.indexOf(it.keep || 0) + 1) % steps.length], newOnly: !!it.newOnly } : { keep: it.keep || 0, newOnly: !it.newOnly };
      return api().UpdateRule(it.id, u).then(loadDVR).catch(fail);
    }
    case 'delete': case 'skip': case 'stop': {
      if (!it.id) return null;
      if (!armed(act, it)) {
        pendingDelete = `${act}:${it.id}`;
        clearTimeout(pendingTimer);
        pendingTimer = setTimeout(() => { pendingDelete = ''; if (state.view === 'recordings') renderRecordings(); }, 4000);
        const what = act === 'delete' ? `delete ${it.title}` : act === 'stop' ? `stop recording ${it.title}` : `skip ${it.title}`;
        if (recUI.zone !== 'actions') toast(`Press Delete again to ${what}`);
        return renderRecordings();
      }
      pendingDelete = '';
      clearTimeout(pendingTimer);
      const done = act === 'stop' ? api().DeleteRule(it.id) : api().DeleteRecording(it.id);
      return done.then(() => {
        toast(act === 'delete' ? 'Deleted' : act === 'stop' ? 'Series recording stopped' : 'Removed from the schedule');
        recZone('list');
        return loadDVR();
      }).catch(fail);
    }
    default: return null;
  }
}

function recordingsKey(e) {
  const tab = state.recTab || 'library';
  const lists = recLists();
  const list = lists[tab];
  const sel = state.recSelBy || (state.recSelBy = { library: 0, upcoming: 0, series: 0 });
  if (e.key === '[' || e.key === ']') {
    const i = REC_TABS.findIndex(([k]) => k === tab);
    state.recTab = REC_TABS[(i + (e.key === ']' ? 1 : REC_TABS.length - 1)) % REC_TABS.length][0];
    return renderRecordings();
  }
  if (recUI.zone === 'menu') return recMenuKey(e);
  if (recUI.zone === 'actions' && recActionsKey(e)) return undefined;
  const cols = tab === 'library' ? Math.max(1, Math.round(f($('#recordings'), 'library').clientWidth / 280)) : 1;
  if (e.key === 'ArrowUp' && (!list.length || sel[tab] < cols)) { e.preventDefault(); return openDock(); }
  if (e.key === 'ArrowLeft' && (tab !== 'library' || sel[tab] % cols === 0)) { e.preventDefault(); return recZone('menu'); }
  if (!list.length) return undefined;
  switch (e.key) {
    case 'ArrowRight':
      if (tab === 'library') sel[tab] = Math.min(list.length - 1, sel[tab] + 1);
      else { e.preventDefault(); return recZone('actions', '-'); }
      break;
    case 'ArrowLeft': sel[tab] = Math.max(0, sel[tab] - 1); break;
    case 'ArrowDown': sel[tab] = Math.min(list.length - 1, sel[tab] + cols); break;
    case 'ArrowUp': sel[tab] = Math.max(0, sel[tab] - cols); break;
    case 'Enter':
      e.preventDefault();
      // Shift Enter, on a keyboard, plays a recording from its start.
      if (tab === 'library' && e.shiftKey) return playRecording(list[sel[tab]], 0);
      return recZone('actions', '-');
    case 'w': case 'W': return tab === 'library' ? recAct('watched') : undefined;
    case 'k': case 'K': return tab === 'series' ? recAct('keep') : undefined;
    case 'n': case 'N': return tab === 'series' ? recAct('newonly') : undefined;
    case 'Delete': case 'Backspace':
      e.preventDefault();
      return recAct(tab === 'library' ? 'delete' : tab === 'series' ? 'stop' : 'skip');
    default: return undefined;
  }
  e.preventDefault();
  renderRecordings();
  const el = $(`#recordings .r-panel.on [data-i="${sel[tab]}"]`);
  if (el) el.scrollIntoView({ block: 'nearest' });
  return undefined;
}

// recMenuKey: Up and Down pick a list (as tabs do), Right or OK goes into
// it, Left (or Up from the first) to the dock.
function recMenuKey(e) {
  const i = REC_TABS.findIndex(([k]) => k === (state.recTab || 'library'));
  switch (e.key) {
    case 'ArrowUp': case 'ArrowDown': {
      const j = i + (e.key === 'ArrowUp' ? -1 : 1);
      if (j < 0) return openDock();
      if (j >= REC_TABS.length) break;
      state.recTab = REC_TABS[j][0];
      renderRecordings();
      break;
    }
    case 'ArrowRight': case 'Enter':
      if (recLists()[state.recTab || 'library'].length) recZone('list');
      break;
    case 'ArrowLeft': return openDock();
    default: return undefined;
  }
  e.preventDefault();
  return undefined;
}

// recActionsKey: Left and Right along the actions, OK presses. Above the
// library's grid, Down goes back to it and Up to the dock; in a list Up
// and Down move to the item above or below, and Left from the first
// action back to the item.
function recActionsKey(e) {
  const acts = recActs();
  const el = document.activeElement;
  const lib = (state.recTab || 'library') === 'library';
  switch (e.key) {
    case 'ArrowLeft': case 'ArrowRight': {
      const next = neighbor(acts, el, e.key === 'ArrowLeft' ? -1 : 1);
      if (next) focusEl(next);
      else if (e.key === 'ArrowLeft' && !lib) recZone('list');
      break;
    }
    case 'ArrowUp': case 'ArrowDown':
      if (lib) {
        if (e.key === 'ArrowUp') openDock(); else recZone('list');
        break;
      }
      recZone('list');
      return false; // and moves in the list
    case 'Enter':
      if (acts.includes(el)) el.click(); else recZone('actions');
      break;
    default: return false;
  }
  e.preventDefault();
  return true;
}

const resumable = (it) => !!it && !it.watched && it.position > 30 && it.duration > 0 && it.position < it.duration - 60;

async function playRecording(it, from = 0) {
  if (!it || it.status === 'failed') return toast('That recording failed and cannot be played');
  const token = ++state.tuneToken;
  state.recording = it;
  showWX(false);
  setView('tv');
  stage.classList.add('tuning');
  $('#nosignal').hidden = true;
  showBanner();
  if (state.hls) state.hls.stopLoad();
  try {
    const pb = await api().PlayRecording(it.id, from);
    if (token !== state.tuneToken) return;
    state.recOffset = pb.offset || 0;
    await play(pb, token);
  } catch (e) {
    if (token !== state.tuneToken) return;
    showNoSignal({ number: 'REC', callSign: it.title, network: '' }, String(e && e.message ? e.message : e));
  } finally {
    if (token === state.tuneToken) setTimeout(() => stage.classList.remove('tuning'), 200);
  }
}

// ---------- favorites and hidden channels ----------
async function saveAppSettings(next) {
  applyBoot(await api().SaveSettings(next));
}

async function toggleFavorite(ch) {
  if (!ch) return;
  const favs = new Set(state.settings.favorites || []);
  const on = !favs.has(ch.key);
  if (on) favs.add(ch.key); else favs.delete(ch.key);
  await saveAppSettings({ ...state.settings, favorites: [...favs] });
  toast(on ? `${ch.number} ${ch.network || displayCall(ch)} added to favorites` : `${ch.number} removed from favorites`);
  if (state.view === 'guide') renderGuide();
}

async function hideChannel(ch) {
  if (!ch) return;
  const hidden = new Set(state.settings.hidden || []);
  hidden.add(ch.key);
  state.lastHidden = ch.key;
  await saveAppSettings({ ...state.settings, hidden: [...hidden] });
  buildLineup();
  toast(`Hid ${ch.number} ${ch.network || displayCall(ch)}. ${androidTV && !prompts.pad ? 'Settings, Hidden channels shows it again.' : 'Press U to undo.'}`, 5000);
  if (state.view === 'guide') renderGuide();
}

async function unhide(keys) {
  const hidden = new Set(state.settings.hidden || []);
  for (const k of keys) hidden.delete(k);
  await saveAppSettings({ ...state.settings, hidden: [...hidden] });
  buildLineup();
  renderAll();
}

const SHOPPING = /\b(qvc2?|hsn2?|shop ?lc|jewelry|jtv|tvdeals|deals|shopping)\b/i;

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
  if (video.paused) video.play().catch(() => {}); else video.pause();
  showTimeshift();
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

// ---------- captions ----------
// Captions follow the saved setting, except that programs whose sound
// isn't English start with them on. C during such a program turns them
// off or on for it alone, leaving the setting as it was.
const ENGLISH = /^(en|eng|english)([-_].*)?$/i;
const UNKNOWN = /^(|und|unknown|mul|mis|zxx|qaa)$/i;
const foreignAudio = (p) => !!p && !!p.audioLang && !ENGLISH.test(p.audioLang) && !UNKNOWN.test(p.audioLang);

function programOn() {
  if (state.recording || !state.current) return null;
  return airingAt(state.current, Date.now()) || null;
}

const programKey = (p) => `${state.current && state.current.key}|${p.start}`;

function captionsWanted() {
  const p = programOn();
  if (foreignAudio(p)) {
    const o = state.captionsFor;
    return o && o.key === programKey(p) ? o.on : true;
  }
  return !!state.settings.captions;
}

// The app draws captions itself, the same on macOS and Linux, from the
// stream's text track, which it keeps hidden from the webview: a custom
// channel's WebVTT subtitles (timed word by word when YouTube's speech
// recognition made them), else a broadcast's CEA-608 captions as hls.js or
// WebKit decode them. They sit low on the picture, above the banner while
// it shows, sized to the picture and the interface size: at most two
// lines, as even as they'll go, broken where the source broke them when
// that fits. A caption too long for two lines is paged, each page filling
// the box in turn, and timed words appear as they're said. Pretext
// (vendor/pretext) measures the words; canvas does until it has loaded,
// or if it can't.
const CAP_FAMILY = '"Schibsted", "Helvetica Neue", sans-serif';
const CAP_HEIGHT = 0.046; // font size, of the picture's height at 100%
const CAP_EMS = 21;       // the widest line, in ems: about 42 characters
const cap = {
  el: $('#captions'),
  track: null,      // the text track drawn
  timedTrack: false, // whether it times words
  checked: 0,       // how many of its cues that's from
  m: null,          // sizes: { size, max, bottom, pad, font, key }
  key: '',          // the page drawn, at its size
  page: null,
  shown: -1,        // how many of its words show
  spans: [],        // its word elements, timed pages only
  bars: [],
  logical: null,    // the track's cues, whole: { sig, cues }
  timed: null,      // a timed track's words, paged
  frozen: new Map(),// timed pages shown, as they were laid out
  units: new Map(), // untimed captions, paged, by cue
  raf: 0,
  ready: false,     // the caption font has loaded
};
let pretext = null;
import('./vendor/pretext/rich-inline.js')
  .then((m) => { pretext = m; resetCaptionLayout(); })
  .catch((e) => log('info', `captions measured with canvas: ${e}`));
document.fonts.load(`500 32px ${CAP_FAMILY}`).catch(() => {}).then(() => { cap.ready = true; resetCaptionLayout(); });

function captionTracks() {
  return [...(video.textTracks || [])].filter((t) => t.kind === 'captions' || t.kind === 'subtitles');
}

// applyCaptions shows or hides captions as wanted, from the text subtitles
// when there are some (a custom channel has those and the same words as
// 608 in the picture), else the captions in the picture. It reports
// whether the stream has any.
function applyCaptions() {
  const { tracks, pick, on } = captionPlan();
  for (const t of tracks) {
    const mode = on && t === pick ? 'hidden' : 'disabled';
    if (t.mode !== mode) t.mode = mode;
  }
  // hls.js stops fetching subtitles nobody's reading.
  if (!on && state.hls && state.hls.subtitleTrack !== -1) state.hls.subtitleTrack = -1;
  setCaptionTrack(on ? pick : null);
  return !!pick;
}

// captionPlan is which tracks there are, which one captions come from,
// and whether they're wanted.
function captionPlan() {
  const tracks = captionTracks();
  const pick = tracks.find((t) => t.kind === 'subtitles') || tracks[0] || null;
  return { tracks, pick, on: !!state.settings && captionsWanted() };
}

// Only the app turns captions on: when a player or the webview changes a
// track (picking one itself, or following a system caption setting), the
// app sets them back as wanted.
function keepCaptions() {
  const { tracks, pick, on } = captionPlan();
  if (tracks.some((t) => t.mode !== (on && t === pick ? 'hidden' : 'disabled'))) applyCaptions();
}

async function toggleCaptions() {
  const p = programOn();
  if (foreignAudio(p)) state.captionsFor = { key: programKey(p), on: !captionsWanted() };
  else await saveAppSettings({ ...state.settings, captions: !state.settings.captions });
  const found = applyCaptions();
  toast(captionsWanted() ? (found ? 'Captions on' : 'Captions on (this program has none)') : 'Captions off', 1800);
}

function setCaptionTrack(t) {
  if (t === cap.track) return;
  cap.track = t;
  cap.timedTrack = false;
  cap.checked = 0;
  cap.logical = null;
  cap.timed = null;
  cap.frozen.clear();
  cap.units.clear();
  drawCaption(null);
  if (t && !cap.raf) cap.raf = requestAnimationFrame(captionFrame);
}

// resetCaptionLayout lays captions out again: the picture, the banner or
// the font changed.
function resetCaptionLayout() {
  cap.m = null;
  cap.key = '';
}

function captionFrame() {
  cap.raf = 0;
  if (!cap.track) return;
  if (state.view === 'tv' && cap.ready) renderCaption(video.currentTime);
  cap.raf = requestAnimationFrame(captionFrame);
}

// captionSizes fits captions to the picture within the stage, in CSS
// pixels: the font a share of the picture's height, scaled with the
// interface, and the lines' bottom a little above the picture's, or above
// the banner or the weather crawl.
function captionSizes() {
  const w = stage.clientWidth;
  const h = stage.clientHeight;
  if (!w || !h) return null;
  const ar = video.videoWidth && video.videoHeight ? video.videoWidth / video.videoHeight : 16 / 9;
  const pw = Math.min(w, h * ar);
  const ph = pw / ar;
  // Larger with the interface, though only half as fast: at 150% they'd
  // otherwise crowd the picture.
  const z = 1 + (((state.settings && state.settings.scale) || autoScale()) / 100 - 1) / 2;
  const size = Math.max(12, Math.round(ph * CAP_HEIGHT * z));
  const max = Math.round(Math.min(pw * 0.88, size * CAP_EMS));
  let bottom = (h - ph) / 2 + ph * 0.055;
  const banner = $('#banner');
  if (state.view === 'tv' && banner.classList.contains('show')) bottom = Math.max(bottom, h - banner.offsetTop + size * 0.45);
  const crawl = $('#crawl');
  if (crawl && document.body.classList.contains('has-crawl')) bottom = Math.max(bottom, crawl.offsetHeight + size * 0.45);
  return { size, max, bottom: Math.round(bottom), pad: size * 0.3, font: `500 ${size}px ${CAP_FAMILY}`, key: `${size}/${max}` };
}

function renderCaption(t) {
  if (!cap.m) {
    cap.m = captionSizes();
    if (!cap.m) return;
    cap.el.style.fontSize = `${cap.m.size}px`;
    cap.el.style.bottom = `${cap.m.bottom}px`;
  }
  const track = cap.track;
  const n = track.cues ? track.cues.length : 0;
  if (!cap.timedTrack && n !== cap.checked) {
    cap.timedTrack = timesWords(track);
    cap.checked = n;
  }
  const at = cap.timedTrack ? timedPage(track, t) : untimedPage(track, t);
  drawCaption(at, t);
}

// ----- cue text -----
const VTT_TIME = /^<(?:(\d+):)?(\d\d):(\d\d)\.(\d{3})>$/;
const CUE_ENTITIES = { amp: '&', lt: '<', gt: '>', nbsp: ' ', quot: '"', apos: "'", lrm: '', rlm: '' };
const decodeCue = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (all, e) => {
  if (e[0] !== '#') return CUE_ENTITIES[e.toLowerCase()] ?? all;
  return String.fromCodePoint(e[1] === 'x' || e[1] === 'X' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
}).replace(/¶/g, '♪'); // a music note, as captions made from broadcast ones misname it
const parsedCues = new Map(); // by start and text

function cueText(cue) {
  if (typeof cue.text === 'string') return cue.text;
  return cue.getCueAsHTML ? cue.getCueAsHTML().textContent : '';
}

// logicalCues is a track's cues as the stream sent them, by start. WebKit
// splits a cue that shows alongside others into pieces with its text, one
// for each stretch the set showing stays the same; they're joined again.
function logicalCues(track) {
  const list = track.cues;
  const n = list ? list.length : 0;
  const sig = n ? `${n}|${list[0].startTime}|${list[n - 1].startTime}` : '';
  if (cap.logical && cap.logical.sig === sig) return cap.logical.cues;
  const cues = [];
  const open = new Map();
  for (let i = 0; i < n; i++) {
    const c = list[i];
    const text = cueText(c);
    const l = open.get(text);
    if (l && c.startTime <= l.endTime + 0.05) {
      l.endTime = Math.max(l.endTime, c.endTime);
      continue;
    }
    const cue = { startTime: c.startTime, endTime: c.endTime, text, line: c.line };
    open.set(text, cue);
    cues.push(cue);
  }
  cap.logical = { sig, cues };
  return cues;
}

// cueLines reads a cue into lines of words, each with when it's said:
// the cue's start, or the WebVTT timestamp before it.
function cueLines(cue) {
  const id = `${cue.startTime}\n${cue.text}`;
  let lines = parsedCues.get(id);
  if (lines) {
    for (const l of lines) for (const w of l) w.end = cue.endTime;
    return lines;
  }
  if (parsedCues.size > 5000) parsedCues.clear();
  lines = [];
  for (const raw of cue.text.split(/\r?\n/)) {
    let at = cue.startTime;
    const words = [];
    for (const part of raw.split(/(<[^>]*>)/)) {
      if (part[0] === '<') {
        const m = VTT_TIME.exec(part);
        if (m) at = (+m[1] || 0) * 3600 + +m[2] * 60 + +m[3] + +m[4] / 1000;
        continue;
      }
      for (const w of decodeCue(part).split(/\s+/)) {
        if (w) words.push({ text: w, at, end: cue.endTime });
      }
    }
    if (words.length) lines.push(words);
  }
  parsedCues.set(id, lines);
  return lines;
}

// timesWords reports whether a track's captions time their words, as
// speech recognition's do (though not every cue: a one-word line has no
// time but its start).
function timesWords(track) {
  for (let i = 0; i < Math.min(track.cues.length, 40); i++) {
    if (/<(\d+:)?\d\d:\d\d\.\d{3}>/.test(cueText(track.cues[i]))) return true;
  }
  return false;
}

// ----- measuring -----
const capWidths = new Map(); // font and word: width
let capCanvas = null;

function canvasFont(font) {
  if (!capCanvas) capCanvas = document.createElement('canvas').getContext('2d');
  capCanvas.font = font;
  return capCanvas;
}

// measureWords returns the width of each word in font.
function measureWords(words, font) {
  if (capWidths.size > 20000) capWidths.clear();
  const out = new Array(words.length);
  const todo = [];
  words.forEach((w, i) => {
    const v = capWidths.get(`${font}\n${w}`);
    if (v === undefined) todo.push(i); else out[i] = v;
  });
  if (todo.length && pretext) {
    const p = pretext.prepareRichInline(todo.map((i, k) => ({ text: (k ? ' ' : '') + words[i], font, break: 'never' })));
    pretext.walkRichInlineLineRanges(p, 1e9, (range) => {
      for (const fr of pretext.materializeRichInlineLineRange(p, range).fragments) out[todo[fr.itemIndex]] = fr.occupiedWidth;
    });
  }
  for (const i of todo) {
    if (out[i] === undefined) out[i] = canvasFont(font).measureText(words[i]).width;
    capWidths.set(`${font}\n${words[i]}`, out[i]);
  }
  return out;
}

const spaceWidth = (font) => measureWords(['a\u00a0a', 'aa'], font).reduce((x, y) => x - y);

// ----- laying out -----
const SENTENCE_END = /[.!?…♪]["'”’)\]]*$/;
const CLAUSE_END = /[,;:.!?…—]["'”’)\]]*$/;

// pageWords splits words from..to into pages of at most two lines no
// wider than max, each as full as it goes but ending a sentence where one
// ends late in it, and the last not left with a word or two. A word
// marked brk starts a line (a change of speaker).
function pageWords(words, widths, space, max, from = 0, to = words.length) {
  // fill is where a page from i ends when filled.
  const fill = (i) => {
    let line = 1;
    let start = i;
    let w = 0;
    for (let j = i; j < to; j++) {
      if (j > start && (words[j].brk || w + space + widths[j] > max)) {
        if (line === 2) return j;
        line = 2;
        start = j;
        w = widths[j];
      } else {
        w += (j > start ? space : 0) + widths[j];
      }
    }
    return to;
  };
  const ranges = [];
  for (let i = from; i < to;) {
    let end = fill(i);
    if (end < to) {
      for (let k = end - 1; k > i + (end - i) / 2; k--) {
        if (SENTENCE_END.test(words[k].text)) { end = k + 1; break; }
      }
    }
    ranges.push([i, end]);
    i = end;
  }
  const chars = (a, b) => { let n = 0; for (let i = a; i < b; i++) n += words[i].text.length + 1; return n; };
  const n = ranges.length;
  if (n > 1 && chars(...ranges[n - 1]) < 0.35 * chars(...ranges[n - 2])) {
    // Even out the last two pages, splitting after punctuation if near.
    const [a] = ranges[n - 2];
    const b = ranges[n - 1][1];
    let best = Infinity;
    for (let k = a + 1; k < b; k++) {
      if (fill(a) < k || fill(k) < b) continue;
      const c = Math.abs(chars(a, k) - chars(k, b)) - (CLAUSE_END.test(words[k - 1].text) ? 8 : 0);
      if (c < best) { best = c; ranges[n - 2] = [a, k]; ranges[n - 1] = [k, b]; }
    }
  }
  return ranges.map(([a, b]) => balance(words, widths, space, max, a, b));
}

// balance puts words a..b on one line if they fit, else two of about
// equal width, breaking after punctuation where it can, at a change of
// speaker where there is one.
function balance(words, widths, space, max, a, b) {
  const sum = [0];
  for (let i = a; i < b; i++) sum.push(sum[sum.length - 1] + widths[i] + space);
  const width = (x, y) => sum[y - a] - sum[x - a] - space;
  for (let k = a + 1; k < b; k++) {
    if (words[k].brk) return { from: a, to: b, lines: [[a, k], [k, b]] };
  }
  if (width(a, b) <= max || b - a < 2) return { from: a, to: b, lines: [[a, b]] };
  let best = Infinity;
  let at = a + 1;
  for (let k = a + 1; k < b; k++) {
    const l = width(a, k);
    const r = width(k, b);
    if (l > max || r > max) continue;
    const c = Math.abs(l - r) + (l > r ? space : 0) - (CLAUSE_END.test(words[k - 1].text) ? 3 * space : 0);
    if (c < best) { best = c; at = k; }
  }
  return { from: a, to: b, lines: [[a, at], [at, b]] };
}

// speakerMark reports whether a word marks a change of speaker, as
// speech recognition and broadcast captions do with ">>".
const speakerMark = (w) => w.startsWith('>>');

// ----- choosing what shows -----
// cuesAt lists a track's cues showing at t, top row first.
function cuesAt(track, t) {
  const list = logicalCues(track);
  let lo = 0;
  let hi = list.length - 1;
  let i = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (list[mid].startTime <= t) { i = mid; lo = mid + 1; } else hi = mid - 1;
  }
  const out = [];
  for (let k = i; k >= 0 && k > i - 40; k--) if (list[k].endTime > t) out.push(list[k]);
  const row = (c) => (typeof c.line === 'number' ? c.line : 0);
  return out.sort((x, y) => row(x) - row(y) || x.startTime - y.startTime);
}

// untimedPage is what shows at t of captions without word times: the
// cues showing, laid out together and paged over their time.
function untimedPage(track, t) {
  const cues = cuesAt(track, t);
  if (!cues.length) return null;
  const key = cues.map((c) => `${c.startTime}/${c.text}`).join('\n');
  let u = cap.units.get(key);
  if (!u || u.end !== Math.max(...cues.map((c) => c.endTime))) {
    if (cap.units.size > 200) cap.units.clear();
    const m = cap.m;
    // Overlapping cues showing the same line show it once, oldest first.
    const seen = new Set();
    const lines = cues.flatMap(cueLines).filter((l) => {
      const text = l.map((w) => w.text).join(' ');
      return !seen.has(text) && seen.add(text);
    });
    const words = [];
    for (const [i, l] of lines.entries()) {
      for (const [k, w] of l.entries()) words.push({ ...w, brk: (k === 0 && i > 0 && /^[-–—]/.test(w.text)) || speakerMark(w.text) });
    }
    const widths = measureWords(words.map((w) => w.text), m.font);
    const space = spaceWidth(m.font);
    // Lines that fit stay as the source broke them.
    let pages;
    const lineWidth = (from, n) => widths.slice(from, from + n).reduce((s, x) => s + x, 0) + (n - 1) * space;
    let from = 0;
    if (lines.length <= 2 && lines.every((l) => { const ok = lineWidth(from, l.length) <= m.max; from += l.length; return ok; })) {
      const cut = lines[0].length;
      pages = [{ from: 0, to: words.length, lines: lines.length === 2 ? [[0, cut], [cut, words.length]] : [[0, words.length]] }];
    } else {
      pages = pageWords(words, widths, space, m.max);
    }
    // Pages share the cues' time out by length.
    const start = Math.min(...cues.map((c) => c.startTime));
    const end = Math.max(...cues.map((c) => c.endTime));
    const chars = (p) => words.slice(p.from, p.to).reduce((s, w) => s + w.text.length + 1, 0);
    const total = pages.reduce((s, p) => s + chars(p), 0);
    let done = 0;
    for (const [i, p] of pages.entries()) {
      p.start = start + ((end - start) * done) / total;
      done += chars(p);
      p.end = i === pages.length - 1 ? end : start + ((end - start) * done) / total;
      p.id = `${key}#${i}`;
    }
    u = { words, widths, space, pages, end };
    cap.units.set(key, u);
  }
  const page = u.pages.find((p) => p.start <= t && t < p.end) || u.pages[u.pages.length - 1];
  return { page, words: u.words, widths: u.widths, space: u.space };
}

// timedPage is what shows at t of captions timed word by word: the
// track's words, in runs of speech split at pauses, paged; a page shows
// from its first word until the next page's, its words as they're said.
// Captions arrive a little ahead of the picture, so a page is laid out
// with what's known then; once it shows it stays as it is (frozen), and
// words that come later go on the pages after.
const wordKey = (w) => `${w.at}\n${w.text}`;

function timedPage(track, t) {
  const m = cap.m;
  const list = logicalCues(track);
  if (!list.length) return null;
  const sig = `${cap.logical.sig}|${m.key}`;
  if (!cap.timed || cap.timed.sig !== sig) {
    const all = [];
    // A cue's last line is its own: the server's screens roll up, the
    // line above repeating the cue before's, which other sources overlap.
    for (const c of list) {
      const lines = cueLines(c);
      if (lines.length) all.push(...lines[lines.length - 1]);
    }
    all.sort((x, y) => x.at - y.at);
    const words = [];
    for (const w of all) {
      const last = words[words.length - 1];
      if (last && last.at === w.at && last.text === w.text) last.end = Math.max(last.end, w.end);
      else words.push({ ...w, brk: speakerMark(w.text) });
    }
    const widths = measureWords(words.map((w) => w.text), m.font);
    const space = spaceWidth(m.font);
    const pages = [];
    for (let a = 0; a < words.length;) {
      let b = a + 1;
      while (b < words.length && words[b].at <= words[b - 1].end + 0.25 && words[b].at - words[b - 1].at < 4) b++;
      const run = [];
      let i = a;
      // Pages already shown, as they were.
      for (let f = cap.frozen.get(wordKey(words[i])); f && i < b; f = i < b && cap.frozen.get(wordKey(words[i]))) {
        let j = i;
        while (j < b && wordKey(words[j]) !== f.last) j++;
        if (j === b) break;
        let k = -1;
        for (let x = i + 1; x <= j; x++) if (f.brk && wordKey(words[x]) === f.brk) k = x;
        run.push({ from: i, to: j + 1, lines: k > 0 ? [[i, k], [k, j + 1]] : [[i, j + 1]] });
        i = j + 1;
      }
      if (i < b) run.push(...pageWords(words, widths, space, m.max, i, b));
      for (const [k, p] of run.entries()) {
        p.start = words[p.from].at;
        let end = 0;
        for (let x = p.from; x < p.to; x++) end = Math.max(end, words[x].end);
        p.end = k + 1 < run.length ? words[run[k + 1].from].at : Math.max(end, p.start + 1);
        p.id = `${wordKey(words[p.from])}/${p.to - p.from}`;
        pages.push(p);
      }
      a = b;
    }
    cap.timed = { sig, words, widths, space, pages };
  }
  const { pages, words } = cap.timed;
  let lo = 0;
  let hi = pages.length - 1;
  let i = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (pages[mid].start <= t) { i = mid; lo = mid + 1; } else hi = mid - 1;
  }
  if (i < 0 || t >= pages[i].end) return null;
  const page = pages[i];
  if (!cap.frozen.has(wordKey(words[page.from]))) {
    if (cap.frozen.size > 400) cap.frozen.delete(cap.frozen.keys().next().value);
    const [, second] = page.lines;
    cap.frozen.set(wordKey(words[page.from]), { last: wordKey(words[page.to - 1]), brk: second ? wordKey(words[second[0]]) : '' });
  }
  return { page, words, widths: cap.timed.widths, space: cap.timed.space, timed: true };
}

// ----- drawing -----
function drawCaption(at, t) {
  const key = at ? `${at.page.id}@${cap.m.key}` : '';
  if (key !== cap.key) {
    cap.key = key;
    cap.page = at;
    cap.shown = -1;
    cap.spans = [];
    cap.bars = [];
    cap.el.textContent = '';
    if (!at) return;
    for (const [a, b] of at.page.lines) {
      const line = document.createElement('div');
      line.className = 'cap-line';
      const bar = document.createElement('span');
      bar.className = at.timed ? 'cap-bar timed' : 'cap-bar';
      if (at.timed) {
        for (let i = a; i < b; i++) {
          const s = document.createElement('span');
          s.textContent = (i > a ? ' ' : '') + at.words[i].text;
          bar.append(s);
          cap.spans.push({ el: s, i, bar, a });
        }
        cap.bars.push({ bar, a, b });
      } else {
        bar.textContent = at.words.slice(a, b).map((w) => w.text).join(' ');
      }
      line.append(bar);
      cap.el.append(line);
    }
  }
  if (!at || !at.timed) return;
  // Timed words show as they're said, with the bar behind them growing.
  const { page, words, widths, space } = at;
  let n = page.from;
  while (n < page.to && words[n].at <= t) n++;
  if (n === cap.shown) return;
  cap.shown = n;
  for (const s of cap.spans) s.el.classList.toggle('wait', s.i >= n);
  for (const { bar, a, b } of cap.bars) {
    let w = 0;
    for (let i = a; i < Math.min(b, n); i++) w += widths[i] + (i > a ? space : 0);
    bar.style.setProperty('--r', n > a ? `${Math.ceil(w + 2 * cap.m.pad + 2)}px` : '0px');
  }
}

// A new stream: the last one's captions go at once, not when its tracks do.
video.addEventListener('emptied', () => setCaptionTrack(null));
if (window.ResizeObserver) new ResizeObserver(resetCaptionLayout).observe(stage);
video.addEventListener('resize', resetCaptionLayout);
new MutationObserver(resetCaptionLayout).observe($('#banner'), { attributes: true, attributeFilter: ['class'] });
new MutationObserver(resetCaptionLayout).observe(document.body, { attributes: true, attributeFilter: ['class'] });

function audioTracks() {
  if (state.hls) return state.hls.audioTracks.map((t, i) => ({ i, label: t.name, lang: t.lang, on: state.hls.audioTrack === i }));
  const list = video.audioTracks;
  if (!list) return [];
  const out = [];
  for (let i = 0; i < list.length; i++) out.push({ i, label: list[i].label, lang: list[i].language, on: list[i].enabled });
  return out;
}

function selectAudio(i) {
  if (state.hls) { state.hls.audioTrack = i; return; }
  const list = video.audioTracks;
  for (let j = 0; j < list.length; j++) list[j].enabled = j === i;
}

// applyAudio picks the preferred language when the stream offers it.
function applyAudio() {
  const want = state.settings.audioLang;
  if (!want) return;
  const t = audioTracks().find((x) => (x.lang || '').startsWith(want));
  if (t && !t.on) selectAudio(t.i);
}

async function cycleAudio() {
  const tracks = audioTracks();
  if (tracks.length < 2) return toast('Only one audio track on this channel', 1800);
  const cur = tracks.findIndex((t) => t.on);
  const next = tracks[(cur + 1) % tracks.length];
  selectAudio(next.i);
  await saveAppSettings({ ...state.settings, audioLang: (next.lang || '').slice(0, 2) });
  toast(`Audio: ${next.label || next.lang || `track ${next.i + 1}`}`, 1800);
}

// ---------- recording progress ----------
let progressTimer = 0;
function saveRecordingProgress() {
  const it = state.recording;
  if (!it || !it.id) return;
  const pos = (state.recOffset || 0) + video.currentTime;
  if (pos < 1) return;
  it.position = pos;
  api().SaveProgress(it.id, pos, it.duration || 0).catch((e) => log('warn', `progress: ${e}`));
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
    m = { title: it.title, artist: 'Recording', album: it.subtitle || 'Airwaves', art: it.image };
  } else if (ch && ch.weather) {
    m = { title: 'Local Forecast', artist: `${ch.number} ${ch.name}`, album: 'Airwaves', art: ch.logo };
  } else if (ch) {
    const p = airingAt(ch, Date.now());
    const name = ch.network || displayCall(ch);
    m = { title: p ? p.title : name, artist: `${ch.number} ${name}`, album: (p && p.episodeTitle) || 'Airwaves', art: (p && p.image) || ch.logo };
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

// ---------- weather ----------
// showWX puts the Airwaves Weather display on the stage: as the weather channel
// (with its music), or as a silent preview on the weather page. The display
// itself is always silent; the music plays here, so muting is instant.
function showWX(on, preview = false) {
  const frame = $('#wx');
  frame.hidden = !on;
  const src = `${state.boot.serverUrl}/weatherstar`;
  if (on && frame.dataset.src !== src) {
    frame.src = src;
    frame.dataset.src = src;
  }
  if (!on && frame.dataset.src) {
    frame.removeAttribute('src');
    delete frame.dataset.src;
  }
  $('#video').hidden = on;
  if (on) fitWX();
  playWXMusic(on && !preview);
}

// fitWX scales the weather display to fill the stage. It is laid out for
// 854x480 and scales itself to its window; the interface size scaled it
// again, off centre. So its window is always 854x480 and the frame is
// scaled here instead, at every stage size. Page zoom (Linux) leaves the
// frame's window at 854x480; CSS zoom would shrink it, so the frame undoes
// the root's CSS zoom and is sized in its own, unzoomed pixels.
const WX_W = 854;
const WX_H = 480;
function fitWX() {
  const z = parseFloat(document.documentElement.style.zoom) || 1;
  const w = stage.clientWidth * z;
  const h = stage.clientHeight * z;
  if (!w || !h) return;
  const s = Math.min(w / WX_W, h / WX_H);
  const frame = $('#wx');
  frame.style.zoom = z === 1 ? '' : String(1 / z);
  frame.style.transform = `translate(${(w - WX_W * s) / 2}px, ${(h - WX_H * s) / 2}px) scale(${s})`;
}
if (window.ResizeObserver) new ResizeObserver(fitWX).observe(stage);

const wxMusic = new Audio();
wxMusic.volume = 0.6;
wxMusic.addEventListener('ended', () => nextWXTrack());

// playWXMusic starts or stops the weather channel's music: the server's
// tracks, shuffled and looped.
async function playWXMusic(on) {
  state.wxMusicOn = on;
  if (!on) return wxMusic.pause();
  if (!state.wxTracks) {
    try {
      const r = await fetch(`${state.boot.serverUrl}/weatherstar/music`);
      state.wxTracks = ((await r.json()).tracks || []).map((t) => (t.startsWith('/') ? state.boot.serverUrl + t : t));
    } catch (e) {
      log('warn', `weather music: ${e}`);
      state.wxTracks = [];
    }
  }
  if (!state.wxMusicOn || !state.wxTracks.length) return;
  wxMusic.muted = !!state.userMuted;
  if (!wxMusic.src) return nextWXTrack();
  wxMusic.play().catch(() => {});
}

function nextWXTrack() {
  const tracks = state.wxTracks || [];
  if (!tracks.length || !state.wxMusicOn) return;
  state.wxTrack = ((state.wxTrack ?? Math.floor(Math.random() * tracks.length)) + 1) % tracks.length;
  wxMusic.src = tracks[state.wxTrack];
  wxMusic.play().catch((e) => log('warn', `weather music: ${e}`));
}

async function loadWeather() {
  try {
    state.wx = await api().Weather();
  } catch (e) {
    log('warn', `weather: ${e}`);
    return;
  }
  renderCrawl();
  if (state.view === 'weather') renderWeather();
  if (state.current && state.current.weather) renderBanner();
}

const deg = (v) => (v == null ? '--' : `${Math.round(v)}°`);
const tclock = clock;

function renderWXBanner(b, ch) {
  const wx = state.wx || {};
  const n = wx.now || {};
  const [p0, p1] = wx.periods || [];
  b.classList.remove('warn', 'detail');
  f(b, 'num').textContent = ch.number;
  f(b, 'num').classList.toggle('long', ch.number.length > 4);
  f(b, 'call').textContent = displayCall(ch);
  f(b, 'net').textContent = ch.network;
  showChannelLogo(b, ch);
  f(b, 'time').textContent = n.observed ? `Observed ${tclock(n.observed)}` : '';
  f(b, 'chips').innerHTML = (wx.alerts || []).map((a) => `<span class="chip live">${esc(a.event)}</span>`).join('');
  f(b, 'title').textContent = n.tempF != null ? `${deg(n.tempF)} ${n.description || ''}` : 'Local Forecast';
  f(b, 'ep').textContent = p0 ? `${p0.name}: ${p0.short}, ${p0.isDay ? 'high' : 'low'} ${p0.tempF}°` : '';
  f(b, 'progress').style.width = '0';
  f(b, 'desc').textContent = p0 ? p0.detailed : '';
  f(b, 'next').textContent = p1 ? `${p1.name}: ${p1.short}, ${p1.tempF}°` : '';
  f(b, 'note').textContent = '';
}

// Watches and warnings crawl across live TV; advisories stay on the W page.
function crawlAlerts() {
  return ((state.wx && state.wx.alerts) || []).filter((a) => /warning|watch/i.test(a.event) || a.severity === 'Severe' || a.severity === 'Extreme');
}

function renderCrawl() {
  const alerts = crawlAlerts();
  const c = $('#crawl');
  document.body.classList.toggle('has-crawl', alerts.length > 0);
  if (!alerts.length) return;
  c.classList.toggle('severe', alerts.some((a) => /warning/i.test(a.event)));
  const text = alerts.map((a) => `${a.event.toUpperCase()}${a.ends ? ` until ${new Date(a.ends).toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit' })}` : ''}. ${a.headline || ''}`).join('     |     ');
  const span = f(c, 'text');
  if (span.textContent !== text) {
    span.textContent = text;
    // Speed scales with length: about 90 px a second.
    span.style.animationDuration = `${Math.max(20, (text.length * 9 + window.innerWidth) / 90)}s`;
  }
}

// ---------- weather view ----------
// Its parts take the focus in turn, Up and Down, scrolling with it: the
// alerts, the next 24 hours, the week, the maps. Left goes to Watch the
// weather channel in the rail, when there is one, and from there to the
// dock, as does Up from the first part.
const wxUI = { at: 0 };
const wxBlocks = () => $$('#weather .wx-main > .fx').filter(isShown);
const wxWatch = () => $('#weather .wx-watch');

function wireWeather() {
  const v = $('#weather');
  wxWatch().addEventListener('click', () => {
    const ch = weatherChannel();
    if (!ch) return;
    if (!(state.current && state.current.weather && !state.recording)) tune(ch);
    setView('tv');
  });
  $('.wx-main', v).addEventListener('focusin', (e) => {
    const i = wxBlocks().indexOf(e.target);
    if (i >= 0) wxUI.at = i;
  });
}

function wxFocus() {
  if (state.dock >= 0 || state.view !== 'weather') return;
  const blocks = wxBlocks();
  const i = Math.min(wxUI.at, blocks.length - 1);
  if (i < 0) return void (isShown(wxWatch()) && focusEl(wxWatch()));
  focusEl(blocks[i]);
  if (i === 0) $('#weather .wx-main').scrollTop = 0;
}

function weatherKey(e) {
  const el = document.activeElement;
  const blocks = wxBlocks();
  if (el === wxWatch()) {
    switch (e.key) {
      case 'ArrowRight': if (blocks.length) wxFocus(); break;
      case 'ArrowLeft': case 'ArrowUp': openDock(); break;
      case 'ArrowDown': break;
      case 'Enter': el.click(); break;
      default: return;
    }
    e.preventDefault();
    return;
  }
  const i = blocks.indexOf(el);
  switch (e.key) {
    case 'ArrowUp':
      if (i > 0) { wxUI.at = i - 1; wxFocus(); } else openDock();
      break;
    case 'ArrowDown':
      if (i < 0) wxFocus();
      else if (i < blocks.length - 1) { wxUI.at = i + 1; wxFocus(); }
      break;
    case 'ArrowLeft': if (isShown(wxWatch())) focusEl(wxWatch()); else openDock(); break;
    case 'ArrowRight': if (i < 0) wxFocus(); break;
    default: return;
  }
  e.preventDefault();
}

function renderWeather() {
  const v = $('#weather');
  wxWatch().hidden = !weatherChannel();
  const wx = state.wx;
  if (!wx) {
    f(v, 'now').innerHTML = '<dl class="rail-stats"><div><dt>Weather</dt><dd>Loading</dd></div></dl>';
    return;
  }
  const n = wx.now || {};
  const air = wx.air;
  const aqiColor = air ? (air.aqi <= 50 ? 'var(--t-strong)' : air.aqi <= 100 ? '#e8d24a' : air.aqi <= 150 ? '#f08a24' : 'var(--red)') : '';
  f(v, 'now').innerHTML = `
    <div class="wx-temp">${deg(n.tempF)}</div>
    <div class="wx-desc">${esc(n.description || '')}</div>
    <dl class="rail-stats wx-facts">
      ${n.feelsLikeF != null ? `<div><dt>Feels like</dt><dd>${deg(n.feelsLikeF)}</dd></div>` : ''}
      <div><dt>Wind</dt><dd>${n.windMph != null ? `${esc(n.windDir)} ${Math.round(n.windMph)}<small>mph${n.gustMph ? `, gusts ${Math.round(n.gustMph)}` : ''}</small>` : '--'}</dd></div>
      ${n.humidity != null ? `<div><dt>Humidity</dt><dd>${Math.round(n.humidity)}%<small>dew point ${deg(n.dewpointF)}</small></dd></div>` : ''}
      ${air ? `<div><dt>Air quality</dt><dd style="color:${aqiColor}">${air.aqi}<small>${esc(air.category)}</small></dd></div>` : ''}
      ${wx.sun ? `<div><dt>Sun</dt><dd class="wx-sun">${tclock(wx.sun.rise)}<small>sets ${tclock(wx.sun.set)}</small></dd></div>` : ''}
    </dl>
    <div class="wx-updated">${n.observed ? `Observed ${tclock(n.observed)}` : ''}</div>`;

  f(v, 'alerts').innerHTML = (wx.alerts || []).map((a) => `
    <article class="wx-alert ${/warning/i.test(a.event) ? 'severe' : ''}">
      <div class="wx-alert-event">${esc(a.event)}</div>
      <div class="wx-alert-when">${a.ends ? `Until ${new Date(a.ends).toLocaleString([], { weekday: 'long', hour: 'numeric', minute: '2-digit' })}` : ''}</div>
      <p>${esc((a.description || a.headline || '').split('\n\n')[0])}</p>
    </article>`).join('');

  // Hourly: temperature line over precipitation bars, 24 hours.
  const hours = (wx.hourly || []).slice(0, 24);
  const svg = f(v, 'chart');
  if (hours.length > 1) {
    const W = 960; const H = 200; const pad = 24;
    const temps = hours.map((h) => h.tempF);
    const lo = Math.min(...temps) - 3; const hi = Math.max(...temps) + 3;
    const x = (i) => (i / (hours.length - 1)) * W;
    const y = (t) => pad + (1 - (t - lo) / (hi - lo)) * (H - pad * 2);
    const bars = hours.map((h, i) => (h.precip ? `<rect class="wx-pop" x="${x(i) - 14}" width="28" y="${H - (h.precip / 100) * (H - pad)}" height="${(h.precip / 100) * (H - pad)}"/>` : '')).join('');
    const night = hours.map((h, i) => (!h.isDay ? `<rect class="wx-night" x="${x(i) - W / hours.length / 2}" width="${W / hours.length + 1}" y="0" height="${H}"/>` : '')).join('');
    const line = hours.map((h, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(h.tempF).toFixed(1)}`).join(' ');
    svg.innerHTML = `${night}${bars}<path class="wx-line" d="${line}"/>`;
    f(v, 'hours').innerHTML = hours.map((h, i) => (i % 3 === 0 ? `<div style="left:${(i / (hours.length - 1)) * 100}%"><b>${h.tempF}°</b>${new Date(h.start).toLocaleTimeString([], { hour: 'numeric' })}${h.precip >= 20 ? `<em>${h.precip}%</em>` : ''}</div>` : '')).join('');
  }

  // 7-day: pair each day with the night after it.
  const days = [];
  for (const p of wx.periods || []) {
    if (p.isDay || !days.length) days.push({ day: p.isDay ? p : null, night: p.isDay ? null : p });
    else days[days.length - 1].night = p;
  }
  f(v, 'days').innerHTML = days.slice(0, 7).map(({ day, night }) => {
    const main = day || night;
    const name = day ? (day.name === 'This Afternoon' || day.name === 'Today' ? 'Today' : day.name.slice(0, 3)) : main.name;
    return `<div class="wx-day">
      <div class="wx-dname">${esc(name)}</div>
      <div class="wx-hilo">${day ? `<b>${day.tempF}°</b>` : ''}${night ? `<span>${night.tempF}°</span>` : ''}</div>
      <div class="wx-short">${esc(main.short)}</div>
      ${Math.max(day ? day.precip : 0, night ? night.precip : 0) >= 20 ? `<div class="wx-precip">${Math.max(day ? day.precip : 0, night ? night.precip : 0)}%</div>` : ''}
    </div>`;
  }).join('');

  const bust = Math.floor(Date.now() / (4 * MIN));
  f(v, 'radar').src = `${state.boot.serverUrl}${wx.radar}?t=${bust}`;
  f(v, 'satellite').src = `${state.boot.serverUrl}${wx.satellite}?t=${bust}`;
  // An alert that went had the focus, perhaps.
  if (state.view === 'weather' && !v.contains(document.activeElement)) wxFocus();
}

// wxPrograms is the weather channel's guide: hour-long "Local Forecast" blocks
// carrying that hour's forecast, matching what airwavesd publishes in XMLTV.
function wxPrograms() {
  const wx = state.wx;
  if (!wx) return [];
  const ch = weatherChannel();
  const genres = [...new Set(['weather', ...categoryGenres(ch && ch.category)])];
  if (state.wxProgs && state.wxProgs.at === wx.updated && state.wxProgs.genres === genres.join()) return state.wxProgs.list;
  const H = 3600_000;
  const first = Math.floor((Date.now() - H) / H) * H;
  const hourly = (wx.hourly || []).map((h) => ({ ...h, t: Date.parse(h.start) }));
  const periods = (wx.periods || []).map((p) => ({ ...p, t: Date.parse(p.start) }));
  const list = [];
  for (let i = 0; i < 48; i++) {
    const s = first + i * H;
    const h = hourly.find((x) => x.t <= s && s < x.t + H);
    const pi = periods.findIndex((p, j) => p.t <= s && s < (periods[j + 1] ? periods[j + 1].t : p.t + 12 * H));
    const alert = (wx.alerts || []).find((a) => (!a.onset || Date.parse(a.onset) < s + H) && (!a.ends || Date.parse(a.ends) > s));
    let sub = h ? `${h.tempF}° and ${h.short}` : '';
    if (alert) sub = `${alert.event}. ${sub}`;
    list.push({
      start: new Date(s).toISOString(), end: new Date(s + H).toISOString(), _s: s, _e: s + H,
      title: 'Local Forecast', episodeTitle: sub,
      description: pi >= 0 ? `${periods[pi].name}: ${periods[pi].detailed}` : 'Current conditions, the local and extended forecast, and radar.',
      genres,
    });
  }
  state.wxProgs = { at: wx.updated, genres: genres.join(), list };
  return list;
}
