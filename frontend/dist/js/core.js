'use strict';

// State, data from the server, boot, and switching views.

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
// reading of a marginal signal jumps around. The HDHomeRun reports
// strength, quality (its signal-to-noise quality) and symbol quality (the
// share of data arriving whole or corrected) as percentages; the errors in
// what arrived the server counts itself.
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
  const strengthPct = val('strengthPct');
  const symbolPct = val('symbolPct');
  let bars = 0;
  if (state !== 'off') bars = q == null ? 3 : q >= 70 ? 5 : q >= 55 ? 4 : q >= 40 ? 3 : q >= 25 ? 2 : q > 0 ? 1 : 0;
  const quality = q != null ? `${Math.round(q)}%` : '';
  const strength = strengthPct != null ? `${Math.round(strengthPct)}%` : '';
  const symbol = symbolPct != null ? `${Math.round(symbolPct)}%` : '';
  const errors = sig && sig.errorsPerSec != null ? sig.errorsPerSec : null;
  const label = state === 'off' ? 'No lock'
    : state === 'part' ? `Locked ${Math.round(lockedPct)}% of the time`
      : quality ? `Quality ${quality}` : 'Locked';
  return { state, bars, label, color: SIG_COLOR[state], quality, strength, symbol, errors, at: Date.parse(src.to || src.at), source: src.source, lockedPct };
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
  if (s.symbol && s.state !== 'off' && s.symbol !== '100%') parts.push(`symbol quality ${s.symbol}`);
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
// AirwavesTV/<version>, showing this page from the server (web.js). This
// says only what's the WebView's own (its keyboard, the Home key); what a
// TV is like is the platform's.
const androidTV = /\bAirwavesTV\//.test(navigator.userAgent);

// ---------- platform ----------
// Airwaves is three experiences, each whole and kept apart (body.pf-*,
// and the checks of platform):
//   desktop: the app in a window on a Mac, Linux or Windows, or a browser.
//     A mouse and a keyboard: hints name keys, letters are shortcuts, the
//     dock shows with the pointer.
//   console: the Linux app under gamescope, SteamOS's Game Mode on a TV.
//     A controller: hints show its buttons from the start, sized for
//     across a room.
//   tv: the Android TV app. A remote with arrows, OK, Back and media keys
//     but no letters or pointer: hints name the remote's keys, holding OK
//     offers more, and the on-screen controls lead to everything.
//   touch: a phone or a tablet, the web app installed or in a browser.
//     Taps, swipes and holds (js/touch.js), no hints, sized to fit, and
//     held sideways.
// The app says which it is (Boot's platform: AIRWAVES_PLATFORM, else a
// console's under gamescope; web.js: the Android TV app's TV, a touch
// screen's touch, else a desktop's, or ?platform=), and Settings, Display, Layout can say
// otherwise on this device. What's pressed still counts: a controller on a
// desktop brings its buttons to the hints, a keyboard on a TV its keys.
const PLATFORMS = [['desktop', 'Desktop'], ['console', 'Console'], ['tv', 'TV'], ['touch', 'Touch']];
const platformKey = 'airwaves.platform';
let platform = 'desktop';
let detectedPlatform = 'desktop';
// onRemote: the hints and the ways to do things are a TV remote's.
const onRemote = () => platform === 'tv' && !prompts.pad;
// onKeys: they're a keyboard's, letters and all.
const onKeys = () => (platform === 'desktop' || platform === 'console') && !prompts.pad;

function applyPlatform(detected) {
  if (detected) detectedPlatform = PLATFORMS.some(([k]) => k === detected) ? detected : 'desktop';
  const forced = localStorage.getItem(platformKey) || '';
  const next = PLATFORMS.some(([k]) => k === forced) ? forced : detectedPlatform;
  for (const [k] of PLATFORMS) document.body.classList.toggle(`pf-${k}`, k === next);
  const changed = next !== platform;
  platform = next;
  // A console starts with its controller's buttons in the hints.
  if (platform === 'console') setPrompts(true);
  else if (changed) setPrompts(false);
  return changed;
}

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
  podcasts: null,  // the Podcasts app's shows and episodes
  dvrKeys: new Map(), // "7.1@1791058239" -> upcoming item
  recPick: '',        // the airing whose Record is open to its choices
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
// nextGen reports whether the tuners take in ATSC 3.0; without, NextGen
// viewing (its guide filter) stays out of sight.
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
  wirePodcasts();
  wireWeather();
  wirePads();
  wireTouch();
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
  let dvrAt = Date.now();
  setInterval(() => {
    if (state.view === 'guide' || state.view === 'recordings' || Date.now() - dvrAt > 5 * MIN - 1000) {
      dvrAt = Date.now();
      loadDVR();
    }
  }, MIN);
  setInterval(heartbeat, 30_000);
  setInterval(loadWeather, 2 * MIN);
  setInterval(signalTick, 2000);
  loadDVR();
  loadWeather();
  loadPodcasts();
  setInterval(loadPodcasts, 15 * MIN);
}

document.addEventListener('DOMContentLoaded', init);

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
  document.body.classList.toggle('has-podcasts', !!state.info.podcasts);
  if (b.lite === '1' || b.lite === '0') setLite(b.lite === '1');
  applyPlatform(b.platform || (androidTV ? 'tv' : 'desktop'));
  applyScale();
  renderHints();
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
  if (state.view === 'recordings') {
    renderRecordings();
    seeFailed();
  }
  renderFailed();
}

// ---------- failed recordings ----------
// A recording that failed puts a count on Recordings in the dock, and says
// so once, until Recordings is opened.
const failedSeenKey = 'airwaves.failedSeen';
const failedTold = new Set();
const failedRecordings = () => ((state.dvr && state.dvr.recorded) || []).filter((it) => it.status === 'failed');

function renderFailed() {
  const b = $('#dock [data-view="recordings"]');
  const seen = new Set(JSON.parse(localStorage.getItem(failedSeenKey) || '[]'));
  const unseen = failedRecordings().filter((it) => !seen.has(it.id));
  let badge = $('.badge', b);
  if (!unseen.length) {
    if (badge) badge.remove();
    return;
  }
  if (!badge) {
    badge = document.createElement('span');
    badge.className = 'badge';
    b.append(badge);
  }
  badge.textContent = String(unseen.length);
  badge.title = `${unseen.length} recording${unseen.length > 1 ? 's' : ''} failed`;
  const news = unseen.filter((it) => !failedTold.has(it.id));
  for (const it of news) failedTold.add(it.id);
  if (news.length === 1) toast(`Recording failed: ${news[0].title}${news[0].detail ? `. ${news[0].detail}` : ''}`, 7000);
  else if (news.length) toast(`${news.length} recordings failed. Recordings has them.`, 7000);
}

function seeFailed() {
  localStorage.setItem(failedSeenKey, JSON.stringify(failedRecordings().map((it) => it.id)));
  renderFailed();
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
  if (v === 'settings') openSettings();
  if (v === 'podcasts') openPodcasts();
  if (v === 'tv' && state.staleStream) restartStream();
  if (v === 'recordings') seeFailed();
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
