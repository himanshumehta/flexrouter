package state

import (
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/model"
)

func TestRedact(t *testing.T) {
	for _, in := range []string{
		"Authorization: Bearer sk-ant-oat01-abcdefghijklmnop",
		"key=sk-or-v1-0123456789abcdef0123",
		"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.sig",
		"cookie: WorkosCursorSessionToken=user%3A%3Aabc",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
	} {
		out := Redact(in)
		for _, secret := range []string{"abcdefghijklmnop", "0123456789abcdef", "eyJzdWIi", "user%3A", "ghp_abc"} {
			if strings.Contains(out, secret) {
				t.Errorf("Redact(%q) = %q still contains %q", in, out, secret)
			}
		}
	}
	if Redact("api.anthropic.com: HTTP 500") != "api.anthropic.com: HTTP 500" {
		t.Fatal("ordinary text changed")
	}
}

func TestMask(t *testing.T) {
	if got := Mask("sk-proj-abcdefgh1234a1f3"); got != "sk-…a1f3" {
		t.Fatalf("got %q", got)
	}
	if got := Mask("short"); got != "…" {
		t.Fatalf("got %q", got)
	}
}

func TestStatusStaleAndFailures(t *testing.T) {
	now := time.Now()
	fresh := &Entry{LastGood: &model.Reading{FetchedAt: now.Add(-time.Minute)}, LastStatus: model.StatusOK}
	if fresh.Status(now, 15*time.Minute, false) != model.StatusOK {
		t.Fatal("fresh")
	}
	old := &Entry{LastGood: &model.Reading{FetchedAt: now.Add(-22 * time.Minute)}, LastStatus: model.StatusOK}
	if old.Status(now, 15*time.Minute, false) != model.StatusStale {
		t.Fatal("stale")
	}
	failedFresh := &Entry{LastGood: &model.Reading{FetchedAt: now.Add(-time.Minute)}, LastStatus: model.StatusError}
	if failedFresh.Status(now, 15*time.Minute, false) != model.StatusOK {
		t.Fatal("a failed read keeps fresh last-good values usable")
	}
	failedOld := &Entry{LastGood: &model.Reading{FetchedAt: now.Add(-time.Hour)}, LastStatus: model.StatusAuthNeeded}
	if failedOld.Status(now, 15*time.Minute, false) != model.StatusAuthNeeded {
		t.Fatal("old data + failure shows the failure")
	}
	var none *Entry
	if none.Status(now, time.Minute, false) != model.StatusNeverRead || none.Status(now, time.Minute, true) != model.StatusPaused {
		t.Fatal("nil entry")
	}
}

func TestHistoryAppendLoadPrune(t *testing.T) {
	t.Setenv("AITANK_HOME", t.TempDir())
	now := time.Now().UTC()
	for _, ago := range []time.Duration{50 * time.Hour, time.Hour, 0} {
		r := &model.Reading{AccountID: "a", FetchedAt: now.Add(-ago), Windows: []model.Window{{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(10)}, {Kind: model.Weekly}}}
		if err := AppendHistory(r); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(LoadHistory(now.Add(-100 * time.Hour))); n != 3 {
		t.Fatalf("want 3 points (unknown window skipped), got %d", n)
	}
	if err := PruneHistory(now); err != nil {
		t.Fatal(err)
	}
	if n := len(LoadHistory(now.Add(-100 * time.Hour))); n != 2 {
		t.Fatalf("prune kept %d", n)
	}
}

func TestLogRedactsErrors(t *testing.T) {
	t.Setenv("AITANK_HOME", t.TempDir())
	_ = AppendLog(LogEntry{T: time.Now(), Account: "a", Status: model.StatusError, Error: "Bearer sk-secret-value-1234567890"})
	l := LoadLog()
	if len(l) != 1 || strings.Contains(l[0].Error, "secret-value") {
		t.Fatalf("%+v", l)
	}
}
