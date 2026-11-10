package frontdesk

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadyURL(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		allow bool
		want  string // substring of the error; empty means the URL is accepted
	}{
		{name: "https", url: "https://tavern.example", want: ""},
		{name: "loopback v4", url: "http://127.0.0.1:8080", want: ""},
		{name: "loopback v6", url: "http://[::1]:8080", want: ""},
		{name: "compose name blocked", url: "http://tavern-core:8080", want: "must be https://"},
		{name: "compose name allowed", url: "http://tavern-core:8080", allow: true, want: ""},
		{name: "localhost name is not loopback", url: "http://localhost:8080", want: "must be https://"},
		{name: "lookalike loopback", url: "http://127.0.0.1.example:8080", want: "must be https://"},
		{name: "userinfo looks like loopback", url: "http://127.0.0.1@evil", want: "credentials"},
		{name: "userinfo looks like loopback with flag", url: "http://127.0.0.1@evil", allow: true, want: "credentials"},
		{name: "127.x is not 127.0.0.1", url: "http://127.0.0.2:8080", want: "must be https://"},
		{name: "127 shorthand", url: "http://127.1:8080", want: "must be https://"},
		{name: "ipv4-mapped ipv6", url: "http://[::ffff:127.0.0.1]:8080", want: "must be https://"},
		{name: "ipv4-mapped hex", url: "http://[::ffff:7f00:1]:8080", want: "must be https://"},
		{name: "credentials", url: "http://user:not-a-password@tavern-core:8080", allow: true, want: "credentials"},
		{name: "empty", url: "", want: ErrNotConfigured.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{
				BaseURL:   tc.url,
				Token:     "tok-test",
				AllowHTTP: tc.allow,
				HTTP:      &http.Client{Timeout: 200 * time.Millisecond},
			}
			if tc.url == "" {
				c.Token = ""
			}
			_, err := c.Commands(context.Background(), "innkeeper")
			if tc.want == "" {
				if err != nil && (strings.Contains(err.Error(), "must be https://") || strings.Contains(err.Error(), "credentials")) {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "0:0:0:0:0:0:0:1"} {
		if !loopbackHost(host) {
			t.Fatalf("%s should be loopback", host)
		}
	}
	for _, host := range []string{
		"127.0.0.1@evil",
		"127.0.0.2",
		"127.0.0.10",
		"127.1",
		"127.0.0.1.evil",
		"::ffff:127.0.0.1",
		"::ffff:7f00:1",
		"0:0:0:0:0:ffff:127.0.0.1",
		"::1%lo",
		"localhost",
		"",
	} {
		if loopbackHost(host) {
			t.Fatalf("%s treated as loopback", host)
		}
	}
}

func TestAllowHTTPEnv(t *testing.T) {
	if !AllowHTTPEnv("1") {
		t.Fatal(`"1" must enable plain http`)
	}
	for _, v := range []string{"", "0", "2", "true", "TRUE", "yes", "on", "1 ", " 1", "1\n"} {
		if AllowHTTPEnv(v) {
			t.Fatalf("%q must not enable plain http", v)
		}
	}
}
