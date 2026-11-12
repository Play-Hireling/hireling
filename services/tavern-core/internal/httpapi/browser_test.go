package httpapi

// The panel proofs (TestPanelStream, TestPanelConsole,
// TestTavernPlaysWithoutCSPViolation) drive a real browser. They used to skip
// when node, Chrome or playwright-core was missing, and the job that runs them
// had none of the three: the owner panel's proof silently stopped running in
// CI (#48), the same shape as the asks date-bomb that nobody saw until the
// clock crossed the TTL.
//
// A missing browser is now a failure. The only escape is the explicit
// HIRELING_BROWSER_TESTS=off, which is meant for a dev machine without a
// browser, is loud in the log, and is never set by CI.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// browserOptOut is the single way to skip a browser proof. It must be a
// deliberate "off": an empty or unknown value keeps the proof mandatory.
const browserOptOut = "HIRELING_BROWSER_TESTS"

// browserProof is the toolchain a proof launches: node, the Chromium binary,
// and the directory that holds the playwright-core package.
type browserProof struct {
	node   string
	chrome string
	root   string
}

// requireBrowser resolves the browser toolchain or fails the test with the
// command that fixes it. It never skips on its own.
func requireBrowser(t *testing.T) browserProof {
	t.Helper()
	if strings.EqualFold(strings.TrimSpace(os.Getenv(browserOptOut)), "off") {
		t.Skipf("%s=off: browser proof skipped on purpose (CI must not set it; #48)", browserOptOut)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is required for the browser proof (#48): install node, or set %s=off on a machine without a browser", browserOptOut)
	}
	root := strings.TrimSpace(os.Getenv("PLAYWRIGHT_CORE"))
	if root == "" {
		root = "/tmp/panel-pw/node_modules"
	}
	if st, err := os.Stat(filepath.Join(root, "playwright-core")); err != nil || !st.IsDir() {
		t.Fatalf("playwright-core is required for the browser proof (#48) under %s: npm install --no-save --prefix /tmp/panel-pw playwright-core, or set %s=off", root, browserOptOut)
	}
	chrome := findChrome()
	if chrome == "" {
		t.Fatalf("a Chromium binary is required for the browser proof (#48): set CHROME_PATH, run playwright-core install chromium, or set %s=off", browserOptOut)
	}
	t.Logf("browser proof: node %s, chrome %s, playwright-core %s", node, chrome, root)
	return browserProof{node: node, chrome: chrome, root: root}
}

// findChrome looks at CHROME_PATH first, then the distro paths, then the
// Playwright download cache that CI fills (PLAYWRIGHT_BROWSERS_PATH, else
// ~/.cache/ms-playwright). Glob returns sorted hits, so the last one wins and
// the newest browser revision is used.
func findChrome() string {
	if p := strings.TrimSpace(os.Getenv("CHROME_PATH")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	for _, p := range []string{"/usr/local/bin/google-chrome", "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	var caches []string
	if d := strings.TrimSpace(os.Getenv("PLAYWRIGHT_BROWSERS_PATH")); d != "" {
		caches = append(caches, d)
	}
	if home, err := os.UserHomeDir(); err == nil {
		caches = append(caches, filepath.Join(home, ".cache", "ms-playwright"))
	}
	for _, dir := range caches {
		for _, pattern := range []string{"chromium-*/chrome-linux64/chrome", "chromium-*/chrome-linux/chrome"} {
			hits, err := filepath.Glob(filepath.Join(dir, pattern))
			if err == nil && len(hits) > 0 {
				return hits[len(hits)-1]
			}
		}
	}
	return ""
}
