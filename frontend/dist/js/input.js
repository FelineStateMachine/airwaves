'use strict';

// Keys, a remote, controllers, the dock, focus and the hints that name them.

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
    if ((k === 'a' || k === 'A') && state.info.antenna !== false) {
      if (receptionShown()) return setView('tv');
      if (state.view !== 'settings') setView('settings');
      return openSettings('reception', 'list');
    }
    if ((k === 'd' || k === 'D') && state.info.dvr) return setView(state.view === 'recordings' ? 'tv' : 'recordings');
    if ((k === 'w' || k === 'W') && state.view !== 'recordings' && state.view !== 'podcasts') return setView(state.view === 'weather' ? 'tv' : 'weather');
    if ((k === 'p' || k === 'P') && state.info.podcasts) return setView(state.view === 'podcasts' ? 'tv' : 'podcasts');
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
    if (k === 'f') return toggleFullscreen();

    if (state.view === 'guide') return guideKey(e);
    if (state.view === 'recordings') return recordingsKey(e);
    if (state.view === 'podcasts') return podcastsKey(e);
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
      case 'F': return toggleFavorite(state.current);
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
    case 'ArrowDown':
      // In the guide the filters sit just below the dock.
      if (state.view === 'guide') { closeDock(false); guideZone('filters'); } else closeDock();
      e.preventDefault();
      return true;
    case 'Escape': closeDock(); e.preventDefault(); return true;
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
  else if (state.view === 'podcasts') podZone(podUI.zone);
  else if (state.view === 'settings') settingsRefocus();
  else if (state.view === 'weather') wxFocus();
}

// viewBack is Back in an inner part of a view (a program's or a
// recording's actions, the guide's filters): it goes to the view's main
// part, and reports whether it did.
function viewBack() {
  if (state.view === 'guide' && state.recPick) { closeRecordPick(); return true; }
  if (state.view === 'guide' && guideUI.zone !== 'grid') { guideZone('grid'); return true; }
  if (state.view === 'recordings' && recUI.zone === 'actions') { recZone('list'); return true; }
  if (state.view === 'podcasts' && podUI.zone === 'actions') { podZone('grid'); return true; }
  // In a tab of Settings, Back goes to the tabs, as on Android TV.
  if (state.view === 'settings' && settingsUI.zone === 'list') { settingsZone('tabs'); return true; }
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
  if (platform === 'tv') return REMOTE_KEYS[key] ? esc(REMOTE_KEYS[key]) : null;
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
