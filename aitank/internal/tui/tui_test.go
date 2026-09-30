package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/recommend"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
)

func TestFrameFitsAndHighlights(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	v := &engine.View{Now: now, Config: cfg, RefreshedAt: now.Add(-12 * time.Second)}
	for i := 0; i < 8; i++ {
		a := &config.Account{ID: "claude-" + string(rune('1'+i)), Provider: "claude", Nickname: "n" + string(rune('a'+i))}
		r := &model.Reading{Plan: "Max", FetchedAt: now, Windows: []model.Window{
			{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(float64(10 * i)), ResetsAt: model.T(now.Add(time.Hour))},
			{Kind: model.Weekly, Name: "Weekly", UsedPct: model.F(50)},
		}}
		v.Rows = append(v.Rows, &engine.Row{Account: a, Status: model.StatusOK, Reading: r, Left: r.Left(false), Next: i == 0})
	}
	v.Rec.Overall = &recommend.Pick{AccountID: "claude-1", Label: "claude na", Left: 50}
	for _, size := range [][2]int{{80, 24}, {40, 10}, {200, 60}} {
		f := Frame(v, 7, size[0], size[1], "", render.Style{On: true})
		lines := strings.Split(f, "\r\n")
		if len(lines) != size[1] {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, l := range lines {
			l = strings.TrimSuffix(strings.TrimPrefix(l, "\x1b[H"), "\x1b[K")
			if render.Width(l) > size[0] {
				t.Fatalf("%v: line too wide (%d): %q", size, render.Width(l), l)
			}
		}
		if !strings.Contains(f, "claude nh") {
			t.Fatalf("%v: selected row scrolled out of view", size)
		}
		if !strings.Contains(f, "Refreshed 12s ago") && size[0] >= 80 {
			t.Fatalf("%v: refresh age missing", size)
		}
	}
}
