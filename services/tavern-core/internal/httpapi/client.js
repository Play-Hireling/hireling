"use strict";
const $ = (sel) => document.querySelector(sel);
const token = document.getElementById("token");
// The owner token lives in this tab only. There is no "remember" option.
// Drop a token an older panel wrote to localStorage.
try { localStorage.removeItem("tavernToken"); } catch (e) { /* private mode */ }
token.value = sessionStorage.getItem("tavernToken") || "";
token.addEventListener("change", () => sessionStorage.setItem("tavernToken", token.value.trim()));

// Turns seen on the live stream, keyed by agent. A snapshot only replaces one
// when its turn is at least as new, so a slow /api/status cannot step back a
// word that already arrived. Cleared per agent when the snapshot catches up.
const liveTurns = new Map();
let streamToken = ""; // the token the current (or attempted) stream used
let streamWarned = false;
let streamController = null;

// Render strictly via text nodes so a hireling's words can never inject markup.
function el(tag, text, cls) {
  const n = document.createElement(tag);
  if (text != null) n.textContent = text;
  if (cls) n.className = cls;
  return n;
}

async function api(path) {
  const h = {};
  if (token.value.trim()) h.Authorization = "Bearer " + token.value.trim();
  const res = await fetch(path, { headers: h });
  if (!res.ok) {
    if (res.status === 401) throw new Error("unauthorized");
    throw new Error("http " + res.status);
  }
  return res.json();
}

const HUE = (id) => {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return h % 360;
};

function sprite(id) {
  const d = el("div", id.charAt(0).toUpperCase(), "sprite");
  d.style.background = "hsl(" + HUE(id) + " 45% 62%)";
  return d;
}

function balloon(agent) {
  const t = agent.turn;
  if (!t) return el("div", "idle — waiting for work", "balloon muted");
  const b = el("div", null, "balloon");
  if (t.topic) b.appendChild(el("div", t.topic));
  if (t.step) b.appendChild(el("div", t.step, "muted"));
  if (t.result) b.appendChild(el("div", t.result, "muted"));
  if (t.waitingOnOwner) b.appendChild(el("div", "waiting on you: " + t.waitingOnOwner, "waiting"));
  if (!t.topic && !t.step && !t.result && !t.waitingOnOwner) b.appendChild(el("div", "with " + (t.with || "the owner"), "muted"));
  return b;
}

// A turn event updates only that agent's balloon. The party list and the other
// agents are left alone, so the first words of an answer replace the old
// balloon as soon as the event arrives, not on the next poll.
function applyTurn(t) {
  if (!t || !t.agent) return;
  const prev = liveTurns.get(t.agent);
  if (prev && Date.parse(t.at || 0) < Date.parse(prev.at || 0)) return;
  liveTurns.set(t.agent, t);
  const card = [...document.querySelectorAll("#party .agent")].find((c) => c.dataset.agent === t.agent);
  if (!card) return; // no party row yet; the next snapshot merges this turn
  const old = card.querySelector(".balloon");
  const next = balloon({ turn: t });
  if (old) card.replaceChild(next, old);
  else card.appendChild(next);
}

// The stream's first event is the same snapshot as GET /api/status. Merge any
// newer turn already seen so a delayed snapshot cannot regress the balloon.
function applySnapshot(s) {
  if (!s || !Array.isArray(s.agents)) return;
  for (const a of s.agents) {
    const live = liveTurns.get(a.id);
    if (!live) continue;
    if (a.turn && Date.parse(a.turn.at || 0) >= Date.parse(live.at || 0)) liveTurns.delete(a.id);
    else a.turn = live;
  }
  renderStatus(s);
  renderQuest(s.quest);
}

function warnStream(reason, t) {
  if (t !== streamToken || token.value.trim() !== t || streamWarned) return;
  streamWarned = true;
  console.warn("panel: live turn stream off (" + reason + "); polling every 5 s");
}

function stopStream() {
  if (streamController) {
    try { streamController.abort(); } catch (e) { /* already gone */ }
    streamController = null;
  }
}

// Minimal SSE parser for fetch's ReadableStream: event name plus data lines,
// a blank line ends the event, and a leading colon is a keepalive comment.
// Returns true to stop reading (error event or malformed data).
function handleFrame(frame, t) {
  let name = "message";
  const data = [];
  for (const line of frame.split(/\r?\n/)) {
    if (!line || line.startsWith(":")) continue;
    const i = line.indexOf(":");
    const field = i < 0 ? line : line.slice(0, i);
    let value = i < 0 ? "" : line.slice(i + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") name = value;
    else if (field === "data") data.push(value);
  }
  if (!data.length) return false;
  let payload;
  try {
    payload = JSON.parse(data.join("\n"));
  } catch (e) {
    warnStream("malformed event", t);
    return true;
  }
  if (name === "status") applySnapshot(payload);
  else if (name === "turn") applyTurn(payload);
  else if (name === "error") {
    warnStream("server: " + (payload && payload.error ? payload.error : "unknown"), t);
    return true;
  }
  return false;
}

async function readStream(body, t) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  const drain = () => {
    for (;;) {
      const m = /\r?\n\r?\n/.exec(buf);
      if (!m) return false;
      const frame = buf.slice(0, m.index);
      buf = buf.slice(m.index + m[0].length);
      if (handleFrame(frame, t)) return true;
    }
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        buf += decoder.decode();
        drain();
        return;
      }
      if (token.value.trim() !== t) return;
      buf += decoder.decode(value, { stream: true });
      if (drain()) {
        try { await reader.cancel(); } catch (e) { /* already closed */ }
        return;
      }
    }
  } finally {
    try { reader.releaseLock(); } catch (e) { /* already gone */ }
  }
}

// EventSource cannot send Authorization, so the panel reads the SSE stream
// with fetch. There is no automatic reconnect: once it fails, the 5 s poll is
// the path until the owner changes the token.
function startStream() {
  const t = token.value.trim();
  if (t === streamToken) return;
  stopStream();
  liveTurns.clear(); // a new token is a different tavern
  streamToken = t;
  streamWarned = false;
  if (!t) return;
  const controller = new AbortController();
  streamController = controller;
  void (async () => {
    try {
      const res = await fetch("/api/turn/stream", {
        headers: { Authorization: "Bearer " + t, Accept: "text/event-stream" },
        cache: "no-store",
        signal: controller.signal,
      });
      if (!res.ok) {
        warnStream("http " + res.status, t);
        return;
      }
      if (!res.body) {
        warnStream("no body", t);
        return;
      }
      await readStream(res.body, t);
      if (!controller.signal.aborted) warnStream("stream ended", t);
    } catch (e) {
      if (e && e.name === "AbortError") return;
      warnStream(e && e.message ? e.message : "network error", t);
    }
  })();
}

function renderStatus(s) {
  // Party
  const party = document.querySelector("#party .party");
  party.replaceChildren();
  if (!s.agents || !s.agents.length) {
    party.appendChild(el("div", "no hirelings in this tavern yet", "hint"));
  }
  for (const a of s.agents) {
    const card = el("div", null, "agent");
    card.dataset.agent = a.id;
    const head = el("div", null, "head");
    head.appendChild(sprite(a.id));
    const name = el("div", null, "name");
    name.appendChild(el("div", a.id));
    if (a.pending) name.appendChild(el("div", a.pending + " command(s) waiting", "pending"));
    head.appendChild(name);
    card.appendChild(head);
    card.appendChild(balloon(a));
    party.appendChild(card);
  }

  // Decisions
  const cards = document.querySelector("#decisions .cards");
  cards.replaceChildren();
  if (!s.decisions || !s.decisions.length) {
    cards.appendChild(el("div", "no open decisions", "hint"));
  }
  for (const d of s.decisions) {
    const c = el("div", null, "card");
    if (d.urgency && d.urgency !== "normal") c.appendChild(el("span", d.urgency, "urgency"));
    c.appendChild(el("div", "from " + d.from, "from"));
    c.appendChild(el("div", d.question, "q"));
    const ul = el("ul");
    for (const o of d.options) ul.appendChild(el("li", o));
    c.appendChild(ul);
    if (d.context) c.appendChild(el("div", d.context, "ctx"));
    if (d.card) c.appendChild(el("div", "a yes grants: " + d.card, "ctx"));
    cards.appendChild(c);
  }

  // Interactions
  const chat = document.querySelector("#chat .chat");
  chat.replaceChildren();
  if (!s.interactions || !s.interactions.length) {
    chat.appendChild(el("div", "the table is quiet", "hint"));
  }
  for (const m of s.interactions) {
    const row = el("div", null, "msg");
    row.appendChild(el("span", m.from + " → " + m.to + ": ", "who"));
    row.appendChild(el("span", m.text));
    if (m.reply) row.appendChild(el("div", m.to + ": " + m.reply, "reply"));
    chat.appendChild(row);
  }
}

function renderQuest(q) {
  const box = $("#quest");
  const g = q && (q.quest || q);
  box.replaceChildren();
  if (!g || !g.title) {
    box.appendChild(el("div", "no quest set yet — define one with PUT /goal or GOAL_TITLE", "hint"));
    return;
  }
  const c = el("div", null, "quest");
  const top = el("div", null, "top");
  top.appendChild(el("span", g.title, "title"));
  const pct = Math.max(0, Math.min(100, g.percent || 0));
  top.appendChild(el("span", pct.toFixed(1) + "%", "pct"));
  c.appendChild(top);
  const bar = el("div", null, "bar");
  const fill = el("div", null, "fill");
  fill.style.width = pct + "%";
  bar.appendChild(fill);
  c.appendChild(bar);
  const num = g.currency ? g.currency + " " : "";
  c.appendChild(el("div", num + (g.current || 0) + " / " + g.target, "num"));
  box.appendChild(c);
}

async function refresh() {
  if (!token.value.trim()) {
    document.querySelector("#party .party").replaceChildren(
      el("div", "paste the owner token above (FRONTDESK_OWNER_TOKEN from your .env) to see the party, balloons and decisions.", "hint"));
    document.querySelector("#decisions .cards").replaceChildren();
    document.querySelector("#chat .chat").replaceChildren();
    $("#quest").replaceChildren(el("div", "the quest needs the owner token too", "hint"));
    return;
  }
  // The quest rides on GET /api/status (200, quest null when unset).
  // GET /api/quest stays 404 until a quest exists; fetching it is a console
  // error and used to wipe the party, decisions and table. An unset quest
  // is the empty state in renderQuest, and status still renders.
  let s;
  try {
    s = await api("/api/status");
  } catch (e) {
    document.querySelector("#party .party").replaceChildren(el("div", "could not read the tavern: " + e.message, "hint"));
    document.querySelector("#decisions .cards").replaceChildren();
    document.querySelector("#chat .chat").replaceChildren();
    $("#quest").replaceChildren();
    return;
  }
  applySnapshot(s);
}

token.addEventListener("change", () => { refresh(); startStream(); });
refresh();
startStream();
setInterval(refresh, 5000);
