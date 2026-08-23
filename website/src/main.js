import "./style.css";
import { makeTextures, url } from "./textures.js";
import { SPRITES, drawSprite } from "./sprites.js";
import { ensureAudio, sfx } from "./sfx.js";

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const REDUCED = matchMedia("(prefers-reduced-motion: reduce)").matches;

/* ================= textures ================= */
const T = makeTextures();
const rootStyle = document.documentElement.style;
rootStyle.setProperty("--tex-grassside", url(T.grassSide));
rootStyle.setProperty("--tex-grasstop", url(T.grassTop));
rootStyle.setProperty("--tex-dirt", url(T.dirt));
rootStyle.setProperty("--tex-stone", url(T.stone));
rootStyle.setProperty("--tex-deepslate", url(T.deepslate));
rootStyle.setProperty("--tex-bedrock", url(T.bedrock));
rootStyle.setProperty("--tex-planks", url(T.planks));

// favicon = grass side tile
const fav = document.createElement("link");
fav.rel = "icon";
fav.href = T.grassSide.toDataURL();
document.head.appendChild(fav);

// hero cube faces
$(".f-top").style.backgroundImage = url(T.grassTop);
$(".f-bottom").style.backgroundImage = url(T.dirt);
$$(".f-front,.f-back,.f-left,.f-right").forEach((f) => (f.style.backgroundImage = url(T.grassSide)));

/* ================= ores scattered underground ================= */
const ORE_TILES = {
  coal: T.coalOre, iron: T.ironOre, gold: T.goldOre,
  redstone: T.redstoneOre, diamond: T.diamondOre,
};
$$("[data-ores]").forEach((sec) => {
  const names = sec.dataset.ores.split(/\s+/).filter(Boolean);
  if (!names.length) return;
  const pool = [];
  names.forEach((n) => {
    const count = n === "diamond" ? 2 : n === "gold" ? 3 : 4;
    for (let i = 0; i < count; i++) pool.push(ORE_TILES[n]);
  });
  // deterministic-ish scatter
  let seed = sec.id.length * 97;
  const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);
  pool.forEach((tileC) => {
    const d = document.createElement("div");
    d.className = "ore";
    d.style.backgroundImage = `url(${tileC.toDataURL()})`;
    d.style.left = `${Math.round(4 + rnd() * 92)}%`;
    d.style.top = `${Math.round(6 + rnd() * 86)}%`;
    if (rnd() > 0.6) d.style.transform = "scale(1.25)";
    sec.appendChild(d);
  });
});

/* ================= splash ================= */
const SPLASHES = [
  "Also try Minecraft!", "Now in Go!", "It digs!", "14 tools inside!",
  "No GUI required!", "Punch trees, not prod!", "Blocks all the way down!",
  "Y=-64 certified!", "One binary!", "Creepers hate this!",
];
$("#splash").textContent = SPLASHES[Math.floor(Math.random() * SPLASHES.length)];

/* ================= inventory ================= */
const TOOLS = [
  { t: "Bash",         ic: "cmd",     r: "",       d: "Run shell commands. Dangerous ones are refused." },
  { t: "Read",         ic: "book",    r: "",       d: "Read text files. Never secrets, never binaries." },
  { t: "Write",        ic: "pencil",  r: "",       d: "Create or overwrite a file." },
  { t: "Edit",         ic: "anvil",   r: "",       d: "Exact-substring replace. Refused if ambiguous." },
  { t: "Search",       ic: "compass", r: "",       d: "Literal or regex search across the project." },
  { t: "List Files",   ic: "chest",   r: "",       d: "List directories or glob. Skips node_modules, .git and secrets." },
  { t: "Batch Read",   ic: "paper",   r: "",       d: "Read many files in a single call." },
  { t: "Apply Patch",  ic: "hammer",  r: "uncom",  d: "Apply unified diffs. The preferred way to edit." },
  { t: "Exec Session", ic: "exec",    r: "rare",   d: "Start a persistent shell session." },
  { t: "Write Stdin",  ic: "lever",   r: "rare",   d: "Pipe input into a live session." },
  { t: "Web",          ic: "pearl",   r: "rare",   d: "Throw it a URL or a question. Get text back." },
  { t: "Sub-agent",    ic: "egg",     r: "epic",   d: "Hatch a focused mini-agent for heavy jobs." },
  { t: "Trigger",      ic: "bell",    r: "uncom",  d: "Ping the human when long work finishes." },
  { t: "Memory",       ic: "bottle",  r: "epic",   d: "Distilled notes that survive context loss." },
  { t: "Plan",         ic: "book",    r: "rare",   d: "Task checklist the agent keeps in progress." },
  { t: "Session Search", ic: "compass", r: "epic", d: "Full-text search across every past transcript." },
  { t: "Ask User",     ic: "sign",    r: "rare",   d: "Blocking clarifying question, then end turn." },
];

function makeSlot(iconName, tipHTML, num) {
  const slot = document.createElement("div");
  slot.className = "slot";
  slot.tabIndex = 0;
  const c = document.createElement("canvas");
  c.className = "pixel";
  drawSprite(c, iconName);
  slot.appendChild(c);
  if (tipHTML) {
    const tip = document.createElement("div");
    tip.className = "tip";
    tip.innerHTML = tipHTML;
    slot.appendChild(tip);
  }
  return slot;
}

const inv = $("#inventory");
TOOLS.forEach((tool) => {
  inv.appendChild(makeSlot(tool.ic,
    `<b class="${tool.r}">${tool.t}</b><span>${tool.d}</span>`));
});
for (let i = TOOLS.length; i < 18; i++) {
  const e = document.createElement("div");
  e.className = "slot";
  inv.appendChild(e);
}

/* ================= hearts ================= */
const HEART = {
  pal: { R: "#e02f2f", W: "#ff8a8a" },
  rows: [
    "..RR..RR..",
    ".RWRRRRRR.",
    "RRRRRRRRRR",
    "RRRRRRRRRR",
    ".RRRRRRRR.",
    "..RRRRRR..",
    "...RRRR...",
    "....RR....",
    "..........",
    "..........",
  ],
};
SPRITES.__heart = HEART;
const heartsEl = $("#hearts");
for (let i = 0; i < 10; i++) {
  const c = document.createElement("canvas");
  c.className = "pixel";
  drawSprite(c, "__heart", 2);
  heartsEl.appendChild(c);
}

/* ================= hotbar nav ================= */
const SECTIONS = $$("[data-section]");
const ICON_FOR = { Home: null, Tools: "chest", Demo: "cmd", Install: "pickaxe", Providers: "sign", Bottom: null };
const hb = $(".hotbar");
const selector = document.createElement("div");
selector.id = "hb-selector";
const hbLabel = document.createElement("div");
Object.assign(hbLabel.style, {
  position: "absolute", bottom: "calc(100% + 12px)", left: "50%",
  transform: "translateX(-50%)", fontFamily: "'Press Start 2P'",
  fontSize: "9px", color: "#fff", textShadow: "2px 2px 0 #000",
  whiteSpace: "nowrap", opacity: "0", transition: "opacity .2s", pointerEvents: "none",
});
hb.appendChild(selector);
hb.appendChild(hbLabel);

SECTIONS.forEach((sec, i) => {
  const slot = document.createElement("div");
  slot.className = "slot";
  slot.tabIndex = 0;
  slot.setAttribute("role", "link");
  slot.setAttribute("aria-label", `Go to ${sec.dataset.section}`);
  slot.innerHTML = `<span class="slot-num">${i + 1}</span>`;
  const name = sec.dataset.section;
  if (ICON_FOR[name]) {
    const c = document.createElement("canvas");
    c.className = "pixel";
    drawSprite(c, ICON_FOR[name]);
    slot.prepend(c);
  } else {
    const c = document.createElement("canvas");
    c.className = "pixel";
    const tex = name === "Home" ? T.grassTop : T.bedrock;
    c.width = 40; c.height = 40;
    c.getContext("2d").drawImage(tex, 0, 0, 40, 40);
    c.style.imageRendering = "pixelated";
    slot.prepend(c);
  }
  slot.addEventListener("click", () => {
    sec.scrollIntoView({ behavior: REDUCED ? "auto" : "smooth" });
    sfx.select();
    setActiveSlot(i, true);
  });
  slot.addEventListener("mouseenter", () => sfx.hover());
  hb.appendChild(slot);
});
for (let i = SECTIONS.length; i < 9; i++) {
  const e = document.createElement("div");
  e.className = "slot empty";
  hb.appendChild(e);
}
const HB_SLOTS = $$(".hotbar .slot:not(.empty)");

let labelTimer;
function setActiveSlot(i, announce = false) {
  selector.style.transform = `translateX(${i * 49}px)`;
  if (announce || !labelTimer) {
    hbLabel.textContent = SECTIONS[i].dataset.section;
    hbLabel.style.opacity = "1";
    clearTimeout(labelTimer);
    labelTimer = setTimeout(() => (hbLabel.style.opacity = "0"), 1400);
  }
}
setActiveSlot(0);

const spy = new IntersectionObserver(
  (entries) => {
    entries.forEach((en) => {
      if (!en.isIntersecting) return;
      const idx = SECTIONS.indexOf(en.target);
      setActiveSlot(idx);
      if (idx >= 1 && !flags.dugIn) {
        flags.dugIn = true;
        toast("Advancement Made!", "Digging In — below the surface");
      }
      if (en.target.id === "providers" && !flags.bottom) {
        flags.bottom = true;
        toast("Advancement Made!", "The Bottomless Pit — Y = -64");
      }
    });
  },
  { rootMargin: "-42% 0px -42% 0px" }
);
SECTIONS.forEach((s) => spy.observe(s));

/* ================= xp bar + depth meter ================= */
const xpFill = $("#xp-fill"), xpLevel = $("#xp-level"), depth = $("#depth-meter");
function onScroll() {
  const max = document.documentElement.scrollHeight - innerHeight;
  const p = Math.min(1, Math.max(0, scrollY / max));
  xpFill.style.width = `${Math.round(p * 100)}%`;
  xpLevel.textContent = String(Math.floor(p * 99));
  const y = Math.round(203 - p * 267);
  depth.textContent = `Y = ${y}`;
  depth.classList.toggle("negative", y < 0);
}
addEventListener("scroll", onScroll, { passive: true });
onScroll();

/* ================= clouds ================= */
const sky = $("#clouds");
const hero = $(".hero");
function cloudLoop() {
  const PX = 4;
  function resize() {
    sky.width = Math.ceil(hero.clientWidth / PX);
    sky.height = Math.ceil(hero.clientHeight / PX);
  }
  resize();
  addEventListener("resize", resize);
  const ctx = sky.getContext("2d");
  ctx.imageSmoothingEnabled = false;
  const mkCloud = () => {
    const parts = [];
    const n = 2 + Math.floor(Math.random() * 3);
    let cx = 0;
    for (let i = 0; i < n; i++) {
      parts.push([cx, Math.random() < 0.4 ? -2 : 0, 6 + Math.floor(Math.random() * 14), 3]);
      cx += parts[i][2] - 2;
    }
    return { x: sky.width + 20, y: 6 + Math.random() * (sky.height * 0.55), w: cx, parts };
  };
  const clouds = Array.from({ length: 6 }, () => ({ ...mkCloud(), x: Math.random() * sky.width }));
  function frame() {
    ctx.clearRect(0, 0, sky.width, sky.height);
    ctx.fillStyle = "rgba(255,255,255,0.88)";
    clouds.forEach((c) => {
      if (!REDUCED) c.x -= 0.07;
      if (c.x < -c.w - 20) Object.assign(c, mkCloud());
      c.parts.forEach(([dx, dy, w, h]) => ctx.fillRect(Math.round(c.x + dx), Math.round(c.y + dy), w, h));
    });
    requestAnimationFrame(frame);
  }
  frame();
}
cloudLoop();

/* ================= break particles ================= */
const fx = $("#fx");
const fctx = fx.getContext("2d");
function sizeFx() { fx.width = innerWidth; fx.height = innerHeight; }
sizeFx();
addEventListener("resize", sizeFx);

const STRATA_COLORS = {
  sky: ["#79a7ff", "#8fbaff", "#fffdd0", "#ffffff"],
  dirt: ["#79553a", "#8a6142", "#6a4831", "#5c3d28"],
  stone: ["#747474", "#676767", "#828282"],
  deepslate: ["#34343b", "#2b2b31", "#3d3d45"],
  bedrock: ["#17171a", "#26262b", "#333338"],
};
const particles = [];
function burst(x, y, colors, n = 10) {
  if (REDUCED) return;
  for (let i = 0; i < n; i++) {
    particles.push({
      x, y,
      vx: (Math.random() - 0.5) * 7,
      vy: -Math.random() * 6 - 1,
      life: 34 + Math.random() * 20,
      c: colors[Math.floor(Math.random() * colors.length)],
      s: 4 + Math.random() * 3,
    });
  }
}
function fxFrame() {
  fctx.clearRect(0, 0, fx.width, fx.height);
  for (let i = particles.length - 1; i >= 0; i--) {
    const p = particles[i];
    p.vy += 0.32; p.x += p.vx; p.y += p.vy; p.life--;
    if (p.life <= 0 || p.y > fx.height + 20) { particles.splice(i, 1); continue; }
    fctx.globalAlpha = p.life < 10 ? p.life / 10 : 1;
    fctx.fillStyle = p.c;
    fctx.fillRect(p.x | 0, p.y | 0, p.s | 0, p.s | 0);
  }
  fctx.globalAlpha = 1;
  requestAnimationFrame(fxFrame);
}
fxFrame();

document.addEventListener("pointerdown", (e) => {
  ensureAudio();
  if (e.target.closest("a, button, .slot, input, textarea, .term")) return;
  const zone = e.target.closest("[data-strata]");
  const colors = STRATA_COLORS[zone?.dataset.strata || "stone"];
  burst(e.clientX, e.clientY, colors);
  sfx.click();
});

/* diamond easter egg */
document.addEventListener("keydown", (e) => {
  if (e.key.toLowerCase() !== "g" || e.target.closest("input,textarea")) return;
  burst(innerWidth / 2, innerHeight / 2, ["#4aedd9", "#21a89a", "#8ff5e0"], 26);
  toast("Advancement Made!", "DIAMONDS! — pressed g like a legend");
  sfx.coin();
});

/* cube punch */
$(".cube-wrap").addEventListener("click", () => {
  if (flags.cube) return;
  flags.cube = true;
  const r = $(".cube-wrap").getBoundingClientRect();
  burst(r.left + r.width / 2, r.top + r.height / 2, ["#5d9c3f", "#79553a", "#8a6142"], 22);
  toast("Advancement Made!", "Punching Trees — the cube felt nothing");
  sfx.ding();
});

/* ================= toasts ================= */
const flags = {};
const toastQueue = [];
let toastBusy = false;
function toast(title, msg) {
  toastQueue.push([title, msg]);
  pumpToast();
}
async function pumpToast() {
  if (toastBusy || !toastQueue.length) return;
  toastBusy = true;
  const [title, msg] = toastQueue.shift();
  const el = document.createElement("div");
  el.className = "toast";
  el.innerHTML = `<h3>${title}</h3><p>${msg}</p>`;
  document.body.appendChild(el);
  await sleep(30);
  el.classList.add("show");
  sfx.ding();
  await sleep(3400);
  el.classList.remove("show");
  await sleep(300);
  el.remove();
  toastBusy = false;
  pumpToast();
}

/* ================= terminal replay ================= */
const SCRIPT = [
  { cls: "t-cmd",  pre: "$ ",            s: "./sigmoidh --provider openrouter" },
  { cls: "t-dim",                        s: "session 2026-08-22_23-05-33 resumed · openrouter/qwen3-coder" },
  { cls: "" },
  { cls: "t-you",  pre: "you> ",         s: "fix the failing test" },
  { cls: "" },
  { cls: "t-tool", pre: "tool › bash     ", s: "go test ./..." },
  { cls: "t-err",  ind: true,            s: "✗ hosttools_test.go:41 — want exit code 1, got 0" },
  { cls: "t-tool", pre: "tool › read     ", s: "hosttools_test.go · lines 32–48" },
  { cls: "t-tool", pre: "tool › read     ", s: "guard.go · lines 10–36" },
  { cls: "t-tool", pre: "tool › edit     ", s: "guard.go — 1 hunk applied" },
  { cls: "t-tool", pre: "tool › bash     ", s: "go test ./..." },
  { cls: "t-ok",   ind: true,            s: "✓ ok  hosttools 0.412s — 14 tests pass" },
  { cls: "" },
  { cls: "t-say",  pre: "sigmoidh> ",    s: "Fixed the guard clause — dangerous commands now exit 1. All green." },
];

const termBody = $("#term-body");
let termRun = 0;

function lineEl(l) {
  const d = document.createElement("div");
  if (l.cls) d.className = l.cls;
  if (l.ind) {
    const sp = document.createElement("span");
    sp.className = "t-indent";
    sp.textContent = l.s;
    d.appendChild(sp);
  } else {
    if (l.pre) {
      const pre = document.createElement("span");
      pre.textContent = l.pre;
      d.appendChild(pre);
    }
    const body = document.createElement("span");
    body.className = "txt";
    body.textContent = l.s ?? "";
    d.appendChild(body);
  }
  return d;
}

async function playTerminal() {
  const id = ++termRun;
  termBody.innerHTML = "";
  const caret = document.createElement("span");
  caret.className = "t-caret";

  for (const l of SCRIPT) {
    if (id !== termRun) return;
    if (!l.cls && !l.pre) { termBody.appendChild(document.createElement("div")); continue; }

    const d = lineEl(l);
    termBody.appendChild(d);

    if (l.cls === "t-cmd") {
      const txt = d.querySelector(".txt");
      txt.textContent = "";
      d.appendChild(caret);
      for (const ch of l.s) {
        if (id !== termRun) return;
        txt.textContent += ch;
        await sleep(REDUCED ? 6 : 26);
      }
      await sleep(350);
    } else if (l.cls === "t-you") {
      const txt = d.querySelector(".txt");
      txt.textContent = "";
      d.appendChild(caret);
      for (const ch of l.s) {
        if (id !== termRun) return;
        txt.textContent += ch;
        await sleep(REDUCED ? 4 : 38);
      }
      await sleep(450);
    } else {
      d.appendChild(caret);
      await sleep(REDUCED ? 60 : 330 + Math.random() * 260);
    }
    caret.remove();
  }
  if (id !== termRun) return;
  toast("Advancement Made!", "Green CI — all 14 tests pass");
}

$("#replay").addEventListener("click", () => { ensureAudio(); sfx.click(); playTerminal(); });
new IntersectionObserver((ents, obs) => {
  ents.forEach((en) => { if (en.isIntersecting) { playTerminal(); obs.disconnect(); } });
}, { threshold: 0.3 }).observe($(".term"));

/* ================= copy buttons ================= */
$$(".copy").forEach((btn) => {
  btn.addEventListener("click", async () => {
    ensureAudio();
    try { await navigator.clipboard.writeText(btn.dataset.copy); } catch {}
    sfx.coin();
    const old = btn.textContent;
    btn.textContent = "[ DONE ]";
    setTimeout(() => (btn.textContent = old), 1300);
    toast("Advancement Made!", "Command Block Pro — recipe copied");
  });
});

/* ================= sfx wiring ================= */
$$(".mc-btn").forEach((b) => b.addEventListener("mouseenter", () => sfx.hover()));
$$(".mc-btn").forEach((b) => b.addEventListener("click", () => { ensureAudio(); sfx.click(); }));
document.addEventListener("keydown", () => ensureAudio(), { once: true });
