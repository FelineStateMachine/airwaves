'use strict';

// Reception, a tab of Settings: what the tuners measured.

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
  const wasScanning = !!(state.antenna && state.antenna.tuner.scanning);
  state.antenna = { tuner: sig.tuner || {}, muxes: sig.muxes || [], sweep: sig.sweep || {} };
  state.byRF = new Map(state.antenna.muxes.map((m) => [m.rf, m]));
  const by = new Map((sig.channels || []).map((c) => [c.number, c]));
  for (const ch of state.antennaChans) {
    const c = by.get(ch.number);
    if (c) [ch.signal, ch.recent] = [c.signal, c.recent];
  }
  state.sigStamp++;
  const sw = state.antenna.sweep;
  if (was && was.running && !sw.running && receptionShown()) {
    toast(sw.note ? `Measure now ${sw.note}` : `Measured ${sw.done} RF channels`, 4000);
  }
  // A channel scan just finished: its channels are the lineup now.
  if (wasScanning && !state.antenna.tuner.scanning) {
    scan(false).then(() => {
      renderAll();
      toast(`The scan found ${state.antennaChans.length} channels`, 4000);
    }).catch((e) => log('warn', e));
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
  if (receptionShown()) renderAntenna();
  if (state.view === 'guide') renderGuide();
  if (state.view === 'tv' && $('#banner').classList.contains('show')) renderBanner();
}

// signalTick reads the signal again while it's on screen: every 2 s while
// Measure now or a channel scan runs, every 10 s in Reception or with an
// antenna channel's banner up, every 30 s in the guide.
function signalTick() {
  if (!state.info || state.info.antenna === false || document.hidden) return;
  const running = !!(state.antenna && (state.antenna.sweep.running || state.antenna.tuner.scanning));
  const banner = state.view === 'tv' && state.current && !state.current.own && $('#banner').classList.contains('show');
  const every = running ? 2000 : receptionShown() || banner ? 10_000 : state.view === 'guide' ? 30_000 : 0;
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

// scanChannels has the tuner scan for channels again, on a second press
// within a few seconds: it takes every tuner for a few minutes.
let scanArmed = 0;
async function scanChannels() {
  const t = (state.antenna && state.antenna.tuner) || {};
  if (t.scanning) return toast('The tuner is scanning for channels', 2000);
  if (Date.now() - scanArmed > 4000) {
    scanArmed = Date.now();
    renderScan();
    setTimeout(renderScan, 4100);
    return undefined;
  }
  scanArmed = 0;
  try {
    const ti = await api().ScanChannels();
    if (state.antenna) state.antenna.tuner = ti;
    signalAt = 0;
    renderTunerCard();
    renderScan();
  } catch (e) {
    toast(String(e && e.message ? e.message : e), 6000);
  }
  return undefined;
}

// renderScan shows the channel scan's button: what it does, and that it
// asks twice.
function renderScan() {
  const a = $('#antenna');
  const t = (state.antenna && state.antenna.tuner) || {};
  const btn = f(a, 'scan');
  btn.classList.toggle('busy', !!t.scanning);
  btn.textContent = t.scanning ? 'Scanning' : 'Scan for channels';
  f(a, 'snote').textContent = t.scanning ? 'The tuner is looking for channels; watching and measuring wait until it is done'
    : Date.now() - scanArmed < 4000 ? 'Press again to scan: it takes every tuner for a few minutes'
      : 'After moving the antenna, or when channels go missing';
}

function wireAntenna() {
  const a = $('#antenna');
  f(a, 'measure').addEventListener('click', () => measureNow());
  f(a, 'scan').addEventListener('click', () => scanChannels());
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
// table, and above it a row of the tuner's card, Measure now and Scan. OK
// on an RF channel with channels watches the first. Left from either goes
// back to Settings' tabs.
const antUI = { zone: 'table', key: 'measure' };

const antHead = () => $$('#antenna .a-head .fx').filter(isShown);

function antZone(zone, key = '') {
  antUI.zone = zone;
  $('#antenna').dataset.zone = zone;
  if (key) antUI.key = key;
  if (state.dock >= 0 || !receptionShown() || settingsUI.zone !== 'list') return;
  renderRFDetail(); // its OK hint is the table's
  if (zone === 'table') {
    blurIn($('#antenna'));
    scrollRF();
    return;
  }
  const items = antHead();
  focusEl(items.find((el) => el.dataset.act === antUI.key) || items[0]);
}

function antRefocus() {
  if (!receptionShown() || state.dock >= 0 || settingsUI.zone !== 'list' || antUI.zone !== 'head') return;
  const el = document.activeElement;
  if (!$('#antenna').contains(el) || !isShown(el)) antZone('head');
}

// antennaKey handles a key in Reception, reporting whether it did.
function antennaKey(e) {
  if (antUI.zone === 'head') {
    const items = antHead();
    const el = document.activeElement;
    const i = items.indexOf(el);
    switch (e.key) {
      case 'ArrowUp': openDock(); break;
      case 'ArrowLeft': if (i > 0) focusEl(items[i - 1]); else settingsZone('tabs'); break;
      case 'ArrowRight': if (i < 0) antZone('head'); else if (i < items.length - 1) focusEl(items[i + 1]); break;
      case 'ArrowDown': if (antRows().length) antZone('table'); break;
      case 'Enter':
        if (el && el.dataset.act === 'measure') measureNow();
        else if (el && el.dataset.act === 'scan') scanChannels();
        else if (i < 0) antZone('head');
        break;
      default: return false;
    }
    // Where the focus comes back to, from the dock.
    const now = document.activeElement;
    if (now && now.dataset.act && $('#antenna').contains(now)) antUI.key = now.dataset.act;
    e.preventDefault();
    return true;
  }
  const rows = antRows();
  let i = rows.indexOf(state.aRF);
  switch (e.key) {
    case 'ArrowUp':
      if (i <= 0) { e.preventDefault(); antZone('head'); return true; }
      i--;
      break;
    case 'ArrowDown': i = Math.min(rows.length - 1, i + 1); break;
    case 'ArrowLeft': e.preventDefault(); settingsZone('tabs'); return true;
    case 'Enter': {
      e.preventDefault();
      const m = state.byRF.get(state.aRF);
      const ch = m && m.channels.length && findChannel(m.channels[0]);
      if (ch) watchChannel(ch);
      return true;
    }
    default: return false;
  }
  e.preventDefault();
  pickRF(rows[Math.max(0, i)]);
  return true;
}

// antMuxes is the RF channels to list: those the scan found (by RF), then
// those a licensed station nearby uses; others hold nothing to see.
function antMuxes() {
  const all = state.antenna ? state.antenna.muxes : [];
  const found = all.filter(muxFound);
  const rest = all.filter((m) => !muxFound(m) && m.stations.length);
  return [...found, ...rest];
}
const muxFound = (m) => m.channels.length > 0;
const antRows = () => antMuxes().map((m) => m.rf);
const muxSig = (m) => sigOf(m && { signal: m.signal, recent: m.history && m.history.windows.length ? m.history.windows[m.history.windows.length - 1] : null });

// tunerName names the tuner: "HDHomeRun FLEX DUO".
const tunerName = (t) => (t && t.name) || 'Tuner';

function renderAntenna() {
  if (!state.report || !state.info || state.info.antenna === false) return;
  const a = $('#antenna');
  const muxes = antMuxes();
  if (!muxes.some((m) => m.rf === state.aRF)) state.aRF = muxes.length ? muxes[0].rf : 0;
  renderTunerCard();
  renderMeasure();
  renderScan();
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
  const scan = muxFound(m) ? "In the tuner's lineup" : 'Not found by the scan';
  const wins = (m.history && m.history.windows) || [];
  const bars = wins.map((w) => {
    const q = w.qualityPct ? w.qualityPct.avg : 0;
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
