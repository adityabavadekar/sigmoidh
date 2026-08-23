// Tiny square-wave SFX — retro, no audio files.
let ctx = null;
export function ensureAudio() {
  if (!ctx) {
    try { ctx = new (window.AudioContext || window.webkitAudioContext)(); } catch { ctx = null; }
  }
  if (ctx && ctx.state === "suspended") ctx.resume();
}

function note(freq, dur = 0.06, vol = 0.035, type = "square", when = 0) {
  if (!ctx) return;
  const t = ctx.currentTime + when;
  const o = ctx.createOscillator();
  const g = ctx.createGain();
  o.type = type; o.frequency.value = freq;
  g.gain.setValueAtTime(vol, t);
  g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
  o.connect(g).connect(ctx.destination);
  o.start(t); o.stop(t + dur + 0.02);
}

export const sfx = {
  hover: () => note(720, 0.03, 0.02),
  click: () => { note(420, 0.05); },
  select:() => { note(560, 0.04); note(760, 0.05, 0.03, "square", 0.045); },
  coin:  () => { note(988, 0.07, 0.04); note(1319, 0.16, 0.04, "square", 0.07); },
  ding:  () => { note(660, 0.1, 0.045); note(880, 0.22, 0.045, "square", 0.09); },
};
