'use strict';

// The Recordings view.

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
  f(b, 'next').innerHTML = hints([['LeftRight', 'Arrows', 'skip'], ...(onKeys() ? [[' ', 'Space', 'pauses']] : []), ['Enter', 'Enter', 'controls'], ['Escape', 'Esc', 'returns to live TV']]);
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
// Watched and Delete, above the grid; for one to come, Don't record; for
// one under way, Watch from the start, 30 more minutes, Stop recording and
// Delete; for a series, how many to keep, new episodes only, and Stop.
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
const ARMED = { delete: 'delete', skip: 'skip it', stop: 'stop it', end: 'stop recording', discard: 'delete it' };

// upcomingChips says how an airing to come, or a recording under way,
// stands: recording until when, a conflict or a channel the tuner doesn't
// have (and why), part of a series.
function upcomingChips(it) {
  const series = it.ruleId && (state.dvr.rules || []).some((r) => r.id === it.ruleId && r.kind === 'series');
  const chips = [];
  if (it.status === 'recording') chips.push(`<span class="chip live">Recording until ${clock(Date.parse(it.until))}</span>`);
  if (it.status === 'conflict') chips.push('<span class="chip warn">Conflict</span>');
  if (it.status === 'unavailable') chips.push('<span class="chip">Channel not available</span>');
  if (series) chips.push('<span class="chip">Part of a series</span>');
  const why = it.status === 'conflict' || it.status === 'unavailable' || (it.status === 'recording' && it.detail) ? `<div class="r-why">${esc(it.detail || '')}</div>` : '';
  return chips.join('') + why;
}

// upcomingActs are the actions of the selected airing to come: skip it; or
// of a recording under way: watch it from the start, record half an hour
// more, stop it (keeping what's recorded), or delete it.
function upcomingActs(it, act) {
  if (it.status !== 'recording') return act('skip', "Don't record", it);
  return act('play', 'Watch from the start', it) + act('extend', '30 more minutes', it) + act('end', 'Stop recording', it) + act('discard', 'Delete', it);
}

function renderRecordings() {
  const v = $('#recordings');
  const lists = recLists();
  const tab = state.recTab || 'library';
  const sel = state.recSelBy || (state.recSelBy = { library: 0, upcoming: 0, series: 0 });
  for (const k of Object.keys(sel)) sel[k] = Math.min(sel[k], Math.max(0, lists[k].length - 1));
  const act = (name, label, it, cls = '') => `<button type="button" class="act fx${cls ? ` ${cls}` : ''}${armed(name, it) ? ' armed' : ''}" data-act="${name}">${armed(name, it) ? `${onRemote() ? 'OK' : 'Press'} again to ${ARMED[name]}` : label}</button>`;

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
      ${cur.status !== 'failed' && cur.detail ? `<p class="rd-note">${esc(cur.detail)}</p>` : ''}
      <div class="rd-actions">${cur.status === 'failed' ? '' : act('play', resumable(cur) ? `Resume at ${mmss(cur.position)}` : 'Play', cur)}${resumable(cur) ? act('restart', 'From the start', cur) : ''}${cur.status === 'failed' ? '' : act('watched', cur.watched ? 'Mark unwatched' : 'Mark watched', cur)}${act('delete', 'Delete', cur)}</div>
      <div class="rd-hint">${recHint(cur)}</div>
    </div>
    <div class="rd-art" style="${cur.image ? `background-image:url('${esc(imageURL(cur.image))}')` : ''}"></div>`
    : '<div class="rd-empty">Nothing recorded yet. In the guide, OK on a program to come records it, or every episode.</div><div></div>';
  f(v, 'library').innerHTML = lib.map((it, i) => `
    <article class="r-card ${i === sel.library ? 'sel' : ''} ${it.status === 'failed' ? 'failed' : ''} ${it.watched ? 'watched' : ''}" data-i="${i}">
      <div class="r-art" style="${it.image ? `background-image:url('${esc(imageURL(it.image))}')` : ''}">${it.image ? '' : `<span>${esc(it.title)}</span>`}
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
        ${upcomingChips(it)}</div>
      <div class="r-acts">${i === sel.upcoming ? upcomingActs(it, act) : ''}</div>
    </li>`).join('') : '<li class="r-empty">Nothing scheduled.</li>';

  f(v, 'rules').innerHTML = lists.series.length ? lists.series.map((r, i) => `
    <li class="r-item ${i === sel.series ? 'sel' : ''}" data-i="${i}">
      <div class="r-what"><div class="r-name">${esc(r.title)}</div><div class="r-sub">${esc(r.channel)}  |  ${r.newOnly ? 'New episodes' : 'Every episode'}  |  ${r.keep ? `Keep newest ${r.keep}` : 'Keep all'}</div></div>
      <div class="r-acts">${i === sel.series ? act('keep', r.keep ? `Keeps newest ${r.keep}` : 'Keeps all', r) + act('newonly', r.newOnly ? 'New episodes only' : 'Every episode', r) + act('stop', 'Stop series', r) : ''}</div>
    </li>`).join('') : '<li class="r-empty">No series recordings. In the guide, OK on a show to come, then Record series.</li>';
  if (lists.series.length && onKeys()) f(v, 'rules').insertAdjacentHTML('beforeend', '<li class="r-hint">K changes how many to keep  |  N new episodes only  |  Delete stops the series</li>');
  recRefocus();
}

function recHint(cur) {
  if (onRemote()) return 'OK for the actions, Left for the other lists';
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
      playOnDemand(it, resumable(it) ? it.position : 0);
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
    case 'play': return playOnDemand(it, it.status === 'recording' ? 0 : resumable(it) ? it.position : 0);
    case 'extend': return api().ExtendRecording(it.id, 30).then((until) => {
      toast(`Recording ${it.title} until ${clock(Date.parse(until))}`);
      return loadDVR();
    }).catch(fail);
    case 'restart': return playOnDemand(it, 0);
    case 'watched': return api().MarkWatched(it.id, !it.watched).then(loadDVR).catch(fail);
    case 'keep': case 'newonly': {
      const steps = [0, 3, 5, 10];
      const u = act === 'keep' ? { keep: steps[(steps.indexOf(it.keep || 0) + 1) % steps.length], newOnly: !!it.newOnly } : { keep: it.keep || 0, newOnly: !it.newOnly };
      return api().UpdateRule(it.id, u).then(loadDVR).catch(fail);
    }
    case 'delete': case 'skip': case 'stop': case 'end': case 'discard': {
      if (!it.id) return null;
      if (!armed(act, it)) {
        pendingDelete = `${act}:${it.id}`;
        clearTimeout(pendingTimer);
        pendingTimer = setTimeout(() => { pendingDelete = ''; if (state.view === 'recordings') renderRecordings(); }, 4000);
        const what = act === 'delete' || act === 'discard' ? `delete ${it.title}` : act === 'stop' ? `stop recording ${it.title}` : act === 'end' ? `stop recording ${it.title}, keeping what's recorded` : `skip ${it.title}`;
        if (recUI.zone !== 'actions') toast(`Press Delete again to ${what}`);
        return renderRecordings();
      }
      pendingDelete = '';
      clearTimeout(pendingTimer);
      const done = act === 'stop' ? api().DeleteRule(it.id) : act === 'end' ? api().StopRecording(it.id) : api().DeleteRecording(it.id);
      return done.then(() => {
        toast(act === 'delete' || act === 'discard' ? 'Deleted' : act === 'stop' ? 'Series recording stopped' : act === 'end' ? `Stopped. ${it.title} is in the library` : 'Removed from the schedule');
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
      if (tab === 'library' && e.shiftKey) return playOnDemand(list[sel[tab]], 0);
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

