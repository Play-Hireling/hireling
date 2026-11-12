package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// TestTavernPlaysWithoutCSPViolation loads /tavern/ in Chromium at 1440,
// 390, 2560×1080 and 3440×1440. The page is the self-host client shape
// (token screen, walk, order, decision) served by tavern-core, so the CSP
// in security.go is the one the browser enforces. It fails, not skips, when
// node, Chrome or playwright-core is missing (#48).
func TestTavernPlaysWithoutCSPViolation(t *testing.T) {
	b := requireBrowser(t)
	node, chrome, root := b.node, b.chrome, b.root

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), tavernPlayHTML)
	writeFile(t, filepath.Join(dir, "play.js"), tavernPlayJS)

	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	approver := func(a string) bool { return a == "owner" }
	s := &Server{
		Gate:      policy.New(p),
		Tasks:     &tasks.Service{Store: tasks.NewMemory(), Approver: approver},
		Usage:     usage.NewMemory(),
		Goal:      &goal.Service{Store: goal.NewMemory(), Approver: approver},
		ClientDir: dir,
	}
	desk := &frontdesk.Desk{Agents: []string{"test"}, AgentTokens: map[string]string{"test": "test-tok"}, SharedToken: "shared-tok", OwnerToken: "owner-tok"}
	if err := desk.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Extra, s.Owner = desk.Register, desk.IsOwner
	srv := listen(s, desk.OwnerToken)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/decisions", strings.NewReader(`{"from":"test","question":"Ship it?","options":["Ship it","Wait"]}`))
	req.Header.Set("Authorization", "Bearer test-tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("plant decision: %d", res.StatusCode)
	}
	for i := 0; i < 3; i++ {
		again, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/decisions", strings.NewReader(`{"from":"test","question":"Ship it?","options":["Ship it","Wait"]}`))
		again.Header.Set("Authorization", "Bearer test-tok")
		again.Header.Set("Content-Type", "application/json")
		more, err := http.DefaultClient.Do(again)
		if err != nil {
			t.Fatal(err)
		}
		more.Body.Close()
		if more.StatusCode != http.StatusOK {
			t.Fatalf("plant decision: %d", more.StatusCode)
		}
	}

	cmd := exec.Command(node, "tavern_play.mjs", srv.URL, desk.OwnerToken)
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_CORE="+root, "CHROME_PATH="+chrome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tavern play: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte(`"ok":true`)) {
		t.Fatalf("play output: %s", out)
	}
	t.Logf("%s", out)
}

const tavernPlayHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Tavern</title><link rel="icon" href="data:,"></head>
<body>
<div id="token-screen"><form id="enter-form"><input id="token" type="password" autocomplete="off"><button id="enter" type="submit">Enter</button></form></div>
<div id="stick" style="width:48px;height:48px;position:fixed;left:8px;bottom:8px"></div>
<div id="player" style="position:absolute;left:10px;top:40px;width:8px;height:8px;background:#c60"></div>
<button id="order" type="button">Order</button>
<button id="answer" type="button">Ship it</button>
<script src="play.js"></script>
</body></html>
`

const tavernPlayJS = `"use strict";
const token = document.getElementById("token");
const screen = document.getElementById("token-screen");
document.getElementById("enter-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  sessionStorage.setItem("tavernToken", token.value.trim());
  screen.hidden = true;
  void pull();
});
function auth() {
  return { Authorization: "Bearer " + sessionStorage.getItem("tavernToken"), "Content-Type": "application/json" };
}
async function pull() {
  const res = await fetch("/api/status", { headers: auth(), cache: "no-store" });
  document.body.dataset.status = String(res.status);
  if (!res.ok) return;
  const body = await res.json();
  const open = (body.decisions || []).find((d) => !d.chosen);
  document.body.dataset.decisions = String((body.decisions || []).length);
  if (open) document.body.dataset.decision = String(open.id);
}
const player = document.getElementById("player");
function step(dx) {
  const left = parseFloat(player.style.left) || 0;
  player.style.left = (left + dx) + "px";
}
addEventListener("keydown", (ev) => {
  if (ev.key === "d" || ev.key === "D" || ev.key === "ArrowRight") step(12);
  if (ev.key === "a" || ev.key === "A" || ev.key === "ArrowLeft") step(-12);
});
addEventListener("touchmove", () => step(12), { passive: true });
document.getElementById("order").addEventListener("click", async () => {
  const res = await fetch("/api/command", { method: "POST", headers: auth(), body: JSON.stringify({ agent: "test", text: "Plan a launch post" }) });
  if (res.ok) document.body.dataset.ordered = "1";
});
document.getElementById("answer").addEventListener("click", async () => {
  const id = document.body.dataset.decision;
  if (!id) return;
  const res = await fetch("/api/decisions/" + encodeURIComponent(id) + "/choose", { method: "POST", headers: auth(), body: JSON.stringify({ option: "Ship it" }) });
  if (res.ok) document.body.dataset.answered = "1";
});
`
