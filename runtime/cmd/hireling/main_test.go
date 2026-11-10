package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestNoteAllowHTTP(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	for _, v := range []string{"", "0", "2", "true", "TRUE", "yes", "on", "1 ", " 1", "1\n"} {
		buf.Reset()
		if noteAllowHTTP(logger, v) || buf.Len() != 0 {
			t.Fatalf("%q enabled plain http: %q", v, buf.String())
		}
	}
	buf.Reset()
	if !noteAllowHTTP(logger, "1") {
		t.Fatal(`"1" did not enable plain http`)
	}
	if got := buf.String(); !strings.Contains(got, "FRONTDESK_ALLOW_HTTP=1") || strings.Count(got, "\n") != 1 {
		t.Fatalf("log = %q, want one line naming FRONTDESK_ALLOW_HTTP=1", got)
	}
}
