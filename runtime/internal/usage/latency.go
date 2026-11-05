package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Latency is the p50/p95 of the measured turns of one day (A13).
type Latency struct {
	Turns              int
	FirstP50, FirstP95 time.Duration
	TotalP50, TotalP95 time.Duration
}

// String is the `hireling check` line.
func (l Latency) String() string {
	if l.Turns == 0 {
		return "no measured turn today"
	}
	return fmt.Sprintf("%d turns today · first words p50 %.1fs p95 %.1fs · end p50 %.1fs p95 %.1fs",
		l.Turns, l.FirstP50.Seconds(), l.FirstP95.Seconds(), l.TotalP50.Seconds(), l.TotalP95.Seconds())
}

// Latencies reads <dir>/<day>.jsonl and returns p50/p95 of the turns that
// carry a latency (API turns). Unreadable lines are skipped.
func Latencies(dir string, day time.Time) Latency {
	f, err := os.Open(filepath.Join(dir, day.UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		return Latency{}
	}
	defer f.Close()
	var first, total []time.Duration
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var t Turn
		if json.Unmarshal(sc.Bytes(), &t) != nil || t.TotalMS <= 0 {
			continue
		}
		total = append(total, time.Duration(t.TotalMS)*time.Millisecond)
		if t.FirstWordsMS > 0 {
			first = append(first, time.Duration(t.FirstWordsMS)*time.Millisecond)
		}
	}
	return Latency{Turns: len(total), FirstP50: pct(first, 50), FirstP95: pct(first, 95), TotalP50: pct(total, 50), TotalP95: pct(total, 95)}
}

// pct is the nearest-rank percentile.
func pct(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := (p*len(s) + 99) / 100
	if i < 1 {
		i = 1
	}
	return s[i-1]
}
