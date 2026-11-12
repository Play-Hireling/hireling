package httpapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPanelConsole runs the owner panel in Chromium through Playwright at
// 1440×900 and 390×844, with and without a quest. It requires node,
// playwright-core and a Chromium binary, and fails without them (#48); a dev
// machine can set HIRELING_BROWSER_TESTS=off. Set PLAYWRIGHT_CORE to the
// directory that contains the playwright-core package (its parent
// node_modules) and CHROME_PATH to the browser, when they are not in the
// defaults requireBrowser knows.
func TestPanelConsole(t *testing.T) {
	b := requireBrowser(t)
	node, chrome, root := b.node, b.chrome, b.root
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
