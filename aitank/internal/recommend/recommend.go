// Package recommend picks the "use next" account (FR-7).
package recommend

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/model"
)

// Candidate is one account's current state as the engine sees it.
type Candidate struct {
	AccountID string
	Label     string
	Family    string
	Status    model.Status
	Reading   *model.Reading
	// PctPerHour is the binding window's burn rate over the last hour, 0 if
	// unknown or idle.
	PctPerHour float64
}

// Pick is a recommendation.
type Pick struct {
	AccountID  string     `json:"account"`
	Label      string     `json:"label"`
	Family     string     `json:"family"`
	Left       float64    `json:"left_pct"`
	Binding    string     `json:"binding_window"`
	ResetsAt   *time.Time `json:"resets_at"`
	Score      float64    `json:"score"`
	LastsHours *float64   `json:"lasts_hours"` // at current pace; nil when idle
	Reason     string     `json:"reason"`
}

// Excluded explains why an account was left out (FR-7.5).
type Excluded struct {
	AccountID string `json:"account"`
	Label     string `json:"label"`
	Why       string `json:"why"`
}

// Options tune scoring.
type Options struct {
	IgnoreModelLimits bool    // FR-7.3
	CloseMargin       float64 // FR-7.4, in score points
}

// Score ranks one account. Higher is better.
//
//	score = left% + expiry bonus − burn penalty
//
// left% is the binding window's remaining percentage. The expiry bonus (up
// to +10) favours capacity that resets within 24 hours, because unused
// capacity in a window that is about to reset is lost anyway. The burn
// penalty (up to −20) applies when the current pace would empty the binding
// window before it resets.
func Score(c Candidate, now time.Time, opt Options) (score float64, p Pick, ok bool) {
	if c.Reading == nil {
		return 0, p, false
	}
	w, left, ok := c.Reading.Binding(opt.IgnoreModelLimits)
	if !ok {
		return 0, p, false
	}
	if c.Reading.UsagePaused {
		left = 0
	}
	score = left
	hoursToReset := math.Inf(1)
	if w.ResetsAt != nil {
		hoursToReset = math.Max(0, w.ResetsAt.Sub(now).Hours())
	}
	if hoursToReset < 24 {
		score += 10 * (1 - hoursToReset/24) * (left / 100)
	}
	var lasts *float64
	if c.PctPerHour > 0 {
		h := left / c.PctPerHour
		lasts = &h
		if h < hoursToReset {
			score -= 20 * math.Max(0, 1-h/hoursToReset)
		}
	}
	p = Pick{
		AccountID: c.AccountID, Label: c.Label, Family: c.Family, Left: left,
		Binding: w.Name, ResetsAt: w.ResetsAt, Score: math.Round(score*10) / 10, LastsHours: lasts,
	}
	return score, p, true
}

// Result is the best pick overall and per family.
type Result struct {
	Overall  *Pick            `json:"overall"`
	ByFamily map[string]*Pick `json:"by_family"`
	Excluded []Excluded       `json:"excluded"`
}

// Recommend scores every usable account.
func Recommend(cands []Candidate, now time.Time, opt Options) Result {
	if opt.CloseMargin <= 0 {
		opt.CloseMargin = 5
	}
	res := Result{ByFamily: map[string]*Pick{}}
	var picks []Pick
	for _, c := range cands {
		if why := exclusion(c, opt); why != "" {
			res.Excluded = append(res.Excluded, Excluded{c.AccountID, c.Label, why})
			continue
		}
		_, p, ok := Score(c, now, opt)
		if !ok {
			res.Excluded = append(res.Excluded, Excluded{c.AccountID, c.Label, "no usage window to score"})
			continue
		}
		picks = append(picks, p)
	}
	sort.SliceStable(picks, func(i, j int) bool { return better(picks[i], picks[j], opt.CloseMargin) })
	for i := range picks {
		p := picks[i]
		if res.Overall == nil {
			res.Overall = withReason(p, picks, opt.CloseMargin)
		}
		if res.ByFamily[p.Family] == nil {
			var fam []Pick
			for _, q := range picks {
				if q.Family == p.Family {
					fam = append(fam, q)
				}
			}
			res.ByFamily[p.Family] = withReason(p, fam, opt.CloseMargin)
		}
	}
	return res
}

func exclusion(c Candidate, opt Options) string {
	switch c.Status {
	case model.StatusOK:
	case model.StatusPaused:
		return "paused"
	case model.StatusStale:
		return "data is stale"
	default:
		return c.Status.Label()
	}
	if c.Reading == nil {
		return "no reading"
	}
	if c.Reading.UsagePaused {
		return "included usage is paused"
	}
	if l := c.Reading.Left(opt.IgnoreModelLimits); l != nil && *l <= 0 {
		return "exhausted"
	}
	return ""
}

// better orders picks: higher score, except that when two scores are within
// the close margin the one projected to last longer wins (FR-7.4).
func better(a, b Pick, margin float64) bool {
	if math.Abs(a.Score-b.Score) <= margin {
		la, lb := lasts(a), lasts(b)
		if la != lb {
			return la > lb
		}
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.AccountID < b.AccountID
}

func lasts(p Pick) float64 {
	if p.LastsHours == nil {
		return math.Inf(1)
	}
	return *p.LastsHours
}

func withReason(p Pick, all []Pick, margin float64) *Pick {
	reason := fmt.Sprintf("%.0f%% left on its tightest window (%s)", p.Left, p.Binding)
	if p.ResetsAt != nil {
		reason += fmt.Sprintf(", resets %s", p.ResetsAt.Local().Format("Mon 15:04"))
	}
	if len(all) > 1 {
		runner := all[1]
		if all[0].AccountID != p.AccountID {
			runner = all[0]
		}
		if math.Abs(p.Score-runner.Score) <= margin && lasts(p) != lasts(runner) {
			reason += fmt.Sprintf("; close to %s but projected to last longer at current pace", runner.Label)
		} else {
			reason += fmt.Sprintf("; next best is %s at %.0f%%", runner.Label, runner.Left)
		}
	}
	p.Reason = reason
	return &p
}
