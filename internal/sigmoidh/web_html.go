package sigmoidh

// Embedded single-file UI for -web. No dependencies, no build step.
// Design language mirrors the terminal TUI: flat dark canvas, magenta ✻,
// yellow ▲ tools, green results, hard 1px borders, zero glow.

const webPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sigmoidh · live</title>
<style>
  :root {
    --bg: #0a0d12; --bg2: #0f141c; --line: #1c2431;
    --fg: #c7d0dc; --dim: #5c6a7d;
    --magenta: #ff5fd7; --cyan: #5fd7ff; --yellow: #ffd75f;
    --green: #5fff87; --red: #ff6b6b;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body { height: 100%; }
  body {
    background: var(--bg); color: var(--fg);
    font: 14px/1.55 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    display: flex; flex-direction: column;
  }
  /* faint blueprint grid */
  body::before {
    content: ""; position: fixed; inset: 0; pointer-events: none;
    background-image: linear-gradient(var(--line) 1px, transparent 1px),
                      linear-gradient(90deg, var(--line) 1px, transparent 1px);
    background-size: 32px 32px; opacity: .18;
  }
  header {
    position: sticky; top: 0; z-index: 5;
    display: flex; align-items: center; gap: 12px;
    padding: 10px 16px; background: rgba(10,13,18,.92);
    border-bottom: 1px solid var(--line); backdrop-filter: blur(2px);
  }
  .logo { font-weight: 700; letter-spacing: .5px; }
  .logo b { color: var(--magenta); }
  .dot { width: 9px; height: 9px; border-radius: 50%; background: var(--green);
         animation: blink 1.6s steps(2) infinite; }
  @keyframes blink { 50% { opacity: .25; } }
  .chip {
    border: 1px solid var(--line); background: var(--bg2);
    padding: 2px 8px; border-radius: 3px; font-size: 12px; color: var(--dim);
  }
  .chip b { color: var(--fg); font-weight: 600; }
  #stats { margin-left: auto; display: flex; gap: 8px; align-items: center; }
  main { flex: 1; overflow-y: auto; padding: 18px 16px 90px; }
  .wrap { max-width: 880px; margin: 0 auto; display: flex; flex-direction: column; gap: 10px; }
  .empty { color: var(--dim); text-align: center; margin-top: 18vh; }
  .empty .big { font-size: 42px; display: block; margin-bottom: 10px; }

  .ev { border: 1px solid var(--line); background: var(--bg2);
        border-radius: 6px; padding: 10px 14px; animation: pop .18s ease-out; }
  @keyframes pop { from { transform: translateY(4px); opacity: 0; } }
  .ev .meta { display: flex; gap: 8px; align-items: baseline; color: var(--dim); font-size: 12px; margin-bottom: 4px; }
  .ev .t { color: var(--cyan); }
  .ev.thinking { border-left: 3px solid var(--magenta); }
  .ev.answer   { border-left: 3px solid var(--cyan); font-size: 15px; }
  .ev.tool     { border-left: 3px solid var(--yellow); }
  .ev.tool.err { border-left-color: var(--red); }
  .ev.system   { border-left: 3px solid var(--dim); color: var(--dim); }
  .ev.user     { border-left: 3px solid var(--green); }
  .ev.user .meta .t { color: var(--green); font-weight: 700; }
  .ev.user.cmd { border-left-color: var(--yellow); background: rgba(255,215,95,.04); }
  .ev.user.cmd .meta .t { color: var(--yellow); }
  .ev.user.cmd .text { color: var(--yellow); }
  .ev.user .text  { white-space: pre-wrap; word-break: break-word; }
  .name { font-weight: 700; }
  .tool .name { color: var(--yellow); }
  .target { color: var(--fg); white-space: pre-wrap; word-break: break-word; }
  pre.code {
    background: #070a0f; border: 1px solid var(--line); border-radius: 4px;
    padding: 8px 10px; margin-top: 6px; overflow-x: auto;
    white-space: pre-wrap; word-break: break-word; color: var(--fg);
  }
  code.inline { background: #070a0f; border: 1px solid var(--line);
                padding: 0 4px; border-radius: 3px; color: var(--green); }
  .stream { white-space: pre-wrap; word-break: break-word; color: var(--dim); }
  .answer .stream { color: var(--fg); }
  .badge { font-size: 11px; padding: 1px 7px; border-radius: 3px; border: 1px solid var(--line); color: var(--dim); }
  .badge.g { color: var(--green); border-color: var(--green); }
  .badge.r { color: var(--red); border-color: var(--red); }
  details { margin-top: 6px; }
  summary { cursor: pointer; color: var(--dim); font-size: 12px; }
  footer {
    position: sticky; bottom: 0; padding: 6px 16px;
    background: rgba(10,13,18,.92); border-top: 1px solid var(--line);
    color: var(--dim); font-size: 12px; display: flex; justify-content: space-between;
  }
</style>
</head>
<body>
<header>
  <span class="dot" id="live"></span>
  <span class="logo">sig<b>·</b>moidh <b>live</b></span>
  <span class="chip" id="model">waiting…</span>
  <div id="stats">
    <span class="chip">tokens <b id="tok">0</b></span>
    <span class="chip">tools <b id="tools">0</b></span>
  </div>
</header>
<main><div class="wrap" id="feed"></div>
  <div class="empty" id="empty"><span class="big">✻</span>waiting for agent events…</div>
</main>
<footer>
  <span>SSE /events · read-only observer</span>
  <span id="clock">0.0s</span>
</footer>
<script>
"use strict";
var feed = document.getElementById("feed");
var empty = document.getElementById("empty");
var curThink = null, curAnswer = null;
var tok = 0, tools = 0, t0 = null, clockFrozen = false;

function el(tag, cls, txt) {
  var e = document.createElement(tag);
  if (cls) e.className = cls;
  if (txt !== undefined) e.textContent = txt;
  return e;
}
function meta(parent, label) {
  var m = el("div", "meta");
  m.appendChild(el("span", "t", label));
  m.appendChild(el("span", null, new Date().toLocaleTimeString()));
  parent.appendChild(m);
}
function stick() {
  var main = document.querySelector("main");
  return main.scrollHeight - main.scrollTop - main.clientHeight < 120;
}
function add(node) {
  var main = document.querySelector("main");
  var atBottom = stick();
  empty.style.display = "none";
  feed.appendChild(node);
  if (atBottom) main.scrollTop = main.scrollHeight;
}
function esc(s) { var d = el("div"); d.textContent = s; return d.innerHTML; }
function mdLite(s) {
  // line-based pass: fences, headings, bullets, bold, inline code
  // simple line-based pass: fences, headings, bullets, bold, inline code
  var html = esc(s)
    .replace(/\x60([^\x60]+)\x60/g, "<code class='inline'>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>");
  var lines = html.split("\n"), res = [], fence = false;
  for (var j = 0; j < lines.length; j++) {
    var L = lines[j];
    if (/^\s*\x60\x60\x60/.test(L)) {
      res.push(fence ? "</pre>" : "<pre class='code'>");
      fence = !fence;
      continue;
    }
    if (fence) { res.push(L); continue; }
    if (/^### /.test(L)) res.push("<div style='color:var(--cyan);font-weight:700'>" + L.slice(4) + "</div>");
    else if (/^## /.test(L)) res.push("<div style='color:var(--cyan);font-weight:700;font-size:16px'>" + L.slice(3) + "</div>");
    else if (/^# /.test(L)) res.push("<div style='color:var(--magenta);font-weight:700;font-size:17px'>" + L.slice(2) + "</div>");
    else if (/^[-*] /.test(L)) res.push("&nbsp;&nbsp•&nbsp;" + L.slice(2));
    else res.push(L);
  }
  if (fence) res.push("</pre>");
  var out = res.join("\n");
}

function ensureThinking() {
  if (curThink) return curThink;
  curThink = el("div", "ev thinking");
  meta(curThink, "✻ thinking");
  var st = el("div", "stream");
  curThink.appendChild(st);
  curThink._s = st;
  add(curThink);
  return curThink;
}
function ensureAnswer() {
  if (curAnswer) return curAnswer;
  curAnswer = el("div", "ev answer");
  meta(curAnswer, "▸ sigmoidh");
  var st = el("div", "stream");
  curAnswer.appendChild(st);
  curAnswer._s = st;
  add(curAnswer);
  return curAnswer;
}

// finalizeOpenBlocks gracefully closes any streaming section so interleaved
// events never leave dangling blocks.
function finalizeOpenBlocks() {
  if (curThink) { curThink = null; }
  if (curAnswer) { curAnswer = null; }
}

function handle(d) {
  switch (d.event) {
    case "bootstrap":
      t0 = Date.now(); clockFrozen = false;
      break;
    case "done":
      clockFrozen = true;
      break;
  }
  switch (d.event) {
    case "tool_call": case "tool_result": case "fallback": case "retry":
    case "trigger": case "user_input": case "done":
      finalizeOpenBlocks();
      break;
  }
  switch (d.event) {
    case "bootstrap":
      document.getElementById("model").innerHTML =
        "<b>" + esc(d.model) + "</b> · " + esc(d.provider) +
        (d.readonly ? " · read-only" : "");
      break;
    case "user_input": {
      var isCmd = d.kind === "command";
      var u = el("div", "ev user" + (isCmd ? " cmd" : ""));
      meta(u, isCmd ? "❯ command" : "❯ you");
      u.appendChild(el("div", "text", d.text));
      add(u);
      break;
    }
    case "thinking_start":
      ensureThinking();
      break;
    case "reasoning_delta":
      ensureThinking()._s.textContent += d.text;
      break;
    case "thinking_end":
      if (curThink) {
        curThink._s.parentNode.appendChild(el("span", "badge", "✓ " + (d.took_ms/1000).toFixed(1) + "s"));
      }
      curThink = null;
      break;
    case "answer_start":
      ensureAnswer();
      break;
    case "answer_delta":
      ensureAnswer()._s.textContent += d.text;
      break;
    case "answer_end":
      if (curAnswer) {
        curAnswer._s.remove();
        var body = el("div");
        body.innerHTML = mdLite(d.markdown || "");
        curAnswer.appendChild(body);
      }
      curAnswer = null;
      break;
    case "tool_call": {
      tools++;
      document.getElementById("tools").textContent = tools;
      var n = el("div", "ev tool");
      meta(n, "▲ " + d.name);
      if (d.target) n.appendChild(el("div", "target", d.target));
      if (d.counts) n.appendChild(el("span", "badge", d.counts));
      add(n);
      break;
    }
    case "tool_result": {
      var r = el("div", "ev tool" + (d.is_error ? " err" : ""));
      meta(r, (d.is_error ? "✖ " : "▼ ") + d.name);
      r.appendChild(el("span", "badge" + (d.is_error ? " r" : " g"),
        d.bytes + " bytes · " + (d.took_ms/1000).toFixed(1) + "s"));
      if (d.preview && d.preview.length) {
        var det = el("details");
        det.appendChild(el("summary", null, "output"));
        det.appendChild(Object.assign(el("pre", "code"), {textContent: d.preview}));
        r.appendChild(det);
      }
      add(r);
      break;
    }
    case "fallback": {
      var f = el("div", "ev system");
      f.textContent = "⚠ " + d.provider + " unavailable — falling back to " + d.model;
      add(f);
      break;
    }
    case "retry": {
      var w = el("div", "ev system");
      w.textContent = "⧗ " + d.provider + " retry " + d.attempt + "/3 in " + (d.delay_ms/1000).toFixed(1) + "s";
      add(w);
      break;
    }
    case "trigger": {
      var tg = el("div", "ev system");
      tg.textContent = "🔔 notified: " + d.message;
      add(tg);
      break;
    }
    case "usage":
      tok += (d.in || 0) + (d.out || 0);
      document.getElementById("tok").textContent = tok.toLocaleString();
      break;
    case "done": {
      var dn = el("div", "ev system");
      var s = d.stats || {};
      dn.textContent = "● done · " + (s.TotalTurns||0) + " turns · " +
        (s.TotalToolCalls||0) + " tools · took " + ((s.TotalDuration||0)/1e9).toFixed(1) + "s";
      add(dn);
      break;
    }
  }
}

var es = new EventSource("/events");
es.onmessage = function(msg) {
  try { handle(JSON.parse(msg.data)); } catch (e) {}
};
setInterval(function() {
  if (t0 === null || clockFrozen) return;
  document.getElementById("clock").textContent = ((Date.now()-t0)/1000).toFixed(1) + "s";
}, 100);
</script>
</body>
</html>
`
