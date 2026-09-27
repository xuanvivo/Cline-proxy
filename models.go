package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Free model list, synced from the feed the official Cline client uses for
// its "Free" picker section. Only the feed's free group is taken: the
// OpenRouter catalog (/models) also lists ":free" models, but those still
// deduct account credits when called through Cline.
const (
	freeModelsURL     = clineAPIBase + "/ai/cline/recommended-models"
	modelSyncInterval = time.Hour
	modelSyncRetry    = 5 * time.Minute
)

type freeModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

// builtinFreeModels seeds the list until the first successful sync
// (the feed's cline-free/ models as of 2026-09).
var builtinFreeModels = []freeModel{
	{ID: "cline-free/mimo-v2.6-flash", Name: "Mimo V2.6 Flash", Description: "Mixture-of-Experts architecture with 309B total parameters"},
	{ID: "cline-free/deepseek-v4.1-flash", Name: "Deepseek-v4.1-Flash", Description: "Fast and efficient with 1M context window"},
	{ID: "cline-free/gemini-3.8-flash", Name: "Gemini 3.8 Flash", Description: "Google's most intelligent Flash model"},
	{ID: "cline-free/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", Description: "Meta's multimodal reasoning model for experimentation, learning, and early-stage agentic, multi-agent, and coding workflows."},
}

var (
	freeModelsMu       sync.RWMutex
	freeModels         = builtinFreeModels
	freeModelsSyncedAt time.Time // zero until the first successful sync
	freeModelsLastErr  string
)

// listFreeModels returns the current free model list (never empty). Sync
// replaces the slice wholesale, so callers may keep reading it after the
// lock is released but must not modify it.
func listFreeModels() []freeModel {
	freeModelsMu.RLock()
	defer freeModelsMu.RUnlock()
	return freeModels
}

// syncFreeModels fetches the free group from upstream and replaces the list.
// On failure the previous list is kept.
func syncFreeModels() error {
	models, err := fetchFreeModels()
	freeModelsMu.Lock()
	defer freeModelsMu.Unlock()
	if err != nil {
		freeModelsLastErr = err.Error()
		return err
	}
	if modelIDs(models) != modelIDs(freeModels) {
		log.Printf("Free models synced: %s", modelIDs(models))
	}
	freeModels = models
	freeModelsSyncedAt = time.Now()
	freeModelsLastErr = ""
	return nil
}

func fetchFreeModels() ([]freeModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", freeModelsURL, nil)
	if err != nil {
		return nil, err
	}
	// Same client identity as chat requests; the feed is public, so no token.
	for k, v := range getProxyConfig().Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch free models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch free models: HTTP %d %s", resp.StatusCode, truncate(readBody(resp), 200))
	}

	var feed struct {
		Free []freeModel `json:"free"`
		// Some Cline responses wrap the payload in {data: {...}}
		Data *struct {
			Free []freeModel `json:"free"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("decode free models: %w", err)
	}
	raw := feed.Free
	if len(raw) == 0 && feed.Data != nil {
		raw = feed.Data.Free
	}

	models := make([]freeModel, 0, len(raw))
	seen := map[string]bool{}
	for _, m := range raw {
		m.ID = strings.TrimSpace(m.ID)
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			m.Name = m.ID
		}
		m.Description = strings.TrimSpace(m.Description)
		models = append(models, m)
	}
	// An empty group is far more likely a feed glitch than Cline dropping
	// every free model, so keep the current list instead.
	if len(models) == 0 {
		return nil, fmt.Errorf("upstream returned no free models")
	}
	return models, nil
}

func modelIDs(models []freeModel) string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return strings.Join(ids, ", ")
}

// startFreeModelSync syncs now, then hourly in the background, retrying
// sooner after a failure.
func startFreeModelSync() {
	go func() {
		for {
			wait := modelSyncInterval
			if err := syncFreeModels(); err != nil {
				log.Printf("Free model sync failed, retry in %v: %v", modelSyncRetry, err)
				wait = modelSyncRetry
			}
			time.Sleep(wait)
		}
	}()
}

// currentDefaultModel is the model used when a request doesn't name one: the
// admin-configured value, else the first cline-free/ model of the free list
// (stealth/ previews come and go too quickly to be a sane default).
func currentDefaultModel() string {
	if m := getProxyConfig().DefaultModel; m != "" {
		return m
	}
	models := listFreeModels()
	for _, m := range models {
		if strings.HasPrefix(m.ID, "cline-free/") {
			return m.ID
		}
	}
	return models[0].ID
}

// freeModelsStatus is the admin panel's view of the free model list.
func freeModelsStatus() map[string]any {
	// Resolve before taking the lock: currentDefaultModel read-locks too,
	// and a recursive RLock can deadlock behind a waiting sync.
	defaultModel := currentDefaultModel()
	freeModelsMu.RLock()
	defer freeModelsMu.RUnlock()
	var syncedAt any // null until the first successful sync
	if !freeModelsSyncedAt.IsZero() {
		syncedAt = freeModelsSyncedAt
	}
	return map[string]any{
		"models":       freeModels,
		"syncedAt":     syncedAt,
		"lastError":    freeModelsLastErr,
		"defaultModel": defaultModel,
	}
}
