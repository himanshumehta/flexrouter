package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
)

func TestCountdown(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2*time.Hour + 54*time.Minute: "2h 54m", 6*24*time.Hour + 9*time.Hour: "6d 9h", 5 * time.Minute: "5m",
		30 * time.Second: "<1m", 0: "now", 3 * time.Hour: "3h", 2 * 24 * time.Hour: "2d",
	} {
		if got := Countdown(d); got != want {
			t.Errorf("%v: got %q want %q", d, got, want)
		}
	}
}

func TestDashForUnknown(t *testing.T) {
	if Pct(nil) != Dash || Money(nil) != Dash || Money(&model.Money{Currency: "USD"}) != Dash || Until(nil, time.Now()) != Dash {
		t.Fatal("unknown values must render as a dash")
	}
	if Pct(model.F(0)) != "0%" {
		t.Fatal("known zero stays zero")
	}
}

func TestColorThresholds(t *testing.T) {
	st := Style{On: true}
	c := config.Default().Colors
	if !strings.Contains(st.LeftColor(c, model.F(80), "x"), green) || !strings.Contains(st.LeftColor(c, model.F(40), "x"), amber) || !strings.Contains(st.LeftColor(c, model.F(5), "x"), red) {
		t.Fatal("left colours")
	}
	if !strings.Contains(st.UsedColor(c, model.F(59), "x"), green) || !strings.Contains(st.UsedColor(c, model.F(60), "x"), amber) || !strings.Contains(st.UsedColor(c, model.F(90), "x"), red) {
		t.Fatal("bar colours")
	}
	if (Style{}).LeftColor(c, model.F(5), "x") != "x" {
		t.Fatal("colour off must not emit escapes")
	}
}

func TestNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("AITANK_FORCE_COLOR", "1")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Fatal("NO_COLOR must win")
	}
}

func view(n int) *engine.View {
	now := time.Now()
	cfg := config.Default()
	v := &engine.View{Now: now, Config: cfg}
	for i := 0; i < n; i++ {
		a := &config.Account{ID: "claude-very-long-identifier", Provider: "claude", Nickname: "a-rather-long-nickname-here"}
		r := &model.Reading{Plan: "Max 20x", FetchedAt: now, Windows: []model.Window{
			{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(45), ResetsAt: model.T(now.Add(2 * time.Hour))},
			{Kind: model.Weekly, Name: "Weekly", UsedPct: model.F(91), ResetsAt: model.T(now.Add(100 * time.Hour))},
			{Kind: model.ModelWeekly, Name: "Weekly (Opus)", Model: "Opus", UsedPct: model.F(12), ResetsAt: model.T(now.Add(100 * time.Hour))},
			{Kind: model.Monthly, Name: "Extra"},
		}}
		v.Rows = append(v.Rows, &engine.Row{Account: a, Status: model.StatusOK, Reading: r, Left: r.Left(false)})
	}
	return v
}

func TestCompactFits80Columns(t *testing.T) {
	var buf bytes.Buffer
	List(&buf, view(3), Style{On: true}, true)
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if Width(l) > 80 {
			t.Fatalf("line is %d columns: %q", Width(l), l)
		}
	}
}

func TestFullListShowsWindows(t *testing.T) {
	var buf bytes.Buffer
	List(&buf, view(1), Style{}, false)
	out := buf.String()
	for _, want := range []string{"Max 20x", "9% left", "Weekly (Opus)", "resets in 2h", "Extra", "—"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
