package profile

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyPlanProfile(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "agents", "innkeeper"))
	if err != nil {
		t.Fatalf("agents/innkeeper must load: %v", err)
	}
	if p.DailyPlan.At == "" {
		t.Fatal("the Innkeeper posts the daily plan (B10): daily_plan.at is missing")
	}
	for _, c := range []struct{ body, want string }{
		{"daily_plan: {at: \"08:00\"}\n", "planner"},
		{"planner: true\ndaily_plan: {at: \"8am\"}\n", "HH:MM"},
		{"planner: true\ndaily_plan: {at: \"25:00\"}\n", "HH:MM"},
		{"planner: true\ndaily_plan: {tz: America/Montevideo}\n", "HH:MM"},
		{"planner: true\ndaily_plan: {at: \"08:00\", tz: Mars/Olympus}\n", "daily_plan.tz"},
	} {
		dir := t.TempDir()
		write(t, dir, "profile.yaml", "id: x\nmodel: deepseek-v4-pro\n"+c.body)
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%q: %v (want %q)", c.body, err, c.want)
		}
	}
}

func TestDailyPlanDue(t *testing.T) {
	d := DailyPlan{At: "08:00", TZ: "America/Montevideo"} // UTC-3
	for _, c := range []struct {
		utc  string
		day  string
		due  bool
		note string
	}{
		{"2026-11-05T10:59:00Z", "2026-11-05", false, "07:59 in Montevideo"},
		{"2026-11-05T11:00:00Z", "2026-11-05", true, "08:00 in Montevideo"},
		{"2026-11-06T01:30:00Z", "2026-11-05", true, "22:30 the evening before: still the 5th"},
		{"2026-11-06T03:00:00Z", "2026-11-06", false, "midnight: a new day, not due yet"},
	} {
		now, _ := time.Parse(time.RFC3339, c.utc)
		day, due, err := d.Due(now)
		if err != nil || day != c.day || due != c.due {
			t.Fatalf("%s (%s): day=%s due=%v err=%v", c.utc, c.note, day, due, err)
		}
	}
	if _, due, err := (DailyPlan{}).Due(time.Now()); due || err != nil {
		t.Fatal("off is never due")
	}
	// No tz = the tavern's clock.
	if day, due, _ := (DailyPlan{At: "08:00"}).Due(time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)); day != "2026-11-05" || !due {
		t.Fatal(day, due)
	}
}
