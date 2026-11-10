"use strict";
const $ = (sel) => document.querySelector(sel);
const token = document.getElementById("token");
token.value = localStorage.getItem("tavernToken") || "";
token.addEventListener("change", () => localStorage.setItem("tavernToken", token.value.trim()));

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

function renderStatus(s) {
  // Party
  const party = document.querySelector("#party .party");
  party.replaceChildren();
  if (!s.agents || !s.agents.length) {
    party.appendChild(el("div", "no hirelings in this tavern yet", "hint"));
  }
  for (const a of s.agents) {
    const card = el("div", null, "agent");
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
  const noToken = document.querySelector("#party .party") === null || !token.value.trim();
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
  renderStatus(s);
  renderQuest(s.quest);
}

token.addEventListener("change", refresh);
refresh();
setInterval(refresh, 5000);
