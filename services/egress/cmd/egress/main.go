// egress: the tavern's forward proxy (A8). Hirelings sit on an internal Docker
// network and reach the internet only through this process, which allows each
// one exactly the hosts in its profile (`network.allow` + the base list).
//
//	EGRESS_ADDR         listen address (default :3128)
//	EGRESS_AGENTS_DIR   agents/<id>/profile.yaml root (default /agents)
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Play-Hireling/hireling/services/egress/internal/proxy"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	logger := log.New(os.Stderr, "egress ", log.LstdFlags|log.LUTC)
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}
	ps, err := proxy.LoadDir(env("EGRESS_AGENTS_DIR", "/agents"))
	if err != nil {
		logger.Fatalf("profiles: %v", err)
	}
	allow := proxy.FromProfiles(ps)
	for id, hosts := range allow {
		logger.Printf("hireling %s may reach %v", id, hosts)
	}
	p := &proxy.Proxy{Allow: allow, Identify: proxy.DockerIdentify, Log: logger}
	// No ServeMux: CONNECT requests have no path and absolute-URI requests must
	// reach the proxy untouched. Only the local health probe is answered here.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect && !r.URL.IsAbs() && r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		p.ServeHTTP(w, r)
	})
	hs := &http.Server{
		Addr:              env("EGRESS_ADDR", ":3128"),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	logger.Printf("egress listening on %s for %d hirelings", hs.Addr, len(allow))
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}

// healthcheck is the Docker HEALTHCHECK (distroless image: no curl).
func healthcheck() int {
	addr := env("EGRESS_ADDR", ":3128")
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != http.StatusOK {
		return 1
	}
	res.Body.Close()
	return 0
}
