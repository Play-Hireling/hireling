package httpapi

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPanelStream is A13's panel proof. panel_stream.mjs posts real turns to
// the Front Desk while the owner panel is open: the streamed step must reach
// the balloon before the turn ends, a stream that closes without events must
// fall back to the 5 s poll (once, not in a hot loop), and the panel must open
// with and without the token at the five resolutions without a new console error.
//
// It skips where node, Chrome or playwright-core are not installed, the same
// way the panel console test does.
func TestPanelStream(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	chrome := os.Getenv("CHROME_PATH")
	if chrome == "" {
		for _, p := range []string{"/usr/local/bin/google-chrome", "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser"} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				chrome = p
				break
			}
		}
	}
	if chrome == "" {
		t.Skip("chrome not installed")
	}
	root := os.Getenv("PLAYWRIGHT_CORE")
	if root == "" {
		root = "/tmp/panel-pw/node_modules"
	}
	if _, err := os.Stat(filepath.Join(root, "playwright-core")); err != nil {
		t.Skip("playwright-core not installed")
	}
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
