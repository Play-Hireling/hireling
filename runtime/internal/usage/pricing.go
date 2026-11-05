package usage

import (
	"strings"
	"time"
)

// Rates are US$ per 1M tokens at peak hours. Source: DeepSeek pricing page
// (https://api-docs.deepseek.com/quick_start/pricing, read 2026-11-03, re-checked 2026-11-05).
// Off-peak is half of every rate.
type Rates struct {
	CacheHit  float64 // input tokens served from DeepSeek's prefix cache
	CacheMiss float64 // input tokens computed
	Output    float64 // completion tokens (reasoning tokens are billed as output)
}

// Peak rates per model family. `deepseek-v4-flash` is a legacy name served
// and billed as `deepseek-flash`.
var peak = map[string]Rates{
	"flash": {CacheHit: 0.006, CacheMiss: 0.30, Output: 1.20},
	"pro":   {CacheHit: 0.044, CacheMiss: 1.32, Output: 3.96},
}

// OffPeakFactor is the discount outside peak hours.
const OffPeakFactor = 0.5

// family maps any DeepSeek model id (with or without a provider prefix) to a
// pricing family; "" when the model is not DeepSeek.
func family(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if !strings.HasPrefix(m, "deepseek") {
		return ""
	}
	switch {
	case strings.Contains(m, "flash"):
		return "flash"
	case strings.Contains(m, "pro"):
		return "pro"
	}
	return ""
}

// IsPeak reports whether t falls in DeepSeek's peak window: 01:00–04:00 and
// 06:00–10:00 UTC, Monday to Friday. Chinese public holidays (off-peak all
// day) are not modelled: on those days the cost is slightly overestimated,
// which is the safe direction for a budget.
func IsPeak(t time.Time) bool {
	u := t.UTC()
	if wd := u.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := u.Hour()
	return (h >= 1 && h < 4) || (h >= 6 && h < 10)
}

// OffPeakAt is when the next off-peak window starts: t itself when t is
// already off-peak, otherwise the end of the current peak window (04:00 or
// 10:00 UTC).
func OffPeakAt(t time.Time) time.Time {
	if !IsPeak(t) {
		return t
	}
	u := t.UTC()
	end := 4
	if u.Hour() >= 6 {
		end = 10
	}
	return time.Date(u.Year(), u.Month(), u.Day(), end, 0, 0, 0, time.UTC)
}

// Discounted reports whether model has DeepSeek's off-peak discount.
func Discounted(model string) bool { return family(model) != "" }

// RatesFor returns the rates in force for a DeepSeek model at t, and false
// when the model has no known price (then cost stays 0 and Priced is false).
func RatesFor(model string, t time.Time) (Rates, bool) {
	r, ok := peak[family(model)]
	if !ok {
		return Rates{}, false
	}
	if !IsPeak(t) {
		r = Rates{r.CacheHit * OffPeakFactor, r.CacheMiss * OffPeakFactor, r.Output * OffPeakFactor}
	}
	return r, true
}

// Cost prices one record at the rates in force at `at`. Only DeepSeek's own
// endpoint is priced: a turn served by the OpenRouter fallback carries other
// rates, so it is recorded with Priced=false and cost 0.
func Cost(r Record, at time.Time) (float64, bool) {
	if !isDeepSeekEndpoint(r.BaseURL) {
		return 0, false
	}
	rates, ok := RatesFor(r.Model, at)
	if !ok {
		return 0, false
	}
	const m = 1_000_000.0
	cost := float64(r.CacheReadTokens)*rates.CacheHit/m +
		float64(r.InputTokens)*rates.CacheMiss/m +
		float64(r.OutputTokens+r.ReasoningTokens)*rates.Output/m
	return cost, true
}

func isDeepSeekEndpoint(baseURL string) bool {
	return baseURL == "" || strings.Contains(strings.ToLower(baseURL), "api.deepseek.com")
}
