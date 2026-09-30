package render

import (
	"encoding/json"
	"io"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/forecast"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/recommend"
)

// Envelope wraps every --json document (FR-9.5). Fields are only ever added
// within a schema version.
type Envelope struct {
	Schema      string    `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`
	Kind        string    `json:"kind"`
	Data        any       `json:"data"`
}

// WriteJSON writes one enveloped document.
func WriteJSON(w io.Writer, kind string, data any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Envelope{Schema: model.SchemaVersion, GeneratedAt: time.Now().UTC(), Kind: kind, Data: data})
}

// AccountJSON is one account in `list --json`. It never contains secrets.
type AccountJSON struct {
	ID         string              `json:"id"`
	Provider   string              `json:"provider"`
	Nickname   string              `json:"nickname"`
	Plan       *string             `json:"plan"`
	Identity   *string             `json:"identity"`
	Status     model.Status        `json:"status"`
	Paused     bool                `json:"paused"`
	LeftPct    *float64            `json:"left_pct"`
	Binding    *string             `json:"binding_window"`
	Windows    []model.Window      `json:"windows"`
	Balances   []model.Money       `json:"balances"`
	Spend      *model.Money        `json:"spend"`
	FetchedAt  *time.Time          `json:"fetched_at"`
	AgeSeconds *float64            `json:"age_seconds"`
	Stale      bool                `json:"stale"`
	Source     *string             `json:"source"`
	Error      *string             `json:"error"`
	Forecasts  []forecast.Forecast `json:"forecasts"`
	UseNext    bool                `json:"use_next"`
	Active     bool                `json:"active"`
}

// ListJSON is `aitank list --json` and `aitank --json`.
type ListJSON struct {
	RefreshedAt *time.Time       `json:"refreshed_at"`
	Accounts    []AccountJSON    `json:"accounts"`
	Recommend   recommend.Result `json:"recommendation"`
}

// ViewJSON converts a view to its JSON form.
func ViewJSON(v *engine.View) ListJSON {
	out := ListJSON{Recommend: v.Rec, Accounts: []AccountJSON{}}
	if !v.RefreshedAt.IsZero() {
		t := v.RefreshedAt
		out.RefreshedAt = &t
	}
	if out.Recommend.Excluded == nil {
		out.Recommend.Excluded = []recommend.Excluded{}
	}
	for _, r := range v.Rows {
		a := AccountJSON{
			ID: r.Account.ID, Provider: r.Account.Provider, Nickname: r.Account.Nickname,
			Status: r.Status, Paused: r.Account.Paused, LeftPct: r.Left, Stale: r.Stale,
			Windows: []model.Window{}, Balances: []model.Money{}, Forecasts: []forecast.Forecast{},
			UseNext: r.Next, Active: r.Active,
		}
		if r.Account.Identity != "" {
			a.Identity = strp(r.Account.Identity)
		}
		if r.Account.Plan != "" {
			a.Plan = strp(r.Account.Plan)
		}
		if r.Error != "" && r.Status != model.StatusOK {
			a.Error = strp(r.Error)
		}
		if rd := r.Reading; rd != nil {
			if rd.Plan != "" {
				a.Plan = strp(rd.Plan)
			}
			a.Windows = append(a.Windows, rd.Windows...)
			a.Balances = append(a.Balances, rd.Balances...)
			a.Spend = rd.Spend
			t := rd.FetchedAt
			a.FetchedAt = &t
			age := r.Age.Seconds()
			a.AgeSeconds = &age
			a.Source = strp(rd.Source)
			if w, _, ok := rd.Binding(v.Config.Recommend.IgnoreModelLimits); ok {
				a.Binding = strp(w.Key())
			}
			a.Forecasts = append(a.Forecasts, r.Forecasts...)
		}
		out.Accounts = append(out.Accounts, a)
	}
	return out
}

func strp(s string) *string { return &s }
