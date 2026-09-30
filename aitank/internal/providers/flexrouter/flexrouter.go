// Package flexrouter reads status from the local flexrouter daemon,
// showing available free-tier models and their health.
package flexrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

func init() { provider.Register(P{}) }

// P is the flexrouter provider.
type P struct{}

func (P) ID() string   { return "flexrouter" }
func (P) Name() string { return "Flexrouter (free tiers)" }
func (P) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Family:    provider.FamilyOther,
		Auth:      []provider.AuthMethod{provider.AuthLocal},
		Windows:   false,
		Balance:   true,
		Launch:    true,
		Profiles:  false,
		FilesRead: []string{"~/.config/flexrouter/state/*.json"},
		Endpoints: []string{"http://localhost:4891/api/status"},
		Commands:  []string{"flexrouter --version", "curl localhost:4891/api/status"},
	}
}

func (P) LaunchSpec(a *config.Account) (string, string, string) {
	return "flexrouter", "", ""
}

// stateDir returns the flexrouter state directory.
func stateDir() string {
	if d := os.Getenv("FLEXROUTER_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "flexrouter", "state")
}

// Detect checks if flexrouter is configured.
func (P) Detect(env *provider.Env) []provider.Detection {
	// Check if state dir exists
	dir := stateDir()
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	return []provider.Detection{{
		Provider: "flexrouter",
		Identity: "local",
		Plan:     "Free Tier Pool",
		Source:   dir,
	}}
}

// apiStatus is the structure from /api/status endpoint.
type apiStatus struct {
	TotalCost    float64                    `json:"total_cost_usd"`
	SessionStart string                     `json:"session_start"`
	Providers    map[string]json.RawMessage `json:"providers"`
}

// apiStats is the structure from /api/stats endpoint.
type apiStats struct {
	Totals struct {
		Requests int `json:"requests"`
		ThisHour int `json:"this_hour"`
		PeakRPM  int `json:"peak_rpm"`
	} `json:"totals"`
}

// modelsResponse is the structure from /models endpoint.
type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	ID          string          `json:"id"`
	Flexrouter  *flexrouterInfo `json:"flexrouter"`
}

type flexrouterInfo struct {
	Kind          string   `json:"kind"`
	Models        []string `json:"models"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Buckets       []string `json:"buckets"`
	Score         int      `json:"score"`
	ContextWindow int      `json:"context_window"`
	Status        *struct {
		Value string `json:"value"`
	} `json:"status"`
}

func (P) Read(ctx context.Context, env *provider.Env, acct *config.Account) (*model.Reading, error) {
	r := &model.Reading{
		Provider:  "flexrouter",
		Plan:      "Free Tier Pool",
		Source:    "API",
		FetchedAt: env.Now(),
	}

	client := &http.Client{Timeout: 3 * time.Second}

	// Check if daemon is running
	resp, err := client.Get("http://localhost:4891/api/status")
	if err != nil {
		r.Notes = append(r.Notes, "Daemon not running — start with `flexrouter serve`")
		return r, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		r.Notes = append(r.Notes, "Daemon error — check dashboard")
		return r, nil
	}

	var status apiStatus
	_ = json.NewDecoder(resp.Body).Decode(&status)

	// Get stats
	var stats apiStats
	if statsResp, err := client.Get("http://localhost:4891/api/stats"); err == nil {
		defer statsResp.Body.Close()
		_ = json.NewDecoder(statsResp.Body).Decode(&stats)
	}

	// Get models for detailed info
	var models modelsResponse
	if modelsResp, err := client.Get("http://localhost:4891/models"); err == nil {
		defer modelsResp.Body.Close()
		_ = json.NewDecoder(modelsResp.Body).Decode(&models)
	}

	// Parse models: find top model, count buckets, count status
	var topModel string
	var topScore int
	bucketCounts := map[string]int{}
	readyCount, busyCount := 0, 0

	for _, m := range models.Data {
		if m.Flexrouter == nil {
			continue
		}
		fr := m.Flexrouter
		if fr.Kind == "bucket" {
			bucketCounts[m.ID] = len(fr.Models)
		} else if fr.Kind == "model" {
			// Track top model by score
			if fr.Score > topScore {
				topScore = fr.Score
				topModel = m.ID
			}
			// Track status
			if fr.Status != nil {
				if fr.Status.Value == "ready" {
					readyCount++
				} else {
					busyCount++
				}
			}
		}
	}

	// Build notes
	r.Notes = append(r.Notes, "Daemon running on :4891")

	// Show top model
	if topModel != "" {
		r.Notes = append(r.Notes, fmt.Sprintf("Top: %s (score %d)", topModel, topScore))
	}

	// Show bucket counts
	if len(bucketCounts) > 0 {
		var bucketInfo string
		for bucket, count := range bucketCounts {
			if count > 0 {
				if bucketInfo != "" {
					bucketInfo += ", "
				}
				bucketInfo += fmt.Sprintf("%s:%d", bucket, count)
			}
		}
		if bucketInfo != "" {
			r.Notes = append(r.Notes, "Buckets: "+bucketInfo)
		}
	}

	// Show model status
	totalModels := readyCount + busyCount
	if totalModels > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d ready, %d busy", readyCount, busyCount))
	}

	// Show request stats
	if stats.Totals.Requests > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d requests (%d/hr)", stats.Totals.Requests, stats.Totals.ThisHour))
	}

	// Show cost if any
	if status.TotalCost > 0 {
		cost := status.TotalCost
		r.Balances = append(r.Balances, model.Money{
			Amount:   &cost,
			Currency: "USD",
			Label:    "Session cost",
		})
	}

	// Show top models as windows (by score, ready first)
	type modelInfo struct {
		id    string
		score int
		ready bool
	}
	var modelList []modelInfo
	for _, m := range models.Data {
		if m.Flexrouter != nil && m.Flexrouter.Kind == "model" {
			ready := m.Flexrouter.Status != nil && m.Flexrouter.Status.Value == "ready"
			modelList = append(modelList, modelInfo{m.ID, m.Flexrouter.Score, ready})
		}
	}
	// Sort by score descending
	for i := 0; i < len(modelList); i++ {
		for j := i + 1; j < len(modelList); j++ {
			if modelList[j].score > modelList[i].score {
				modelList[i], modelList[j] = modelList[j], modelList[i]
			}
		}
	}
	// Show top 5
	for i, m := range modelList {
		if i >= 5 {
			break
		}
		pct := 0.0
		if !m.ready {
			pct = 100.0 // busy = 100% used
		}
		r.Windows = append(r.Windows, model.Window{
			Kind:    model.ModelWeekly,
			Name:    m.id,
			Model:   m.id,
			UsedPct: &pct,
			Unit:    "%",
		})
	}

	return r, nil
}
