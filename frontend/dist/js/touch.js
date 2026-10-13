'use strict';

// Touch: a phone or a tablet, the web app installed or in a browser.

// ---------- touch ----------
// Taps are clicks, which every view takes already; this adds what a finger
// needs besides. On TV a tap brings up the controls and puts them away, a
// swipe left or right skips as Left and Right do (back 10 s, ahead 30 s),
// up or down changes channel as Up and Down do. In the guide a swipe left
// or right moves the hours shown, a tap picks a program, a tap on the one
// picked watches it or shows what's to come's actions, and holding one
// shows its actions (record, favorite, hide). In Recordings and Podcasts a
// tap on what's picked plays it. A phone held upright is asked to turn
// sideways (style.css).
const touch = { x: 0, y: 0, on: false, held: 0, longDone: false, at: -Infinity };
const TOUCH_SWIPE = 48;      // CSS pixels a finger travels for a swipe
const TOUCH_HOLD_MS = 550;   // a press this long is a hold
const TOUCH_MOUSE_MS = 1000; // a finger's stand-in mouse events follow it within this

// A finger's or a pen's pointer: its gestures are a finger's in any
// layout, as on a laptop's touch screen.
const touching = (e) => e.pointerType !== 'mouse';

// A finger also sends a mouse's events after it, for pages made for one.
// Here they'd act twice: its move is a pointer brought to the screen, which
// brings up the dock and the controls (wireMouse), and two quick taps are a
// double click, which plays what the second tap already played. What a
// finger does is handled here, from its pointer events.
const fromFinger = () => performance.now() - touch.at < TOUCH_MOUSE_MS;
const fingerTap = () => platform === 'touch' || fromFinger();

function wireTouch() {
  const lastFinger = (e) => { if (touching(e)) touch.at = performance.now(); };
  document.addEventListener('pointerdown', (e) => {
    lastFinger(e);
    if (!touching(e)) return;
    Object.assign(touch, { x: e.clientX, y: e.clientY, on: true, longDone: false });
    clearTimeout(touch.held);
    const cell = e.target.closest && e.target.closest('#guide .g-rows [data-row]');
    if (cell) touch.held = setTimeout(() => { touch.longDone = true; holdCell(cell); }, TOUCH_HOLD_MS);
  }, { capture: true, passive: true });
  document.addEventListener('pointermove', (e) => {
    if (touch.on && Math.hypot(e.clientX - touch.x, e.clientY - touch.y) > 12) clearTimeout(touch.held);
  }, { capture: true, passive: true });
  document.addEventListener('pointercancel', () => { touch.on = false; clearTimeout(touch.held); }, true);
  document.addEventListener('pointerup', (e) => {
    lastFinger(e);
    if (!touch.on || !touching(e)) return;
    touch.on = false;
    clearTimeout(touch.held);
    const dx = e.clientX - touch.x;
    const dy = e.clientY - touch.y;
    if (Math.max(Math.abs(dx), Math.abs(dy)) < TOUCH_SWIPE) return;
    swipe(e.target, Math.abs(dx) > Math.abs(dy) ? (dx > 0 ? 'right' : 'left') : (dy > 0 ? 'down' : 'up'));
  }, { capture: true, passive: true });
  for (const type of ['mousemove', 'dblclick']) {
    document.addEventListener(type, (e) => { if (fromFinger()) e.stopImmediatePropagation(); }, true);
  }
  // A hold's lift isn't also a tap.
  document.addEventListener('click', (e) => {
    if (touch.longDone) {
      touch.longDone = false;
      e.stopPropagation();
      e.preventDefault();
    }
  }, true);
  // On TV a tap on the picture, or on the banner over it, brings up the
  // controls; with them up, a tap anywhere but on a control puts them
  // away, the banner too, as a phone's video players do. Listened for on
  // the picture and the banner themselves, as an iPhone sends a tap's
  // click only to what listens for one, and first (capture), so a tap on
  // the picture elsewhere, which goes back to TV (wireDock), isn't one.
  const tapTV = (e) => {
    if (!fingerTap() || state.view !== 'tv' || document.body.classList.contains('booting')) return;
    if (e.target.closest('button, input, a, .tl, [data-act]')) return;
    if (osd.open) hideBanner(); else openOSD(null);
  };
  for (const el of [stage, $('#banner')]) el.addEventListener('click', tapTV, true);
}

// swipe acts on a swipe over el: on TV, over the picture or the banner,
// the arrows; in the guide, the hours shown.
function swipe(el, dir) {
  if (state.view === 'tv' && !el.closest('#dock')) {
    if (dir === 'left' || dir === 'right') {
      skip(dir === 'right' ? 30 : -10);
      return openOSD('timeline');
    }
    return step(dir === 'up' ? -1 : 1);
  }
  if (state.view === 'guide' && el.closest('.g-rows') && (dir === 'left' || dir === 'right')) {
    const earliest = floor30(Date.now()) - 30 * MIN;
    state.gStart = Math.max(earliest, state.gStart + (dir === 'left' ? 60 : -60) * MIN);
    state.gTime = Math.max(state.gTime, state.gStart);
    if (state.gTime >= state.gStart + (state.gSpan - 30) * MIN) state.gTime = state.gStart;
    renderGuide();
  }
  return undefined;
}

// holdCell shows a guide program's actions, picking it first.
function holdCell(cell) {
  state.gRow = Number(cell.dataset.row);
  if (cell.dataset.t) state.gTime = Number(cell.dataset.t);
  renderGuide();
  guideOpenActions();
}

// tappedAgain reports whether a tap lands on what was already picked: the
// guide's, Recordings' and Podcasts' second tap does it.
const tappedAgain = (was, now) => fingerTap() && was === now;
