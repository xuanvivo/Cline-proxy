package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStableVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"3.0.73", "3.0.47", true}, {"3.10.0", "3.9.99", true},
		{"v1.2.0", "1.1.99", true}, {"1.2.0", "1.2.0", false},
		{"1.1.0", "1.2.0", false}, {"1.3.0-beta.1", "1.2.0", false},
		{"desktop-v9.0.0", "1.2.0", false}, {"1.02.0", "1.1.0", false},
		{"1.3", "1.2.0", false}, {"1.3.0", "custom", false},
	} {
		if got := newerVersion(tc.latest, tc.current); got != tc.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func TestVersionCheckFiltersCachesAndRetainsResults(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for _, name := range []string{"Authorization", "Cookie", "X-Api-Key", "X-Private"} {
			if r.Header.Get(name) != "" {
				t.Errorf("release check leaked %s", name)
			}
		}
		if fail.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.RequestURI() {
		case "/repos/xuanvivo/Cline-proxy/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v1.3.0","published_at":"2026-10-23T10:00:00Z"}`)
		case "/repos/cline/cline/releases?per_page=100":
			fmt.Fprint(w, `[
				{"tag_name":"desktop-v99.0.0"}, {"tag_name":"sdk/sdk/v99.0.0"},
				{"tag_name":"cli-v4.0.0","draft":true},
				{"tag_name":"cli-v5.0.0","prerelease":true},
				{"tag_name":"cli-v6.0.0-beta.1"},
				{"tag_name":"cli-v3.9.99"}, {"tag_name":"cli-v3.10.1"}
			]`)
		case "/cline/cline/cli-v3.10.1/sdk/packages/core/package.json":
			fmt.Fprint(w, `{"version":"0.0.96"}`)
		default:
			t.Errorf("unexpected metadata URL %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &versionChecker{client: server.Client(), apiBase: server.URL, rawBase: server.URL}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.check(context.Background()); _ = c.status() }()
	}
	wg.Wait()
	if calls.Load() != 3 {
		t.Fatalf("coalesced checks made %d HTTP calls, want 3", calls.Load())
	}
	if c.cli.Latest == nil || c.cli.Latest.Version != "3.10.1" || c.cli.Latest.CoreVersion != "0.0.96" {
		t.Fatalf("wrong CLI/core release: %+v", c.cli)
	}
	if c.proxy.Latest == nil || c.proxy.Latest.Version != "1.3.0" {
		t.Fatalf("wrong proxy release: %+v", c.proxy)
	}
	fail.Store(true)
	c.lastAttempt = time.Now().Add(-versionCheckCache)
	c.check(context.Background())
	if c.cli.Latest.Version != "3.10.1" || c.proxy.Latest.Version != "1.3.0" || c.cli.LastError == "" || c.proxy.LastError == "" {
		t.Fatalf("rate limit must retain previous results and report failure: %+v %+v", c.proxy, c.cli)
	}
}

func TestVersionStatusRemainsResponsiveDuringCheck(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	c := &versionChecker{client: server.Client(), apiBase: server.URL, rawBase: server.URL}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.check(ctx); close(done) }()
	<-started
	statusDone := make(chan struct{})
	go func() { _ = c.status(); close(statusDone) }()
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		t.Error("status blocked behind upstream network request")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("version check ignored cancellation")
	}
	if c.proxy.LastError == "" || c.cli.LastError == "" {
		t.Fatal("cancellation was not reported")
	}
}

func TestCLIRejectsMissingOrInvalidReleaseMetadata(t *testing.T) {
	for _, tc := range []struct{ name, releases, core string }{
		{"desktop only", `[{"tag_name":"desktop-v0.0.48"}]`, `{"version":"0.0.95"}`},
		{"invalid JSON", `{`, `{}`},
		{"invalid core", `[{"tag_name":"cli-v3.0.73"}]`, `{"version":"preview"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/releases") {
					fmt.Fprint(w, tc.releases)
				} else {
					fmt.Fprint(w, tc.core)
				}
			}))
			defer server.Close()
			c := &versionChecker{client: server.Client(), apiBase: server.URL, rawBase: server.URL}
			if info, err := c.fetchCLI(context.Background()); err == nil || info != nil {
				t.Fatalf("invalid metadata accepted: %+v %v", info, err)
			}
		})
	}
}

func TestLegacyHeaderMigrationPreservesCustomizations(t *testing.T) {
	old := map[string]string{"user-agent": "Cline/3.0.47", "X-CLIENT-VERSION": "3.0.47", "X-CORE-VERSION": "custom", "X-Private": "keep"}
	got := migrateLegacyHeaders(old)
	if got["user-agent"] != "Cline/"+defaultClientVersion || got["X-CLIENT-VERSION"] != defaultClientVersion || got["X-CORE-VERSION"] != "custom" || got["X-Private"] != "keep" {
		t.Fatalf("incorrect migration: %v", got)
	}
	if _, ok := got["X-PLATFORM"]; ok {
		t.Fatal("deleted header was recreated")
	}
	if old["user-agent"] != "Cline/3.0.47" {
		t.Fatal("migration mutated live config")
	}
}

func TestVersionAdminAuthAndApplyPersistence(t *testing.T) {
	oldVersions, oldPool, oldPath, oldConfig, oldInPlace := versions, pool, poolPath, getProxyConfig(), inPlaceSave
	t.Cleanup(func() {
		versions, pool, poolPath, inPlaceSave = oldVersions, oldPool, oldPath, oldInPlace
		setProxyConfig(oldConfig)
	})
	versions = &versionChecker{cli: releaseCheck{Latest: &releaseInfo{Version: "3.0.74", CoreVersion: "0.0.96"}}}
	pool = &AccountPool{Accounts: []*Account{{AccountID: "test-account", RefreshToken: "test-refresh", Status: "active"}}, Keys: []string{"existing-test-key"}}
	poolPath = filepath.Join(t.TempDir(), ".cline-accounts.json")
	inPlaceSave = false
	setProxyConfig(&proxyConfigData{Strategy: "fill", DefaultModel: "custom-model", Headers: map[string]string{"user-agent": "custom", "X-Private": "keep"}})
	before := getProxyConfig()
	mux := newProxyMux()
	for _, route := range []string{"/version", "/version/check", "/version/apply-client"} {
		req := httptest.NewRequest(http.MethodPost, "/"+adminBasePath+"/api"+route, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", route, rr.Code)
		}
	}
	request := func(method, route string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/"+adminBasePath+"/api"+route, nil)
		req.SetBasicAuth(adminUser, adminPass)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	if rr := request(http.MethodGet, "/version/apply-client"); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET mutated identity: %d", rr.Code)
	}
	if rr := request(http.MethodPost, "/version/apply-client"); rr.Code != http.StatusOK {
		t.Fatalf("apply failed: %s", rr.Body)
	}
	data, err := os.ReadFile(poolPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted AccountPool
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Accounts) != 1 || persisted.Accounts[0].RefreshToken != "test-refresh" || len(persisted.Keys) != 1 || persisted.Keys[0] != "existing-test-key" {
		t.Fatal("applying version changed account/key data")
	}
	cfg := persisted.Config
	if cfg.Headers["User-Agent"] != "Cline/3.0.74" || cfg.Headers["X-CORE-VERSION"] != "0.0.96" || cfg.Headers["X-Private"] != "keep" || cfg.Strategy != "fill" || cfg.DefaultModel != "custom-model" {
		t.Fatalf("incorrect persisted identity/config: %+v", cfg)
	}
	if _, exists := cfg.Headers["user-agent"]; exists {
		t.Fatal("duplicate case-insensitive identity header")
	}
	if before.Headers["user-agent"] != "custom" {
		t.Fatal("apply mutated config in place")
	}
	versions.cli.Latest = &releaseInfo{Version: "3.0.73", CoreVersion: "0.0.95"}
	if rr := request(http.MethodPost, "/version/apply-client"); rr.Code != http.StatusConflict {
		t.Fatalf("stale result downgraded CLI identity: %d", rr.Code)
	}
	versions.cli.Latest = &releaseInfo{Version: "3.0.74", CoreVersion: "0.0.96"}
	poolPath = filepath.Join(t.TempDir(), "missing-dir", "pool.json")
	before = getProxyConfig()
	if rr := request(http.MethodPost, "/version/apply-client"); rr.Code != http.StatusInternalServerError {
		t.Fatalf("save failure was hidden: %d", rr.Code)
	}
	if getProxyConfig() != before || pool.Config != before {
		t.Fatal("failed persistence did not restore configuration")
	}
}

func TestHealthCountsLiveAccounts(t *testing.T) {
	oldPool := pool
	t.Cleanup(func() { pool = oldPool })
	pool = &AccountPool{Accounts: []*Account{{Status: "active"}}}
	mux := newProxyMux()
	check := func(want int) {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
		var result struct {
			Active  int    `json:"activeAccounts"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Active != want || result.Version != appVersion {
			t.Fatalf("stale health: %s", rr.Body)
		}
	}
	check(1)
	pool.Accounts = append(pool.Accounts, &Account{Status: "active"})
	check(2)
	pool.Accounts[0].Status = "expired"
	check(1)
}
