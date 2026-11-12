package httpapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPanelConsole runs the owner panel in Chromium through Playwright at
// 1440×900 and 390×844, with and without a quest. It requires node,
// playwright-core and Chrome; otherwise it skips so `go test` stays green
// where those are not installed. Set PLAYWRIGHT_CORE to the directory that
// contains the playwright-core package (its parent node_modules, or the
// package directory's parent).
func TestPanelConsole(t *testing.T) {
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
	script := filepath.Join("panel_console.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}

	empty := newOwnerServer(t, true)
	run := func(srvURL, route, expect string) {
		t.Helper()
		cmd := exec.Command(node, script, srvURL, testOwnerToken, expect, route)
		cmd.Env = append(os.Environ(), "PLAYWRIGHT_CORE="+root, "CHROME_PATH="+chrome)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("panel console %q: %v\n%s", expect, err, out)
		}
		t.Logf("%s", out)
	}
	run(empty.URL, "/", "no quest set yet")

	with := newOwnerServer(t, true)
	if code := call(t, with, "PUT", "/goal", "owner", `{"title":"Net revenue","target":1000}`, nil); code != 200 {
		t.Fatalf("set quest: %d", code)
	}
	run(with.URL, "/", "Net revenue")

	// OS5: with a client bundle mounted, / is the tavern and the panel is at
	// /panel/. The panel keeps the same console and CSP behavior there.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<!doctype html><html><body>GAME</body></html>")
	mounted := newOwnerServerWithClient(t, true, dir)
	run(mounted.URL, "/panel/", "no quest set yet")
}
