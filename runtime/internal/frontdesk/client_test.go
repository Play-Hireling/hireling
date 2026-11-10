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
