package forecast

import (
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func series(start float64, perMin float64, mins int, reset time.Time) []state.Point {
	var pts []state.Point
	for m := mins; m >= 0; m -= 5 {
		pts = append(pts, state.Point{T: now.Add(-time.Duration(m) * time.Minute), Account: "a", Window: "five_hour:5-hour",
			UsedPct: start + perMin*float64(mins-m), ResetsAt: &reset})
	}
	return pts
}

func reading(used float64, reset time.Time) *model.Reading {
	return &model.Reading{AccountID: "a", FetchedAt: now, Windows: []model.Window{{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(used), ResetsAt: &reset}}}
}

func TestWarnsWhenPaceEmptiesWithinHour(t *testing.T) {
	reset := now.Add(3 * time.Hour)
	h := series(10, 1, 60, reset) // 1%/min, ends at 70%
	fs := Compute(reading(70, reset), h, now, Options{})
	w := Warnings(fs)
	if len(w) != 1 {
		t.Fatalf("want a warning, got %+v", fs)
	}
	if m := *w[0].EmptyInMins; m < 25 || m > 35 {
		t.Fatalf("empty in %v min, want ~30", m)
	}
	if !strings.Contains(w[0].Message(), "5h limit runs out in ~30 min") {
		t.Fatalf("message %q", w[0].Message())
	}
}

func TestNoWarningWhenIdleSlowOrResetFirst(t *testing.T) {
	reset := now.Add(3 * time.Hour)
	if w := Warnings(Compute(reading(50, reset), series(50, 0, 60, reset), now, Options{})); len(w) != 0 {
		t.Fatal("idle usage must not warn")
	}
	if w := Warnings(Compute(reading(16, reset), series(10, 0.1, 60, reset), now, Options{})); len(w) != 0 {
		t.Fatal("slow pace must not warn (would project days ahead)")
	}
	soon := now.Add(10 * time.Minute)
	if w := Warnings(Compute(reading(70, soon), series(10, 1, 60, soon), now, Options{})); len(w) != 0 {
		t.Fatal("window resets before it would empty: no warning")
	}
}

func TestIgnoresSamplesBeforeReset(t *testing.T) {
	reset := now.Add(4 * time.Hour)
	old := now.Add(-time.Hour)
	h := series(0, 1.5, 60, old) // previous cycle, steep
	h = append(h, state.Point{T: now.Add(-5 * time.Minute), Account: "a", Window: "five_hour:5-hour", UsedPct: 2, ResetsAt: &reset})
	if w := Warnings(Compute(reading(2, reset), h, now, Options{})); len(w) != 0 {
		t.Fatalf("previous cycle's pace leaked into the forecast: %+v", w)
	}
}

func TestStaleHistoryGivesNoForecast(t *testing.T) {
	reset := now.Add(3 * time.Hour)
	h := series(10, 1, 60, reset)
	r := reading(70, reset)
	later := now.Add(30 * time.Minute)
	r.FetchedAt = now
	if fs := Compute(r, h, later, Options{}); len(fs) != 0 {
		t.Fatal("forecast must disappear when readings stop")
	}
}
