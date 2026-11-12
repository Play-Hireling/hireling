package loop

// The turn-file write recovers a stale file it can remove, and a write that
// still fails (a foreign file in a sticky dir) is logged once, with the euid,
// not once per turn.

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTurnFileWriteRecovery(t *testing.T) {
	_, h := newServer(t)

	// A path that is a directory makes the write fail, and an empty one is
	// removed by the retry, which then creates the file: the stale-file path.
	l := newLoop(h, &fakeTurner{out: "done"})
	l.GateTurnFile = filepath.Join(t.TempDir(), "gate-turn.json")
	if err := os.Mkdir(l.GateTurnFile, 0o755); err != nil {
		t.Fatal(err)
	}
	l.writeGateTurn(context.Background(), "c-1", "", "open a PR")
	if raw, err := os.ReadFile(l.GateTurnFile); err != nil || !strings.Contains(string(raw), `"turn":"c-1"`) {
		t.Fatalf("not recovered: %s (%v)", raw, err)
	}

	// A non-empty directory cannot be removed: the failure is logged once.
	l2 := newLoop(h, &fakeTurner{out: "done"})
	dir := t.TempDir()
	l2.GateTurnFile = filepath.Join(dir, "gate-turn.json")
	if err := os.MkdirAll(filepath.Join(dir, "gate-turn.json", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The mode the directory was born with (the umask decides it).
	born, err := os.Stat(l2.GateTurnFile)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	l2.Log = log.New(&buf, "", 0)
	l2.writeGateTurn(context.Background(), "c-1", "", "x")
	l2.writeGateTurn(context.Background(), "c-2", "", "x")
	if n := strings.Count(buf.String(), "gate turn file"); n != 1 {
		t.Fatalf("logged %d times, want once: %q", n, buf.String())
	}
	if !strings.Contains(buf.String(), "euid=") {
		t.Fatalf("the log does not name the euid: %q", buf.String())
	}
	// The directory at the path keeps its mode: a chmod 0640 here would take
	// away its traverse bit and break everyone, the temp-dir cleanup included.
	if fi, err := os.Stat(l2.GateTurnFile); err != nil || !fi.IsDir() || fi.Mode() != born.Mode() {
		t.Fatalf("the directory at the path changed mode: %v, was %v (%v)", fi, born, err)
	}

	// A file this process owns but that is read-only is restored by chmod and
	// rewritten in place — the owner's mode fix, no directory write needed.
	l3 := newLoop(h, &fakeTurner{out: "done"})
	l3.GateTurnFile = filepath.Join(t.TempDir(), "gate-turn.json")
	if err := os.WriteFile(l3.GateTurnFile, []byte("stale"), 0o444); err != nil {
		t.Fatal(err)
	}
	l3.writeGateTurn(context.Background(), "c-9", "", "open a PR")
	if raw, err := os.ReadFile(l3.GateTurnFile); err != nil || !strings.Contains(string(raw), `"turn":"c-9"`) {
		t.Fatalf("read-only file not restored: %s (%v)", raw, err)
	}
}
