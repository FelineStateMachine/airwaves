'use strict';

// More: Settings and its tabs, what the rows do, the interface size, favorites and hidden channels.

// ---------- settings ----------
// More is Settings: a tab for each area in the rail, and beside it the
// tab's rows, or Reception's measurements. For a remote or a controller
// first (a mouse and a keyboard work too). On the tabs Up and Down pick
// one, Right or OK goes into it. In a list Up and Down move, Left and Right
// change a choice or a switch, Enter switches, runs or opens a list (hidden
// channels, the controls, data sources), and Left on a row with nothing to
// change goes back to the tabs. Page Up and Down (a controller's LB and
// RB) change tab anywhere, Up from the top moves to the dock, and Esc goes
// back. Changes are saved as they're made. The server's address and token
// need a keyboard: Enter on their row puts the cursor in them.
const settingsUI = { tab: 'display', zone: 'tabs', at: 0, sub: null, from: 0, rows: [], pending: {}, timers: {}, tested: '' };

// SETTINGS_TABS are the areas in the rail's order, [id, label]; a row's
// section is its tab's label.
const SETTINGS_TABS = [['display', 'Display'], ['watching', 'Watching'], ['channels', 'Channels'], ['reception', 'Reception'],
  ['recordings', 'Recordings'], ['server', 'Server'], ['about', 'About']];

const AUDIO_LANGS = [['', 'As broadcast'], ['en', 'English'], ['es', 'Spanish'], ['fr', 'French'], ['ko', 'Korean'],
  ['vi', 'Vietnamese'], ['zh', 'Chinese'], ['ru', 'Russian'], ['pt', 'Portuguese']];
const GUIDE_HOURS = [6, 12, 24, 36, 48, 72];
const WATCHED = [[0, 'Keep them'], [1, 'Delete after a day'], [7, 'Delete after a week'], [30, 'Delete after a month']];
const SETTINGS_LISTS = { hidden: 'Hidden channels', controls: 'Remote, controller and keys', sources: 'Data sources' };
// How to hide a channel, with the keys there are.
const HIDE_HOW = () => (onRemote() ? 'In the guide, hold OK on a channel\'s program: Hide channel' : 'In the guide, H hides a channel');

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
  f(v, 'tabs').addEventListener('click', (e) => {
    const b = e.target.closest('[data-tab]');
    if (b) pickSettingsTab(b.dataset.tab, 'list');
  });
}

// settingsTabs are the tabs there are: Reception with an antenna, and
// those with rows.
function settingsTabs() {
  const sections = new Set(settingsRows().map((r) => r.section));
  return SETTINGS_TABS.filter(([k, label]) => (k === 'reception' ? !!state.info && state.info.antenna !== false : sections.has(label)));
}

const receptionShown = () => state.view === 'settings' && settingsUI.tab === 'reception';

// openSettings shows a tab, the one left open when none is asked for, with
// the focus on the tabs or in the tab (zone 'list').
function openSettings(tab = settingsUI.tab, zone = 'tabs') {
  settingsUI.sub = null;
  pickSettingsTab(tab, zone);
}

function pickSettingsTab(tab, zone = settingsUI.zone) {
  const tabs = settingsTabs();
  if (!tabs.some(([k]) => k === tab)) tab = tabs.length ? tabs[0][0] : 'display';
  if (tab !== settingsUI.tab) {
    settingsUI.at = 0;
    settingsUI.sub = null;
  }
  settingsUI.tab = tab;
  const reception = tab === 'reception';
  $('#settings').dataset.tab = tab;
  f($('#settings'), 'list').hidden = reception;
  $('#antenna').hidden = !reception;
  renderSettings();
  if (reception) {
    renderAntenna();
    signalAt = 0; // fresh measurements
    if (zone === 'list') antUI.zone = antRows().length ? 'table' : 'head';
  } else f($('#settings'), 'list').scrollTop = 0;
  settingsZone(zone);
}

// settingsZone puts the focus on the tabs, or in the tab.
function settingsZone(zone) {
  settingsUI.zone = zone;
  $('#settings').dataset.zone = zone;
  if (state.dock >= 0 || state.view !== 'settings') return;
  if (zone === 'tabs') {
    blurIn($('#settings .s-main'));
    focusEl($(`#settings [data-tab="${settingsUI.tab}"]`));
  } else if (settingsUI.tab === 'reception') {
    blurIn($('#settings .rail'));
    antZone(antUI.zone);
  } else {
    blurIn($('#settings .rail'));
    moveSetting(settingsUI.at);
  }
  renderSettingsHint();
}

function settingsRefocus() {
  if (state.view !== 'settings' || state.dock >= 0) return;
  settingsZone(settingsUI.zone);
}

// stepSettingsTab moves to the tab before or after, staying in tabs or
// in the tab.
function stepSettingsTab(dir) {
  const tabs = settingsTabs();
  const i = tabs.findIndex(([k]) => k === settingsUI.tab);
  const next = tabs[Math.max(0, Math.min(tabs.length - 1, i + dir))];
  if (next && next[0] !== settingsUI.tab) pickSettingsTab(next[0]);
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
  const auto = PLATFORMS.find(([k]) => k === detectedPlatform)[1];
  add('Display', {
    id: 'platform', kind: 'choice', label: 'Layout', subFor: (k) => PLATFORM_SUBS[k || detectedPlatform],
    choices: [['', `Auto, ${auto}`], ...PLATFORMS], get: () => localStorage.getItem(platformKey) || '', set: setPlatform,
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
    add('About', { id: 'tuner', kind: 'info', label: 'Tuner', value: t.tuners ? tunerName(t) : what, sub: [t.tuners && what, t.model].filter(Boolean).join(', ') });
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
  renderSettingsTabs();
  const label = (SETTINGS_TABS.find(([k]) => k === settingsUI.tab) || [])[1];
  const rows = settingsUI.rows = settingsUI.sub ? settingsList(settingsUI.sub) : settingsRows().filter((r) => r.section === label);
  settingsUI.at = Math.min(settingsUI.at, Math.max(0, rows.length - 1));
  let html = '';
  if (rows.length) html += `<h2 class="s-head">${esc(rows[0].section)}</h2><div class="s-group">${rows.map(settingsRowHTML).join('')}</div>`;
  if (settingsUI.sub === 'controls' && platform === 'tv') {
    html += '<p class="s-note">On the remote: OK brings up the controls over the picture, with Guide first, so OK twice opens the guide. In them Left and Right move along a row, Up and Down between rows: the timeline (Left and Right skip), the buttons (play, captions, record, last channel and more), and the views at the top. On live TV Up and Down change channel, and Left and Right skip. In the guide OK watches what\'s on, and holding OK offers more: record, series, favorite, hide. Back goes back. Remotes with them: digits tune a channel, Channel up and down change channel, Play/Pause pauses.</p>';
  } else if (settingsUI.sub === 'controls') {
    html += '<p class="s-note">Enter on TV brings up the controls, with the guide, captions, recording, the last channel and the views; Esc puts them away. Digits tune a channel. The remote\'s Fast forward and Rewind skip, Record records. Keyboard only: L last channel, C captions, V audio track, Shift F favorite, R record, D recordings, A reception, F full screen. In the guide hold Enter, or press I, for a program\'s actions. In Steam, give Airwaves the Gamepad controller layout, not a keyboard one.</p>';
  }
  const keep = list.scrollTop;
  list.innerHTML = html;
  list.scrollTop = keep;
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

// renderSettingsTabs lists the tabs in the rail, the open one marked.
function renderSettingsTabs() {
  const box = f($('#settings'), 'tabs');
  const html = settingsTabs().map(([k, label]) => `<button type="button" class="rm-item fx ${k === settingsUI.tab ? 'on' : ''}" data-tab="${k}" role="tab" aria-selected="${k === settingsUI.tab}">
    <span class="rm-label">${esc(label)}</span></button>`).join('');
  if (box.innerHTML === html) return;
  const had = box.contains(document.activeElement);
  box.innerHTML = html;
  if (had) focusEl($(`[data-tab="${settingsUI.tab}"]`, box), false);
}

function renderSettingsHint() {
  const hint = f($('#settings'), 'hint');
  if (settingsUI.zone === 'tabs') {
    hint.innerHTML = hints([['UpDown', 'Up, Down', 'picks'], ['Enter', 'Enter', 'goes in'], ['Escape', 'Esc', 'leaves']]);
    return;
  }
  if (settingsUI.tab === 'reception') {
    hint.innerHTML = hints([['Arrows', 'Arrows', 'move'], ['Enter', 'Enter', 'measures, or watches the RF channel picked'], ['Escape', 'Esc', 'back to the tabs']]);
    return;
  }
  const r = settingsUI.rows[settingsUI.at];
  const top = settingsUI.at === 0 && !settingsUI.sub;
  const items = [['UpDown', 'Up, Down', top ? 'move, Up to the menu' : 'move']];
  if (r && (r.kind === 'choice' || r.kind === 'switch')) items.push(['LeftRight', 'Left, Right', 'change']);
  if (r && r.kind === 'switch') items.push(['Enter', 'Enter', 'switches']);
  if (r && r.kind === 'open') items.push(['Enter', 'Enter', 'opens']);
  if (r && r.kind === 'action') items.push(['Enter', 'Enter', r.verb || 'runs']);
  if (r && r.kind === 'text') items.push(['Enter', 'Enter', androidTV ? 'types in it' : 'types in it, with a keyboard']);
  items.push(['Escape', 'Esc', settingsUI.sub ? 'back' : 'back to the tabs']);
  hint.innerHTML = hints(items);
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
  renderSettingsHint();
}

// settingsKey handles a key in Settings, or returns false to leave it to
// the keys every view has (Esc goes back to TV).
function settingsKey(e) {
  if (!settingsUI.sub && (e.key === 'PageUp' || e.key === 'PageDown' || e.key === '[' || e.key === ']')) {
    e.preventDefault();
    stepSettingsTab(e.key === 'PageDown' || e.key === ']' ? 1 : -1);
    return true;
  }
  if (settingsUI.zone === 'tabs') return settingsTabsKey(e);
  if (settingsUI.tab === 'reception') return antennaKey(e);
  const r = settingsUI.rows[settingsUI.at];
  switch (e.key) {
    case 'ArrowUp':
      if (settingsUI.at > 0) moveSetting(settingsUI.at - 1);
      else if (!settingsUI.sub) openDock();
      break;
    case 'ArrowDown': moveSetting(settingsUI.at + 1); break;
    case 'PageUp': case 'PageDown': moveSetting(settingsUI.at + (e.key === 'PageDown' ? 8 : -8)); break;
    case 'ArrowLeft':
      if (r && r.kind === 'choice') stepSetting(r, -1);
      else if (r && r.kind === 'switch') { if (settingsValue(r)) changeSetting(r, false); }
      else if (settingsUI.sub) closeSettingsList();
      else settingsZone('tabs');
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

// settingsTabsKey: Up and Down pick a tab (Up from the first to the dock),
// Right or OK goes into it, Left to the dock.
function settingsTabsKey(e) {
  const tabs = settingsTabs();
  const i = tabs.findIndex(([k]) => k === settingsUI.tab);
  switch (e.key) {
    case 'ArrowUp': if (i > 0) stepSettingsTab(-1); else openDock(); break;
    case 'ArrowDown': stepSettingsTab(1); break;
    case 'ArrowRight': case 'Enter': settingsZone('list'); break;
    case 'ArrowLeft': openDock(); break;
    default: return false;
  }
  e.preventDefault();
  return true;
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
// Auto is the app's for the platform it says it is; for another, chosen in
// Settings, a desktop's 100% or 125% across a room.
const autoScale = () => (platform === detectedPlatform ? (state.boot && state.boot.autoScale) || 100 : platform === 'desktop' ? 100 : 125);
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
  // The viewport in the root's own pixels, for style.css's --vw and --vh:
  // engines differ on what vw and vh mean under CSS zoom (the Mac app's
  // WebKit sizes the guide's floating TV and its grid differently), while
  // pixels zoom alike everywhere.
  const cssZoom = native ? 1 : z;
  root.style.setProperty('--vw', `${innerWidth / cssZoom / 100}px`);
  root.style.setProperty('--vh', `${innerHeight / cssZoom / 100}px`);
  // Larger sizes leave less room above the guide (.tight in style.css):
  // the size chosen, not the zoom, which on the Android TV app also fits a
  // 1080p screen's interface into the WebView.
  root.classList.toggle('tight', z / fit > 1 && (native ? innerHeight : innerHeight / z) < 900);
  fitWX();
  if (state.view === 'guide' && state.report) renderGuide();
}

const PLATFORM_SUBS = {
  desktop: 'For a mouse and a keyboard',
  console: 'For a controller, across a room',
  tv: 'For a TV remote: arrows, OK and Back',
};

async function setPlatform(k) {
  if (k) localStorage.setItem(platformKey, k); else localStorage.removeItem(platformKey);
  if (applyPlatform()) {
    applyScale();
    renderHints();
  }
  toast(`Layout: ${k ? PLATFORMS.find(([x]) => x === k)[1] : `automatic, ${PLATFORMS.find(([x]) => x === detectedPlatform)[1]}`}`, 1800);
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
  toast(`Hid ${ch.number} ${ch.network || displayCall(ch)}. ${onKeys() ? 'Press U to undo.' : 'Settings, Hidden channels shows it again.'}`, 5000);
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
