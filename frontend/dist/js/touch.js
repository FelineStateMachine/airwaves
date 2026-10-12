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
const touch = { x: 0, y: 0, on: false, held: 0, longDone: false };
const TOUCH_SWIPE = 48;      // CSS pixels a finger travels for a swipe
const TOUCH_HOLD_MS = 550;   // a press this long is a hold

const touching = (e) => platform === 'touch' && e.pointerType !== 'mouse';

function wireTouch() {
  document.addEventListener('pointerdown', (e) => {
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
    if (!touch.on || !touching(e)) return;
    touch.on = false;
    clearTimeout(touch.held);
    const dx = e.clientX - touch.x;
    const dy = e.clientY - touch.y;
    if (Math.max(Math.abs(dx), Math.abs(dy)) < TOUCH_SWIPE) return;
    swipe(e.target, Math.abs(dx) > Math.abs(dy) ? (dx > 0 ? 'right' : 'left') : (dy > 0 ? 'down' : 'up'));
  }, { capture: true, passive: true });
  // A hold's lift isn't also a tap.
  document.addEventListener('click', (e) => {
    if (touch.longDone) {
      touch.longDone = false;
      e.stopPropagation();
      e.preventDefault();
    }
  }, true);
  // On TV a tap that isn't on a control, on the picture or the banner over
  // it, brings up the controls or puts them away: seen first (capture), so
  // a tap on the picture elsewhere, which goes back to TV (wireDock), isn't
  // one.
  document.addEventListener('click', (e) => {
    if (platform !== 'touch' || state.view !== 'tv' || document.body.classList.contains('booting')) return;
    if (e.target.closest('button, input, a, .ob, .tl, #dock, [data-act]')) return;
    if (osd.open) closeOSD(); else openOSD(null);
  }, true);
}

// swipe acts on a swipe over el: on TV, the arrows; in the guide, the
// hours shown.
function swipe(el, dir) {
  if (state.view === 'tv' && (stage.contains(el) || el === document.body)) {
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
const tappedAgain = (was, now) => platform === 'touch' && was === now;
