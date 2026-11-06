package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// OS5: the plain-art client is served at the root as self-contained HTML, and
// reads only the owner's /api/* endpoints with the owner token. It never names
// the owner or the private office panel.
func TestClientServed(t *testing.T) {
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}),
		Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory()}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{"Tavern", "/api/status", "/api/quest", "FRONTDESK_OWNER_TOKEN"} {
		if !strings.Contains(body, want) {
			t.Errorf("client page missing %q", want)
		}
	}
	for _, bad := range []string{"pedro", "escritorio"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("client page leaks %q", bad)
		}
	}
}
