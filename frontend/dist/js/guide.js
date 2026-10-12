'use strict';

// The guide.

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
      case 'record': return openRecordPick(ch, selectedProgram(list));
      case 'once': state.recPick = ''; return recordSelected('once');
      case 'series': state.recPick = ''; return recordSelected('series', false);
      case 'new': state.recPick = ''; return recordSelected('series', true);
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
    const picked = () => `${state.gRow}|${(selectedProgram(guideChannels()) || {})._s}`;
    const was = guideUI.zone === 'grid' ? picked() : '';
    guideZone('grid');
    state.gRow = Number(cell.dataset.row);
    if (cell.dataset.t) state.gTime = Number(cell.dataset.t);
    renderGuide();
    if (tappedAgain(was, picked())) guideActivate();
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
    // Up and Down wrap round the channels, so the last ones are a press
    // away. The dock is Left from the earliest program, and the filters
    // Down from the dock.
    case 'ArrowUp': state.gRow = list.length ? (state.gRow - 1 + list.length) % list.length : 0; break;
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
    case 'F': e.preventDefault(); toggleFavorite(list[state.gRow]); return;
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
      ${ch.logoDark ? `<div class="logo own" style="background-image:url('${esc(ch.logoDark)}')"></div>`
    : ch.logo ? `<div class="logo${ch.own ? ' own' : ''}" style="background-image:url('${esc(ch.logo)}')"></div>` : '<div></div>'}
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
      const recCls = rec ? (rec.status === 'unavailable' || rec.status === 'conflict' ? 'rec rec-x' : rec.status === 'recording' ? 'rec rec-on' : 'rec') : '';
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
  if (onRemote()) return 'Filters: Left to the top bar, then Down';
  return hints([['i', 'I', 'more'], ['', 'Shift F', 'favorite'], ['h', 'H', 'hide'], ['[', '[ ]', 'filter']]);
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
  // Laid out, it may need less shown beside it.
  requestAnimationFrame(fitGuideDetail);
  const now = Date.now();
  const airing = !p || p._s <= now;
  const act = (name, label, cls = '') => `<button type="button" class="act fx${cls ? ` ${cls}` : ''}" data-act="${name}">${label}</button>`;
  const parts = [];
  if (airing) parts.push(act('watch', !p || now < p._e ? 'Watch' : `Watch ${esc(ch.number)}`));
  if (state.info.dvr && p && p._e > now && !ch.weather && !ch.custom) {
    const it = state.dvrKeys.get(airingKey(ch, p));
    const rule = seriesRuleFor(ch, p);
    if (it) {
      const label = { recording: 'Recording now', unavailable: 'Channel not available', conflict: 'Conflict: every tuner records then' }[it.status] || 'Recording scheduled';
      parts.push(`<span class="chip ${it.status === 'unavailable' || it.status === 'conflict' ? 'warn' : 'live'}">${label}</span>`);
      parts.push(act('once', it.status === 'recording' ? 'Stop recording' : 'Cancel'));
    } else if (!rule && p.seriesId) {
      // One Record, which opens to this airing, every episode or new ones;
      // another program closes it.
      if (state.recPick !== airingKey(ch, p)) state.recPick = '';
      parts.push(state.recPick === airingKey(ch, p)
        ? act('once', '<i></i>This one', 'rec') + act('series', 'Every episode') + act('new', 'New only')
        : act('record', '<i></i>Record', 'rec'));
    } else {
      parts.push(act('once', '<i></i>Record', 'rec'));
    }
    if (rule) {
      parts.push(`<span class="chip new">Series${rule.newOnly ? ', new only' : ''}</span>${act('series', 'Stop series')}`);
    }
  }
  if (!airing) parts.push(act('watch', `Watch ${esc(ch.number)}`));
  parts.push(act('fav', isFav(ch) ? 'Unfavorite' : 'Favorite'));
  parts.push(act('hide', 'Hide channel'));
  box.innerHTML = parts.join('');
  guideRefocus();
}

// fitGuideDetail leaves out what of the program's details doesn't fit
// above the grid at this window and interface size: its cast first, then
// its description, its tags, its episode line and the hint. The title and the
// actions always show, the actions on one row that scrolls sideways when
// even that isn't enough.
function fitGuideDetail() {
  const g = $('#guide');
  const top = $('.g-top', g);
  const box = $('.g-detail', g);
  // The picture needs the details to keep a readable width beside it.
  top.classList.toggle('no-art', top.clientWidth < 1100);
  const parts = ['cast', 'desc', 'meta', 'ep', 'hint'].map((k) => f(g, k));
  for (const el of parts) el.classList.remove('gd-gone');
  box.classList.remove('one-row');
  const cs = getComputedStyle(top);
  const room = top.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
  for (const el of parts) {
    if (box.offsetHeight <= room) return;
    el.classList.add('gd-gone');
  }
  if (box.offsetHeight > room) box.classList.add('one-row');
}

// openRecordPick opens Record into its three choices, the first focused.
function openRecordPick(ch, p) {
  if (!ch || !p) return;
  state.recPick = airingKey(ch, p);
  renderGuideActions(ch, p);
  focusEl(f($('#guide'), 'actions').querySelector('[data-act="once"]'));
}

// closeRecordPick folds the choices back into Record, focused.
function closeRecordPick() {
  state.recPick = '';
  const list = guideChannels();
  renderGuideActions(list[state.gRow], selectedProgram(list));
  const rec = f($('#guide'), 'actions').querySelector('[data-act="record"]');
  if (rec && guideUI.zone === 'actions') focusEl(rec);
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
    if (kind === 'once' && it && it.status === 'recording') {
      await api().StopRecording(it.id);
      toast(`Stopped recording ${p.title}; what was recorded is in the library`);
    } else if (kind === 'once' && it) {
      await api().DeleteRecording(it.id);
      toast(`Won't record ${p.title}`);
    } else if (kind === 'series' && rule) {
      await api().DeleteRule(rule.id);
      toast(`Stopped the series recording of ${p.title}`);
    } else if (kind === 'series' && !p.seriesId) {
      return toast(`${p.title} has no series information. Press R to record this airing.`);
    } else {
      await api().Record({ kind, channel: ch.number, callSign: ch.callSign, start: p.start, newOnly: !!newOnly });
      const aired = Math.round((Date.now() - p._s) / MIN);
      toast(kind === 'series'
        ? `Recording ${newOnly ? 'new episodes' : 'every episode'} of ${p.title} on ${ch.number}`
        : aired >= 1 ? `Recording ${p.title} from now; the first ${aired} minute${aired > 1 ? 's' : ''} already aired`
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
    for (const k of ['kicker', 'title', 'ep', 'meta', 'cast', 'desc', 'actions', 'hint']) f(g, k).textContent = '';
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
  f(g, 'cast').textContent = castLine(p);
  f(g, 'desc').textContent = p ? p.description || '' : ch.description || '';
  const live = !p || p._s <= now;
  const ok = keyHint('Enter', 'Enter');
  const when = !live ? `Starts in ${humanMinutes(Math.round((p._s - now) / MIN))}  |  ${ok} for more` : p && p._e <= now ? `Ended ${clock(p._e)}  |  ${ok} to watch ${esc(ch.number)}` : `${ok} to watch`;
  const more = !live ? '' : prompts.pad ? hints([['i', 'I', 'for more']]) : `hold ${ok} for more`;
  const rec = state.info.dvr && p && p._e > now && !ch.weather && !ch.custom && onKeys() ? '  |  R record, Shift R series, N new only' : '';
  f(g, 'hint').innerHTML = `${when}${more ? `, ${more}` : ''}${rec}`;
  renderGuideActions(ch, p);
  const art = f(g, 'art');
  if (p && p.image) { art.style.backgroundImage = `url('${imageURL(p.image)}')`; art.classList.add('has'); } else { art.style.backgroundImage = ''; art.classList.remove('has'); }
}
