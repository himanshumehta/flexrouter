package recommend

import (
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/model"
)

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func cand(id, fam string, st model.Status, used float64, resetIn time.Duration, pace float64) Candidate {
	return Candidate{AccountID: id, Label: fam + "/" + id, Family: fam, Status: st, PctPerHour: pace,
		Reading: &model.Reading{Windows: []model.Window{{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(used), ResetsAt: model.T(now.Add(resetIn))}}}}
}

func TestExclusions(t *testing.T) {
	cs := []Candidate{
		cand("a", "claude", model.StatusPaused, 0, time.Hour, 0),
		cand("b", "claude", model.StatusStale, 0, time.Hour, 0),
		cand("c", "claude", model.StatusAuthNeeded, 0, time.Hour, 0),
		cand("d", "claude", model.StatusOK, 100, time.Hour, 0),
		cand("e", "claude", model.StatusOK, 50, 3*time.Hour, 0),
	}
	res := Recommend(cs, now, Options{})
	if res.Overall == nil || res.Overall.AccountID != "e" {
		t.Fatalf("want e, got %+v", res.Overall)
	}
	if len(res.Excluded) != 4 {
		t.Fatalf("want 4 excluded, got %+v", res.Excluded)
	}
}

func TestPerFamilyAndOverall(t *testing.T) {
	cs := []Candidate{
		cand("c1", "claude", model.StatusOK, 60, 4*time.Hour, 0),
		cand("x1", "codex", model.StatusOK, 10, 4*time.Hour, 0),
	}
	res := Recommend(cs, now, Options{})
	if res.Overall.AccountID != "x1" || res.ByFamily["claude"].AccountID != "c1" || res.ByFamily["codex"].AccountID != "x1" {
		t.Fatalf("%+v", res)
	}
}

func TestCloseAccountsPreferLongerLasting(t *testing.T) {
	// a has slightly more left but burns fast; b lasts longer at its pace.
	cs := []Candidate{
		cand("a", "claude", model.StatusOK, 40, 4*time.Hour, 60),
		cand("b", "claude", model.StatusOK, 42, 4*time.Hour, 5),
	}
	res := Recommend(cs, now, Options{CloseMargin: 5})
	if res.Overall.AccountID != "b" {
		t.Fatalf("want b (lasts longer), got %s: %s", res.Overall.AccountID, res.Overall.Reason)
	}
}

func TestSoonResetGetsBonus(t *testing.T) {
	cs := []Candidate{
		cand("late", "claude", model.StatusOK, 30, 6*24*time.Hour, 0),
		cand("soon", "claude", model.StatusOK, 36, 30*time.Minute, 0),
	}
	res := Recommend(cs, now, Options{CloseMargin: 1})
	if res.Overall.AccountID != "soon" {
		t.Fatalf("capacity that resets soon should be used first, got %s", res.Overall.AccountID)
	}
}

func TestModelLimitToggle(t *testing.T) {
	c := cand("a", "claude", model.StatusOK, 10, time.Hour, 0)
	c.Reading.Windows = append(c.Reading.Windows, model.Window{Kind: model.ModelWeekly, Model: "Opus", UsedPct: model.F(100)})
	if res := Recommend([]Candidate{c}, now, Options{}); res.Overall != nil {
		t.Fatal("per-model limit should count by default")
	}
	if res := Recommend([]Candidate{c}, now, Options{IgnoreModelLimits: true}); res.Overall == nil {
		t.Fatal("toggle should ignore per-model limit")
	}
}
