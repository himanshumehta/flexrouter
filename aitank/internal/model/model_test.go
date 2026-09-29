package model

import (
	"testing"
	"time"
)

func TestBindingPicksTightestWindow(t *testing.T) {
	now := time.Now()
	r := &Reading{Windows: []Window{
		{Kind: FiveHour, Name: "5-hour", UsedPct: F(30), ResetsAt: T(now.Add(time.Hour))},
		{Kind: Weekly, Name: "Weekly", UsedPct: F(70)},
		{Kind: ModelWeekly, Name: "Weekly (Opus)", Model: "Opus", UsedPct: F(95)},
		{Kind: Monthly, Name: "Unknown"},
	}}
	w, left, ok := r.Binding(false)
	if !ok || w.Model != "Opus" || left != 5 {
		t.Fatalf("got %v %v %v", w.Name, left, ok)
	}
	w, left, _ = r.Binding(true)
	if w.Kind != Weekly || left != 30 {
		t.Fatalf("ignoring model limits: got %v %v", w.Name, left)
	}
}

func TestLeftUnknownIsNil(t *testing.T) {
	r := &Reading{Windows: []Window{{Kind: Weekly}}}
	if r.Left(false) != nil {
		t.Fatal("unknown windows must give nil, never 0")
	}
	var nilR *Reading
	if nilR.Left(false) != nil {
		t.Fatal("nil reading")
	}
}

func TestPctFromUsedLimitAndPausedUsage(t *testing.T) {
	w := Window{Used: F(25), Limit: F(200)}
	if p := w.Pct(); p == nil || *p != 12.5 {
		t.Fatalf("pct = %v", p)
	}
	r := &Reading{Windows: []Window{w}, UsagePaused: true}
	if l := r.Left(false); l == nil || *l != 0 {
		t.Fatalf("paused usage should count as exhausted, got %v", l)
	}
	over := Window{UsedPct: F(140)}
	if p := over.Pct(); *p != 100 {
		t.Fatalf("clamp: %v", *p)
	}
}
