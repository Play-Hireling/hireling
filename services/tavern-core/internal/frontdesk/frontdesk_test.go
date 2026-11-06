package frontdesk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memPersist struct{ data []byte }

func (m *memPersist) Load(context.Context) ([]byte, error) { return m.data, nil }
func (m *memPersist) Save(_ context.Context, b []byte) error {
	m.data = append([]byte{}, b...)
	return nil
}

func newDesk(t *testing.T, p Persister) (*Desk, *httptest.Server) {
	t.Helper()
	d := &Desk{Agents: []string{"innkeeper", "dev"}, AgentTokens: map[string]string{"dev": "dev-tok"}, SharedToken: "shared", OwnerToken: "owner-tok", Persist: p}
	if err := d.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	d.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return d, srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
	return res.StatusCode
}

func TestOrderAckAndStatus(t *testing.T) {
	_, srv := newDesk(t, nil)
	if code := call(t, srv, "POST", "/api/command", "shared", `{"agent":"dev","text":"x"}`, nil); code != 401 {
		t.Fatalf("hireling token must not order: %d", code)
	}
	var made struct{ Command Command }
	if code := call(t, srv, "POST", "/api/command", "owner-tok", `{"agent":"dev","text":"Fix the README"}`, &made); code != 200 || made.Command.ID == "" {
		t.Fatalf("order: %d %+v", code, made)
	}
	var q struct{ Commands []Command }
	if code := call(t, srv, "GET", "/api/commands/dev", "dev-tok", "", &q); code != 200 || len(q.Commands) != 1 {
		t.Fatalf("queue: %d %+v", code, q)
	}
	if code := call(t, srv, "GET", "/api/commands/innkeeper", "dev-tok", "", nil); code != 401 {
		t.Fatalf("dev's own token must not read the innkeeper's queue: %d", code)
	}
	call(t, srv, "POST", "/api/turn", "dev-tok", `{"agent":"dev","phase":"start","topic":"README"}`, nil)
	call(t, srv, "POST", "/api/commands/dev/ack", "dev-tok", `{"id":"`+made.Command.ID+`"}`, nil)
	if call(t, srv, "GET", "/api/commands/dev", "dev-tok", "", &q); len(q.Commands) != 0 {
		t.Fatalf("received command still pending: %+v", q)
	}
	call(t, srv, "POST", "/api/commands/dev/ack", "dev-tok", `{"id":"`+made.Command.ID+`","result":"done","note":"ok","answer":"Done."}`, nil)
	var st struct {
		Agents []AgentStatus
	}
	if code := call(t, srv, "GET", "/api/status", "owner-tok", "", &st); code != 200 || len(st.Agents) != 2 {
		t.Fatalf("status: %d %+v", code, st)
	}
	dev := st.Agents[0]
	if dev.ID != "dev" || dev.Turn == nil || dev.Turn.Topic != "README" || dev.LastDone == nil || dev.LastDone.Answer != "Done." {
		t.Fatalf("dev row: %+v", dev)
	}
}

func TestLongPollWakesOnOrder(t *testing.T) {
	_, srv := newDesk(t, nil)
	done := make(chan []Command, 1)
	go func() {
		var q struct{ Commands []Command }
		call(t, srv, "GET", "/api/commands/innkeeper?wait=5", "shared", "", &q)
		done <- q.Commands
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	call(t, srv, "POST", "/api/command", "owner-tok", `{"agent":"innkeeper","text":"Plan the launch"}`, nil)
	select {
	case cs := <-done:
		if len(cs) != 1 || time.Since(start) > 2*time.Second {
			t.Fatalf("long poll: %+v after %s", cs, time.Since(start))
		}
	case <-time.After(4 * time.Second):
		t.Fatal("long poll did not wake")
	}
}

func TestDecisionRoundTripAndPersistence(t *testing.T) {
	p := &memPersist{}
	_, srv := newDesk(t, p)
	var opened struct{ Decision Decision }
	if code := call(t, srv, "POST", "/api/decisions", "dev-tok", `{"from":"dev","question":"Merge PR 12?","options":["Merge","Wait"]}`, &opened); code != 200 {
		t.Fatalf("open: %d", code)
	}
	if code := call(t, srv, "POST", "/api/decisions/"+opened.Decision.ID+"/choose", "owner-tok", `{"option":"Lunch"}`, nil); code != 400 {
		t.Fatalf("unknown option accepted: %d", code)
	}
	if code := call(t, srv, "POST", "/api/decisions/"+opened.Decision.ID+"/choose", "owner-tok", `{"option":"merge"}`, nil); code != 200 {
		t.Fatalf("choose: %d", code)
	}
	// A new desk on the same store sees the answer as a command for dev.
	_, srv2 := newDesk(t, p)
	var q struct{ Commands []Command }
	call(t, srv2, "GET", "/api/commands/dev", "dev-tok", "", &q)
	if len(q.Commands) != 1 || q.Commands[0].DecisionID != opened.Decision.ID || !strings.Contains(q.Commands[0].Text, "chosen: Merge (decision ") {
		t.Fatalf("answer command: %+v", q.Commands)
	}
}

func TestGetOneDecision(t *testing.T) {
	_, srv := newDesk(t, nil)
	var opened struct{ Decision Decision }
	if code := call(t, srv, "POST", "/api/decisions", "dev-tok", `{"from":"dev","question":"Merge PR 12?","options":["Merge","Wait"]}`, &opened); code != 200 {
		t.Fatalf("open: %d", code)
	}
	path := "/api/decisions/" + opened.Decision.ID
	// Only the owner reads it: no token, a hireling's own token and the shared one are 401.
	for _, tok := range []string{"", "dev-tok", "shared", "owner-tok-x"} {
		if code := call(t, srv, "GET", path, tok, "", nil); code != 401 {
			t.Fatalf("token %q: %d, want 401", tok, code)
		}
	}
	var got struct{ Decision Decision }
	if code := call(t, srv, "GET", path, "owner-tok", "", &got); code != 200 || got.Decision.ID != opened.Decision.ID || got.Decision.Chosen != "" || got.Decision.Question != "Merge PR 12?" {
		t.Fatalf("open decision: %d %+v", code, got)
	}
	// Answered decisions stay readable, with the choice.
	if code := call(t, srv, "POST", path+"/choose", "owner-tok", `{"option":"wait"}`, nil); code != 200 {
		t.Fatalf("choose: %d", code)
	}
	got = struct{ Decision Decision }{}
	if code := call(t, srv, "GET", path, "owner-tok", "", &got); code != 200 || got.Decision.Chosen != "Wait" {
		t.Fatalf("answered decision: %d %+v", code, got)
	}
	var open struct{ Decisions []Decision }
	call(t, srv, "GET", "/api/decisions", "owner-tok", "", &open)
	if len(open.Decisions) != 0 {
		t.Fatalf("the list must keep showing open decisions only: %+v", open)
	}
	if code := call(t, srv, "GET", "/api/decisions/d-404", "owner-tok", "", nil); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	if code := call(t, srv, "GET", "/api/decisions/d-404", "dev-tok", "", nil); code != 401 {
		t.Fatalf("unknown id must not leak existence to a hireling: %d", code)
	}
}

func TestOwnerClosedWithoutToken(t *testing.T) {
	d := &Desk{Agents: []string{"dev"}, SharedToken: "shared"}
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	if d.IsOwner(r) {
		t.Fatal("no owner token configured: nobody is the owner")
	}
	r.Header.Set("Authorization", "Bearer ")
	if d.IsOwner(r) {
		t.Fatal("an empty bearer must not match an empty owner token")
	}
	d.OwnerToken = "owner-tok"
	r.Header.Set("Authorization", "Bearer owner-tok")
	if !d.IsOwner(r) {
		t.Fatal("owner token not recognised")
	}
	r.Header.Set("Authorization", "Bearer shared")
	if d.IsOwner(r) {
		t.Fatal("the shared hireling token is not the owner's")
	}
}

func TestInteractions(t *testing.T) {
	_, srv := newDesk(t, nil)
	var it struct{ Interaction Interaction }
	if code := call(t, srv, "POST", "/api/interaction", "shared", `{"from":"innkeeper","to":"dev","text":"Can you take T-2?"}`, &it); code != 200 {
		t.Fatalf("new: %d", code)
	}
	if code := call(t, srv, "POST", "/api/interaction/"+it.Interaction.ID+"/reply", "shared", `{"agent":"innkeeper","reply":"x"}`, nil); code != 403 {
		t.Fatalf("sender replied: %d", code)
	}
	if code := call(t, srv, "POST", "/api/interaction/"+it.Interaction.ID+"/reply", "dev-tok", `{"agent":"dev","reply":"On it"}`, nil); code != 200 {
		t.Fatalf("reply: %d", code)
	}
	var list struct{ Interactions []Interaction }
	call(t, srv, "GET", "/api/interactions", "owner-tok", "", &list)
	if len(list.Interactions) != 1 || list.Interactions[0].Reply != "On it" {
		t.Fatalf("list: %+v", list)
	}
}
