// Package proxy is the tavern's only door to the internet (feature A8, "network
// limited to allowlisted hosts per role"). Hirelings live on an internal Docker
// network with no route out; HTTP_PROXY/HTTPS_PROXY point them here. For every
// request the proxy works out which hireling is calling (reverse DNS of the
// client address on the Docker network → container name `…-agent-<id>-N`) and
// lets it through only to that profile's allowed hosts on ports 80/443.
// Decisions are logged as `allow`/`deny` with agent and host — never a path,
// never a body: the proxy only ever sees CONNECT host:port for HTTPS.
package proxy

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

// Identify maps a client IP to a hireling id ("" = unknown). The default uses
// reverse DNS on the Docker network; tests inject a map.
type Identify func(ctx context.Context, ip string) string

// Proxy holds the per-hireling allowlists.
type Proxy struct {
	Allow    map[string][]string // hireling id → allowed hosts
	Identify Identify
	Log      *log.Logger
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	Ports    map[string]bool // allowed destination ports; nil = 80 and 443
}

// FromProfiles builds the allowlists from agents/<id>/profile.yaml.
func FromProfiles(ps []*profile.Profile) map[string][]string {
	m := make(map[string][]string, len(ps))
	for _, p := range ps {
		m[p.ID] = p.AllowedHosts()
	}
	return m
}

// LoadDir reads every agents/<id>/profile.yaml under dir.
func LoadDir(dir string) ([]*profile.Profile, error) {
	entries, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	var ps []*profile.Profile
	for _, e := range entries {
		p, err := profile.Load(e)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, nil
}

var allowedPorts = map[string]bool{"443": true, "80": true}

// agentRe finds `agent-<id>-<n>` in a container name such as
// `hireling-abc123-agent-social-media-1.hireling-abc123_tavern.`.
var agentRe = regexp.MustCompile(`(?:^|[-_.])agent-([a-z][a-z0-9-]*?)-[0-9]+(?:[._]|$)`)

// AgentFromName extracts the hireling id from a container (PTR) name.
func AgentFromName(name string) string {
	m := agentRe.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return ""
	}
	return m[1]
}

// DockerIdentify resolves the client IP through the embedded Docker DNS.
func DockerIdentify(ctx context.Context, ip string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil {
		return ""
	}
	for _, n := range names {
		if id := AgentFromName(n); id != "" {
			return id
		}
	}
	return ""
}

func (p *Proxy) logf(format string, a ...any) {
	if p.Log != nil {
		p.Log.Printf(format, a...)
	}
}

// agent resolves the caller on every request: the lookup goes to Docker's
// embedded DNS (local, sub-millisecond) and Docker reuses an address as soon as
// a container goes away, so nothing is cached (found: with a cache, a
// stranger that took the IP of a finished hireling inherited its identity).
func (p *Proxy) agent(ctx context.Context, remote string) string {
	ip, _, err := net.SplitHostPort(remote)
	if err != nil {
		ip = remote
	}
	if p.Identify == nil {
		return ""
	}
	return p.Identify(ctx, ip)
}

func (p *Proxy) ports() map[string]bool {
	if p.Ports != nil {
		return p.Ports
	}
	return allowedPorts
}

// Decide is the policy: which hireling, is the host:port allowed, why not.
func (p *Proxy) Decide(ctx context.Context, remote, hostport string) (agent string, ok bool, reason string) {
	agent = p.agent(ctx, remote)
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, "443"
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch {
	case agent == "":
		return "", false, "unknown client"
	case !p.ports()[port]:
		return agent, false, "port " + port
	case net.ParseIP(host) != nil:
		return agent, false, "ip literal"
	}
	allow, known := p.Allow[agent]
	if !known {
		return agent, false, "no profile"
	}
	if !profile.HostAllowed(host, allow) {
		return agent, false, "not on allowlist"
	}
	// D6, second line: even an allowed suffix never reaches a finance, KYC or
	// legal system (e.g. an allow of `example.com` does not open `bank.example.com`).
	if why := profile.SensitiveHost(host); why != "" {
		return agent, false, "data rule (D6): " + why
	}
	return agent, true, ""
}

// ServeHTTP handles CONNECT (HTTPS tunnels) and absolute-URI plain HTTP.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "egress: this is a forward proxy", http.StatusBadRequest)
		return
	}
	target := r.URL.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "80")
	}
	agent, ok, why := p.Decide(r.Context(), r.RemoteAddr, target)
	if !ok {
		p.logf("deny  %-14s %-40s %s", agent, target, why)
		http.Error(w, "egress: "+why, http.StatusForbidden)
		return
	}
	p.logf("allow %-14s %s", agent, target)
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = r.URL.Host
			req.RequestURI = ""
			req.Header.Del("Proxy-Connection")
			req.Header.Del("Proxy-Authorization")
		},
		Transport: &http.Transport{DialContext: p.dial, Proxy: nil, ResponseHeaderTimeout: 60 * time.Second},
	}
	rp.ServeHTTP(w, r)
}

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	return d.DialContext(ctx, network, addr)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}
	agent, ok, why := p.Decide(r.Context(), r.RemoteAddr, target)
	if !ok {
		p.logf("deny  %-14s %-40s %s", agent, target, why)
		http.Error(w, "egress: "+why, http.StatusForbidden)
		return
	}
	upstream, err := p.dial(r.Context(), "tcp", target)
	if err != nil {
		p.logf("fail  %-14s %-40s %v", agent, target, err)
		http.Error(w, "egress: upstream unreachable", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "egress: hijack unsupported", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	p.logf("allow %-14s %s", agent, target)
	go pipe(upstream, client, buf)
}

type flusher interface{ Flush() error }

func pipe(upstream, client net.Conn, buf interface {
	io.Reader
	io.Writer
}) {
	defer upstream.Close()
	defer client.Close()
	if f, ok := buf.(flusher); ok {
		_ = f.Flush()
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, buf); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	// Half-close: let the other direction drain for a moment, then close both.
	if tc, ok := upstream.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// readDir lists the agent directories (those with a profile.yaml) under dir.
func readDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(d, "profile.yaml")); err == nil {
			out = append(out, d)
		}
	}
	return out, nil
}
