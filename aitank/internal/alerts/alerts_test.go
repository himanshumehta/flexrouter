package alerts

import (
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func viewWith(used float64, reset time.Time, enabled bool) *engine.View {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "claude-1", Provider: "claude", Nickname: "me", Alerts: config.AccountAlerts{Enabled: enabled, At: 20}}}
	r := &model.Reading{FetchedAt: now, Windows: []model.Window{{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(used), ResetsAt: &reset}}}
	return &engine.View{Now: now, Config: cfg, Rows: []*engine.Row{{Account: &cfg.Accounts[0], Status: model.StatusOK, Reading: r, Left: r.Left(false)}}}
}

func kinds(ns []Notice) map[string]int {
	m := map[string]int{}
	for _, n := range ns {
		m[n.Kind]++
	}
	return m
}

func TestOffByDefault(t *testing.T) {
	st := state.LoadAlerts()
	if ns := Evaluate(viewWith(95, now.Add(time.Hour), false), st, now); len(ns) != 0 {
		t.Fatalf("alerts must be off by default: %+v", ns)
	}
}

func TestThresholdOncePerCycleThenRefill(t *testing.T) {
	t.Setenv("AITANK_HOME", t.TempDir())
	st := state.LoadAlerts()
	reset := now.Add(time.Hour)
	if k := kinds(Evaluate(viewWith(85, reset, true), st, now)); k["threshold"] != 1 {
		t.Fatalf("%v", k)
	}
	if ns := Evaluate(viewWith(88, reset, true), st, now.Add(5*time.Minute)); len(ns) != 0 {
		t.Fatalf("must not repeat within the cycle: %+v", ns)
	}
	if k := kinds(Evaluate(viewWith(100, reset, true), st, now.Add(10*time.Minute))); k["limit"] != 1 {
		t.Fatalf("limit hit: %v", k)
	}
	next := reset.Add(5 * time.Hour)
	if k := kinds(Evaluate(viewWith(0, next, true), st, reset.Add(time.Minute))); k["refilled"] != 1 {
		t.Fatalf("refill: %v", k)
	}
	if k := kinds(Evaluate(viewWith(85, next, true), st, reset.Add(3*time.Hour))); k["threshold"] != 1 {
		t.Fatalf("new cycle fires again: %v", k)
	}
}

func TestQuietMutes(t *testing.T) {
	st := state.LoadAlerts()
	v := viewWith(95, now.Add(time.Hour), true)
	v.Rows[0].Account.QuietUntil = now.Add(time.Hour)
	if ns := Evaluate(v, st, now); len(ns) != 0 {
		t.Fatal("quiet account alerted")
	}
}

func TestBillReminder(t *testing.T) {
	st := state.LoadAlerts()
	v := viewWith(0, now.Add(time.Hour), false)
	v.Config.Bills = []config.Bill{{ID: "bill-1", Name: "Max", Price: 200, Currency: "USD", Cycle: "monthly", Renewal: now.AddDate(0, 0, 2)}}
	if k := kinds(Evaluate(v, st, now)); k["bill"] != 1 {
		t.Fatalf("%v", k)
	}
	if ns := Evaluate(v, st, now.Add(time.Hour)); len(ns) != 0 {
		t.Fatal("bill reminder repeated")
	}
}
