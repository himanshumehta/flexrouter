// Package bills tracks manually entered subscriptions (FR-14).
package bills

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
)

// Cycles are the supported billing cycles.
var Cycles = []string{"weekly", "monthly", "quarterly", "yearly"}

// ValidCycle reports whether c is supported.
func ValidCycle(c string) bool {
	for _, x := range Cycles {
		if x == c {
			return true
		}
	}
	return false
}

// addMonths keeps the day of month, clamping to the month's last day
// (a 31 Jan renewal becomes 28/29 Feb, then 31 Mar).
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), 0, 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, t.Hour(), t.Minute(), 0, 0, t.Location())
}

// NextRenewal rolls the stored renewal date forward past now.
func NextRenewal(b config.Bill, now time.Time) time.Time {
	anchor := b.Renewal
	t := anchor
	for i := 1; t.Before(startOfDay(now)) && i < 10000; i++ {
		// Step from the anchor each time so day clamping does not drift.
		switch b.Cycle {
		case "weekly":
			t = anchor.AddDate(0, 0, 7*i)
		case "quarterly":
			t = addMonths(anchor, 3*i)
		case "yearly":
			t = addMonths(anchor, 12*i)
		default:
			t = addMonths(anchor, i)
		}
	}
	return t
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Monthly is a bill's cost per month.
func Monthly(b config.Bill) float64 {
	switch b.Cycle {
	case "weekly":
		return b.Price * 52 / 12
	case "quarterly":
		return b.Price / 3
	case "yearly":
		return b.Price / 12
	default:
		return b.Price
	}
}

// Totals is the monthly total per currency.
func Totals(bs []config.Bill) map[string]float64 {
	out := map[string]float64{}
	for _, b := range bs {
		out[strings.ToUpper(b.Currency)] += Monthly(b)
	}
	return out
}

// TotalsText formats totals as "$42.00/mo + €9.99/mo".
func TotalsText(bs []config.Bill) string {
	t := Totals(bs)
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%.2f %s/mo", t[k], k))
	}
	if len(parts) == 0 {
		return "0/mo"
	}
	return strings.Join(parts, " + ")
}

// Due returns bills renewing within days of now.
func Due(bs []config.Bill, now time.Time, days int) []config.Bill {
	var out []config.Bill
	limit := startOfDay(now).AddDate(0, 0, days+1)
	for _, b := range bs {
		if NextRenewal(b, now).Before(limit) {
			out = append(out, b)
		}
	}
	return out
}

// ParseDate accepts YYYY-MM-DD in local time.
func ParseDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("want a date like 2026-10-15")
	}
	return t, nil
}
