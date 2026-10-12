'use strict';

// Podcasts, an app like Weather: video podcasts from YouTube, played on
// demand, and the Podcasts tab of Settings where shows are added.

// ---------- podcasts ----------
// The view is the Recordings library's: the rail lists Latest (every
// show's newest), Continue (episodes part watched) and each show; beside it
// the episode picked, above a grid of the list's episodes. Its parts take
// the focus in turn (data-zone): the grid, the rail's menu (Left from the
// grid's left edge), and the episode's actions (OK): Play or Resume, From
// the start, Watched. Back goes from the actions to the grid.
const podUI = { zone: 'grid', act: '', list: 'latest', sel: {}, at: 0, timer: 0, searching: false };

// asEpisode marks an episode for the player (state.recording), which plays
// recordings too.
const asEpisode = (ep) => ({ ...ep, kind: 'episode' });

async function loadPodcasts() {
  if (!state.info || !state.info.podcasts) return;
  try {
    state.podcasts = await api().Podcasts();
  } catch (e) {
    log('warn', `podcasts: ${e}`);
    return;
  }
  podUI.at = Date.now();
  if (state.view === 'podcasts') renderPodcasts();
  if (state.view === 'settings' && settingsUI.tab === 'podcasts') renderSettings();
  // While shows are listed, look again soon.
  clearTimeout(podUI.timer);
  if (state.podcasts && state.podcasts.updating) podUI.timer = setTimeout(loadPodcasts, 5000);
}

const podShows = () => (state.podcasts && state.podcasts.shows) || [];
const showOf = (ep) => podShows().find((s) => s.id === ep.showId);
const unwatched = (eps) => eps.filter((ep) => !ep.watched).length;

// podLists are the rail's lists: [id, label, episodes], newest first.
function podLists() {
  const shows = podShows();
  const all = shows.flatMap((s) => s.episodes || []).sort((a, b) => Date.parse(b.published) - Date.parse(a.published));
  const lists = [['latest', 'Latest', all.slice(0, 60)]];
  const going = all.filter(resumable);
  if (going.length) lists.push(['continue', 'Continue', going]);
  for (const s of shows) lists.push([s.id, s.title || 'Show', s.episodes || []]);
  return lists;
}

function openPodcasts() {
  renderPodcasts();
  const list = podList();
  podZone(list && list[2].length ? 'grid' : 'menu');
  if (Date.now() - podUI.at > MIN) loadPodcasts();
}

function podList() {
  const lists = podLists();
  return lists.find(([k]) => k === podUI.list) || lists[0];
}

function podZone(zone, act = '') {
  podUI.zone = zone;
  $('#podcasts').dataset.zone = zone;
  if (act) podUI.act = act;
  if (state.dock >= 0 || state.view !== 'podcasts') return;
  if (zone === 'grid') return blurIn($('#podcasts'));
  if (zone === 'menu') return void focusEl($(`#podcasts [data-list="${CSS.escape(podList()[0])}"]`));
  const acts = $$('#podcasts .rd-actions .act.fx').filter(isShown);
  if (!acts.length) return podZone('grid');
  focusEl(acts.find((b) => b.dataset.act === podUI.act) || acts[0]);
}

function podRefocus() {
  if (state.view !== 'podcasts' || state.dock >= 0 || podUI.zone === 'grid') return;
  if (!$('#podcasts').contains(document.activeElement) || !isShown(document.activeElement)) podZone(podUI.zone);
}

// When an episode came out: "Today", "Yesterday", "Mon", or "Oct 3".
function published(t) {
  const d = new Date(t);
  if (Number.isNaN(d.getTime())) return '';
  const days = Math.floor((new Date().setHours(0, 0, 0, 0) - new Date(d).setHours(0, 0, 0, 0)) / (24 * 3600 * 1000));
  if (days <= 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return d.toLocaleDateString([], { weekday: 'long' });
  return d.toLocaleDateString([], { month: 'short', day: 'numeric', ...(days > 300 ? { year: 'numeric' } : {}) });
}

const runtime = (sec) => (sec ? (sec >= 3600 ? `${Math.floor(sec / 3600)} h ${Math.round((sec % 3600) / 60)} min` : `${Math.round(sec / 60)} min`) : '');

function renderPodcasts() {
  const v = $('#podcasts');
  const lists = podLists();
  const cur = podList();
  podUI.list = cur[0];
  const eps = cur[2];
  const at = Math.min(podUI.sel[cur[0]] || 0, Math.max(0, eps.length - 1));
  podUI.sel[cur[0]] = at;

  f(v, 'menu').innerHTML = lists.map(([k, label, list]) => {
    const n = k === 'continue' ? list.length : unwatched(list);
    return `<button type="button" class="rm-item fx ${k === cur[0] ? 'on' : ''}" data-list="${esc(k)}"><span class="rm-label">${esc(label)}</span><span class="rm-count">${n || ''}</span></button>`;
  }).join('');

  const ep = eps[at];
  const show = ep && showOf(ep);
  const act = (name, label) => `<button type="button" class="act fx" data-act="${name}">${label}</button>`;
  const shows = podShows();
  f(v, 'detail').innerHTML = ep ? `
    <div>
      <div class="rd-kicker">${esc(show ? show.title : '')}  |  ${published(ep.published)}${ep.duration ? `  |  ${runtime(ep.duration)}` : ''}</div>
      <div class="rd-title">${esc(ep.title)}</div>
      <p class="rd-desc">${esc(ep.description || '')}</p>
      <div class="rd-actions">${act('play', resumable(ep) ? `Resume at ${mmss(ep.position)}` : 'Play')}${resumable(ep) ? act('restart', 'From the start') : ''}${act('watched', ep.watched ? 'Mark unwatched' : 'Mark watched')}</div>
      <div class="rd-hint">${podHint(ep)}</div>
    </div>
    <div class="rd-art" style="${ep.thumbnail ? `background-image:url('${esc(imageURL(ep.thumbnail))}')` : ''}"></div>`
    : `<div class="rd-empty">${shows.length ? (state.podcasts && state.podcasts.updating ? 'Finding episodes...' : 'No episodes here yet.') : 'No shows yet. More, Podcasts adds them.'}</div><div></div>`;

  f(v, 'grid').innerHTML = eps.map((e, i) => {
    const s = cur[0] === 'latest' || cur[0] === 'continue' ? showOf(e) : null;
    return `
    <article class="r-card ${i === at ? 'sel' : ''} ${e.watched ? 'watched' : ''}" data-i="${i}">
      <div class="r-art" style="${e.thumbnail ? `background-image:url('${esc(imageURL(e.thumbnail))}')` : ''}">${e.thumbnail ? '' : `<span>${esc(e.title)}</span>`}
        ${resumable(e) ? `<div class="r-prog"><i style="width:${Math.min(100, (e.position / e.duration) * 100)}%"></i></div>` : ''}
        ${e.watched ? '<b class="r-badge">Watched</b>' : e.duration ? `<b class="r-badge p-len">${mmss(e.duration)}</b>` : ''}</div>
      <div class="r-meta">
        <div class="r-name p-name">${esc(e.title)}</div>
        <div class="r-when">${published(e.published)}${s ? `  |  ${esc(s.title)}` : ''}</div>
      </div>
    </article>`;
  }).join('') || (cur[0] !== 'latest' && cur[0] !== 'continue' && (shows.find((x) => x.id === cur[0]) || {}).error
    ? `<p class="r-empty">${esc(shows.find((x) => x.id === cur[0]).error)}</p>` : '');
  podRefocus();
}

function podHint(ep) {
  if (onRemote()) return 'OK for the actions, Left for the shows';
  return hints([['Enter', 'Enter', 'for the actions'], ...(resumable(ep) ? [['', 'Shift Enter', 'plays from the start']] : []), ['w', 'W', ep.watched ? 'unwatched' : 'watched']]);
}

function wirePodcasts() {
  const v = $('#podcasts');
  f(v, 'menu').addEventListener('click', (e) => {
    const b = e.target.closest('[data-list]');
    if (!b) return;
    podUI.list = b.dataset.list;
    renderPodcasts();
  });
  v.addEventListener('click', (e) => {
    const b = e.target.closest('[data-act]');
    if (b) return podAct(b.dataset.act);
    const card = e.target.closest('[data-i]');
    if (card) {
      const was = podUI.sel[podUI.list] || 0;
      podUI.sel[podUI.list] = Number(card.dataset.i);
      renderPodcasts();
      if (tappedAgain(was, podUI.sel[podUI.list])) podAct('play');
    }
    return null;
  });
  v.addEventListener('focusin', (e) => {
    const b = e.target.closest('[data-act]');
    if (b) podUI.act = b.dataset.act;
  });
  f(v, 'grid').addEventListener('dblclick', (e) => {
    const card = e.target.closest('[data-i]');
    if (!card) return;
    const ep = podList()[2][Number(card.dataset.i)];
    if (ep) playOnDemand(asEpisode(ep), resumable(ep) ? ep.position : 0);
  });
}

const podSelected = () => {
  const cur = podList();
  return cur[2][podUI.sel[cur[0]] || 0];
};

async function podAct(act) {
  const ep = podSelected();
  if (!ep) return;
  if (act === 'play') return playOnDemand(asEpisode(ep), resumable(ep) ? ep.position : 0);
  if (act === 'restart') return playOnDemand(asEpisode(ep), 0);
  if (act === 'watched') {
    const watched = !ep.watched;
    ep.watched = watched;
    if (!watched) ep.position = 0;
    renderPodcasts();
    try {
      await api().EpisodeProgress(ep.id, watched ? ep.duration || 0 : 0, ep.duration || 0, watched);
    } catch (e) {
      toast(String(e && e.message ? e.message : e), 5000);
    }
  }
  return undefined;
}

// podcastsKey handles a key in Podcasts, as recordingsKey does in the
// library.
function podcastsKey(e) {
  const lists = podLists();
  if (e.key === '[' || e.key === ']') {
    const i = lists.findIndex(([k]) => k === podUI.list);
    podUI.list = lists[(i + (e.key === ']' ? 1 : lists.length - 1)) % lists.length][0];
    return renderPodcasts();
  }
  if (podUI.zone === 'menu') return podMenuKey(e, lists);
  if (podUI.zone === 'actions' && podActionsKey(e)) return undefined;
  const eps = podList()[2];
  const sel = podUI.sel;
  const k = podUI.list;
  const cols = Math.max(1, Math.round(f($('#podcasts'), 'grid').clientWidth / 280));
  const i = sel[k] || 0;
  if (e.key === 'ArrowUp' && (!eps.length || i < cols)) { e.preventDefault(); return openDock(); }
  if (e.key === 'ArrowLeft' && i % cols === 0) { e.preventDefault(); return podZone('menu'); }
  if (!eps.length) return undefined;
  switch (e.key) {
    case 'ArrowRight': sel[k] = Math.min(eps.length - 1, i + 1); break;
    case 'ArrowLeft': sel[k] = Math.max(0, i - 1); break;
    case 'ArrowDown': sel[k] = Math.min(eps.length - 1, i + cols); break;
    case 'ArrowUp': sel[k] = Math.max(0, i - cols); break;
    case 'Enter':
      e.preventDefault();
      if (e.shiftKey) return podAct('restart');
      return podZone('actions', 'play');
    case 'w': case 'W': return podAct('watched');
    default: return undefined;
  }
  e.preventDefault();
  renderPodcasts();
  const el = $(`#podcasts .r-grid [data-i="${sel[k]}"]`);
  if (el) el.scrollIntoView({ block: 'nearest' });
  return undefined;
}

// podMenuKey: Up and Down pick a list, Right or OK goes into it, Left (or
// Up from the first) to the dock.
function podMenuKey(e, lists) {
  const i = lists.findIndex(([k]) => k === podUI.list);
  switch (e.key) {
    case 'ArrowUp': case 'ArrowDown': {
      const j = i + (e.key === 'ArrowUp' ? -1 : 1);
      if (j < 0) return openDock();
      if (j >= lists.length) break;
      podUI.list = lists[j][0];
      renderPodcasts();
      podZone('menu');
      break;
    }
    case 'ArrowRight': case 'Enter':
      if (podList()[2].length) podZone('grid');
      break;
    case 'ArrowLeft': return openDock();
    default: return undefined;
  }
  e.preventDefault();
  return undefined;
}

// podActionsKey: Left and Right along the actions, OK presses, Down (or
// Back) to the grid, Up to the dock.
function podActionsKey(e) {
  const acts = $$('#podcasts .rd-actions .act.fx').filter(isShown);
  const el = document.activeElement;
  switch (e.key) {
    case 'ArrowLeft': case 'ArrowRight': {
      const next = neighbor(acts, el, e.key === 'ArrowLeft' ? -1 : 1);
      if (next) focusEl(next);
      break;
    }
    case 'ArrowUp': openDock(); break;
    case 'ArrowDown': podZone('grid'); break;
    case 'Enter':
      if (acts.includes(el)) el.click(); else podZone('actions');
      break;
    default: return false;
  }
  e.preventDefault();
  return true;
}

// renderEpisodeBanner is the banner for an episode playing.
function renderEpisodeBanner(b, ep) {
  const show = showOf(ep);
  showChannelLogo(b, null);
  f(b, 'num').classList.remove('long');
  f(b, 'num').textContent = 'POD';
  f(b, 'call').textContent = show ? show.title : '';
  f(b, 'net').textContent = published(ep.published);
  b.classList.remove('warn', 'detail');
  f(b, 'time').textContent = ep.duration ? runtime(ep.duration) : '';
  f(b, 'chips').innerHTML = '';
  f(b, 'title').textContent = ep.title;
  f(b, 'ep').textContent = '';
  f(b, 'cast').textContent = '';
  const pos = (state.recOffset || 0) + (video.currentTime || 0);
  f(b, 'progress').style.width = ep.duration ? `${Math.min(100, (pos / ep.duration) * 100)}%` : '0';
  f(b, 'desc').textContent = ep.description || '';
  f(b, 'next').innerHTML = hints([['LeftRight', 'Arrows', 'skip'], ...(onKeys() ? [[' ', 'Space', 'pauses']] : []), ['Enter', 'Enter', 'controls'], ['Escape', 'Esc', 'returns to live TV']]);
  f(b, 'meter').innerHTML = '';
  f(b, 'tier').textContent = 'YouTube';
  f(b, 'tier').style.color = '';
  f(b, 'tx').textContent = `Through ${state.info.name}`;
  f(b, 'atsc3').textContent = '';
  f(b, 'note').textContent = '';
}

// ---------- podcasts in settings ----------
// Settings, Podcasts: the shows, each with Remove (pressed twice), and Add
// a show, which takes a search or a YouTube address and lists what it
// finds to add.
let podRemoveArmed = '';

function podcastRows(add) {
  if (!state.info || !state.info.podcasts) return;
  const typeIt = androidTV ? 'OK brings up the keyboard' : 'Type it with a keyboard';
  add('Podcasts', { id: 'podsearch', kind: 'text', label: 'Add a show', sub: `A name to search YouTube for, or a channel's or playlist's address. ${typeIt}`, name: 'podsearch', text: '', placeholder: 'Search or paste an address' });
  for (const s of podShows()) {
    const n = (s.episodes || []).length;
    const armed = podRemoveArmed === s.id;
    add('Podcasts', {
      id: `show:${s.id}`, kind: 'action', verb: armed ? 'removes it' : 'asks to remove it', label: s.title || s.url,
      sub: s.error || `${n} episode${n === 1 ? '' : 's'}, ${unwatched(s.episodes || [])} unwatched`,
      value: armed ? 'Press again to remove' : 'Remove', run: () => removeShow(s),
    });
  }
}

async function removeShow(s) {
  if (podRemoveArmed !== s.id) {
    podRemoveArmed = s.id;
    setTimeout(() => { if (podRemoveArmed === s.id) { podRemoveArmed = ''; renderSettings(); } }, 4000);
    return renderSettings();
  }
  podRemoveArmed = '';
  await api().RemovePodcast(s.id);
  toast(`Removed ${s.title}`, 1800);
  await loadPodcasts();
  return undefined;
}

// searchShows looks for shows to add: an address is offered as it is, a
// name is searched for on YouTube.
async function searchShows(q) {
  q = q.trim();
  if (!q) return;
  if (/^https?:\/\//.test(q) || /^(www\.)?youtube\.com\//.test(q)) {
    state.podFound = [{ id: q, title: q, url: q.startsWith('http') ? q : `https://${q}`, description: 'This address' }];
  } else {
    toast(`Searching YouTube for ${q}...`, 15000);
    try {
      state.podFound = await api().SearchPodcasts(q);
    } catch (e) {
      return toast(String(e && e.message ? e.message : e), 6000);
    }
    toast(`${state.podFound.length} found`, 1500);
  }
  openSettingsList('podfound');
}

function podFoundRows(add) {
  const found = state.podFound || [];
  for (const c of found) {
    const have = podShows().some((s) => s.url === c.url);
    add({
      id: c.url, kind: have ? 'info' : 'action', verb: 'adds it', label: c.title,
      sub: [c.subscribers, c.description].filter(Boolean).join('. ').slice(0, 140),
      value: have ? 'Added' : 'Add', run: () => addShow(c),
    });
  }
  if (!found.length) add({ id: 'none', kind: 'info', label: 'Nothing found', sub: 'Try other words, or paste the show\'s address' });
}

async function addShow(c) {
  toast(`Adding ${c.title}...`, 30000);
  try {
    await api().AddPodcast(c.url, 0);
  } catch (e) {
    return toast(String(e && e.message ? e.message : e), 6000);
  }
  toast(`Added ${c.title}`, 2000);
  closeSettingsList();
  await loadPodcasts();
  return undefined;
}
