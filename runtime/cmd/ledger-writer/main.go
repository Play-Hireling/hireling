// Command ledger-writer is E3: it commits hirelings' Ledger pages to the brain
// repo as their author. See internal/ledgerwriter.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/ledgerwriter"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	logger := log.New(os.Stderr, "ledger-writer: ", log.LstdFlags|log.LUTC)
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
		res, err := c.Get("http://127.0.0.1" + env("LEDGER_WRITER_ADDR", ":8090") + "/health")
		if err != nil || res.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	w := &ledgerwriter.Writer{
		Dir:       env("LEDGER_WRITER_DIR", "/repo/hireling-ledger"),
		RepoURL:   os.Getenv("LEDGER_REPO_URL"),
		Token:     os.Getenv("LEDGER_GIT_TOKEN"),
		AgentsDir: env("HIRELING_AGENTS_DIR", "/agents"),
		Identify:  ledgerwriter.DockerIdentify,
		Log:       logger,
	}
	if w.RepoURL == "" {
		logger.Fatal("LEDGER_REPO_URL is not set: the brain repo this tavern writes to (docs/guides/ledger.md)")
	}
	if w.Token == "" {
		logger.Printf("LEDGER_GIT_TOKEN unset: commits will fail to push")
	}
	addr := env("LEDGER_WRITER_ADDR", ":8090")
	logger.Printf("listening on %s, repo %s", addr, w.RepoURL)
	srv := &http.Server{Addr: addr, Handler: w.Handler(), ReadHeaderTimeout: 10 * time.Second}
	logger.Fatal(srv.ListenAndServe())
}
