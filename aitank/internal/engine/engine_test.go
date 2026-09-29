package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

type fake struct {
	next func() (*model.Reading, error)
}

var fk = &fake{}

func (*fake) ID() string   { return "fake" }
func (*fake) Name() string { return "Fake" }
func (*fake) Capabilities() provider.Capabilities {
	return provider.Capabilities{Family: provider.FamilyClaude}
}
func (*fake) Detect(*provider.Env) []provider.Detection { return nil }
func (f *fake) Read(context.Context, *provider.Env, *config.Account) (*model.Reading, error) {
	return f.next()
}

func init() { provider.Register(fk) }

func setup(t *testing.T) (*config.Config, *provider.Env, *time.Time) {
	t.Setenv("AITANK_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "fake-1", Provider: "fake", Nickname: "one"}}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	env := &provider.Env{Now: func() time.Time { return now }}
	return cfg, env, &now
}

func good(used float64) func() (*model.Reading, error) {
	return func() (*model.Reading, error) {
		return &model.Reading{Plan: "Max", Windows: []model.Window{{Kind: model.FiveHour, Name: "5-hour", UsedPct: model.F(used)}}}, nil
	}
}

func TestFailedReadKeepsLastGoodAndBacksOff(t *testing.T) {
	cfg, env, now := setup(t)
	fk.next = good(20)
	if _, err := Refresh(context.Background(), cfg, env, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts[0].Plan != "Max" {
		t.Fatal("plan learned from reading")
	}
	fk.next = func() (*model.Reading, error) { return nil, provider.Errf(model.StatusAuthNeeded, "expired") }
	*now = now.Add(time.Minute)
	outs, _ := Refresh(context.Background(), cfg, env, RefreshOptions{})
	if outs[0].Status != model.StatusAuthNeeded {
		t.Fatalf("%+v", outs)
	}
	e := state.LoadCache().Accounts["fake-1"]
	if e.LastGood == nil || *e.LastGood.Windows[0].UsedPct != 20 || e.LastError == "" || e.Failures != 1 {
		t.Fatalf("last good must be kept: %+v", e)
	}
	if !e.NextAttempt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("backoff %v", e.NextAttempt.Sub(*now))
	}
	// Within backoff: skipped unless forced.
	outs, _ = Refresh(context.Background(), cfg, env, RefreshOptions{})
	if outs[0].Skipped == "" {
		t.Fatal("should back off")
	}
	fk.next = good(30)
	outs, _ = Refresh(context.Background(), cfg, env, RefreshOptions{Force: true})
	if outs[0].Status != model.StatusOK {
		t.Fatal("force should read")
	}
	v := BuildView(cfg, *now)
	if v.Rows[0].Status != model.StatusOK || *v.Rows[0].Left != 70 || v.Rec.Overall == nil {
		t.Fatalf("view: %+v", v.Rows[0])
	}
}

func TestRateLimitIsRespectedEvenWhenForced(t *testing.T) {
	cfg, env, _ := setup(t)
	fk.next = func() (*model.Reading, error) {
		return nil, &provider.Error{Status: model.StatusRateLimited, Err: errors.New("429"), RetryAfter: 30 * time.Minute}
	}
	Refresh(context.Background(), cfg, env, RefreshOptions{})
	fk.next = good(10)
	outs, _ := Refresh(context.Background(), cfg, env, RefreshOptions{Force: true})
	if outs[0].Skipped == "" {
		t.Fatal("a vendor Retry-After must be respected")
	}
}

func TestStaleExcludedFromRecommendation(t *testing.T) {
	cfg, env, now := setup(t)
	fk.next = good(10)
	Refresh(context.Background(), cfg, env, RefreshOptions{})
	v := BuildView(cfg, now.Add(22*time.Minute))
	if v.Rows[0].Status != model.StatusStale || v.Rec.Overall != nil {
		t.Fatalf("stale data must not be recommended: %+v", v.Rec)
	}
}

func TestBackoffCaps(t *testing.T) {
	if Backoff(5*time.Minute, 0) != 0 || Backoff(5*time.Minute, 3) != 20*time.Minute || Backoff(5*time.Minute, 10) != time.Hour {
		t.Fatal("backoff")
	}
}

func TestAutoResume(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "a", Paused: true, Options: map[string]string{"paused_until": "2026-09-29T10:00:00Z"}}}
	if !AutoResume(cfg, time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC)) || cfg.Accounts[0].Paused {
		t.Fatal("pause should end once the window refilled")
	}
}
