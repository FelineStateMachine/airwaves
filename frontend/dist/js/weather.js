'use strict';

// The weather channel and the Weather view.

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
