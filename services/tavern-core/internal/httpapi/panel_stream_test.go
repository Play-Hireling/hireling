package httpapi

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// TestPanelStream is A13's panel proof. panel_stream.mjs posts real turns to
// the Front Desk while the owner panel is open: the streamed step must reach
// the balloon before the turn ends, a stream that closes without events must
// fall back to the 5 s poll (once, not in a hot loop), and the panel must open
// with and without the token at the five resolutions without a new console error.
//
// It fails, not skips, when the browser toolchain is missing (#48): CI installs
// Chromium, so a green run here means the proof actually ran.
func TestPanelStream(t *testing.T) {
	b := requireBrowser(t)
	node, chrome, root := b.node, b.chrome, b.root
	script := "panel_stream.mjs"
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}

	srv := newOwnerServer(t, false)
	cmd := exec.Command(node, script, srv.URL, testOwnerToken, "test-tok")
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_CORE="+root, "CHROME_PATH="+chrome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("panel stream: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte(`"ok": true`)) {
		t.Fatalf("panel stream output: %s", out)
	}
	t.Logf("%s", out)
}
