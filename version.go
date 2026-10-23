package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed VERSION
var versionFile string

var (
	appVersion  = strings.TrimSpace(versionFile)
	buildCommit = "development"
	buildTime   = "unknown"
)

const (
	defaultClientVersion = "3.0.73"
	defaultCoreVersion   = "0.0.95"
	versionCheckInterval = 6 * time.Hour
	versionCheckCache    = 5 * time.Minute
)

type releaseInfo struct {
	Version     string `json:"version"`
	URL         string `json:"url"`
	PublishedAt string `json:"publishedAt,omitempty"`
	CoreVersion string `json:"coreVersion,omitempty"`
}

type releaseCheck struct {
	Latest    *releaseInfo `json:"latest"`
	CheckedAt *time.Time   `json:"checkedAt"`
	LastError string       `json:"lastError"`
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

// This client never carries account tokens, admin credentials or custom proxy
// headers. Release checks only read public metadata and never install code.
type versionChecker struct {
	checkMu     sync.Mutex
	mu          sync.Mutex
	client      *http.Client
	apiBase     string
	rawBase     string
	proxy       releaseCheck
	cli         releaseCheck
	lastAttempt time.Time
}

var versions = &versionChecker{
	client:  &http.Client{Timeout: 12 * time.Second},
	apiBase: "https://api.github.com",
	rawBase: "https://raw.githubusercontent.com",
}

// stableVersion accepts only a complete stable release, excluding desktop,
// SDK and preview tags before a candidate is compared numerically.
func stableVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return v, false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return v, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func newerVersion(latest, current string) bool {
	a, okA := stableVersion(latest)
	b, okB := stableVersion(current)
	if !okA || !okB {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func (c *versionChecker) getJSON(ctx context.Context, url string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Cline-proxy/"+appVersion)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("GitHub rate limit or access denied (HTTP %d); will retry later", resp.StatusCode)
		}
		return fmt.Errorf("release metadata: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dest)
}

func (c *versionChecker) fetchProxy(ctx context.Context) (*releaseInfo, error) {
	var release githubRelease
	if err := c.getJSON(ctx, c.apiBase+"/repos/xuanvivo/Cline-proxy/releases/latest", &release); err != nil {
		return nil, err
	}
	if _, ok := stableVersion(release.TagName); !ok || release.Draft || release.Prerelease {
		return nil, fmt.Errorf("no stable proxy release found")
	}
	return &releaseInfo{Version: strings.TrimPrefix(release.TagName, "v"), URL: "https://github.com/xuanvivo/Cline-proxy/releases/tag/" + release.TagName, PublishedAt: release.PublishedAt}, nil
}

func (c *versionChecker) fetchCLI(ctx context.Context) (*releaseInfo, error) {
	var releases []githubRelease
	if err := c.getJSON(ctx, c.apiBase+"/repos/cline/cline/releases?per_page=100", &releases); err != nil {
		return nil, err
	}
	var latest *releaseInfo
	var tag string
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "cli-v") {
			continue
		}
		version := strings.TrimPrefix(release.TagName, "cli-v")
		if _, ok := stableVersion(version); !ok {
			continue
		}
		if latest == nil || newerVersion(version, latest.Version) {
			tag = release.TagName
			latest = &releaseInfo{Version: version, URL: "https://github.com/cline/cline/releases/tag/" + tag, PublishedAt: release.PublishedAt}
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("no stable Cline CLI release found")
	}
	// Read the core version from the SAME release tag, not the moving main branch.
	var pkg struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, c.rawBase+"/cline/cline/"+tag+"/sdk/packages/core/package.json", &pkg); err != nil {
		return nil, fmt.Errorf("CLI core version: %w", err)
	}
	if _, ok := stableVersion(pkg.Version); !ok {
		return nil, fmt.Errorf("invalid CLI core version")
	}
	latest.CoreVersion = pkg.Version
	return latest, nil
}

func updateReleaseCheck(check *releaseCheck, latest *releaseInfo, err error) {
	now := time.Now().UTC()
	check.CheckedAt = &now
	if err != nil {
		check.LastError = err.Error()
		return
	}
	check.Latest = latest
	check.LastError = ""
}

func (c *versionChecker) check(ctx context.Context) {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	c.mu.Lock()
	// Coalesce manual/automatic requests and bound public GitHub API usage.
	if time.Since(c.lastAttempt) < versionCheckCache {
		c.mu.Unlock()
		return
	}
	c.lastAttempt = time.Now()
	c.mu.Unlock()
	proxy, err := c.fetchProxy(ctx)
	c.mu.Lock()
	updateReleaseCheck(&c.proxy, proxy, err)
	c.mu.Unlock()
	cli, err := c.fetchCLI(ctx)
	c.mu.Lock()
	updateReleaseCheck(&c.cli, cli, err)
	c.mu.Unlock()
}

func versionChecksEnabled() bool {
	return !strings.EqualFold(envOr("VERSION_CHECK_ENABLED", "true"), "false")
}

func startVersionChecks() {
	if !versionChecksEnabled() {
		return
	}
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			versions.check(ctx)
			cancel()
			log.Printf("Version check completed; next check in %v", versionCheckInterval)
			time.Sleep(versionCheckInterval)
		}
	}()
}

func (c *versionChecker) status() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	clientVersion := ""
	for k, v := range getProxyConfig().Headers {
		if strings.EqualFold(k, "X-CLIENT-VERSION") {
			clientVersion = v
		}
	}
	view := func(check releaseCheck, current string) map[string]any {
		return map[string]any{
			"current": current, "latest": check.Latest,
			"checkedAt": check.CheckedAt, "lastError": check.LastError,
			"updateAvailable": check.Latest != nil && newerVersion(check.Latest.Version, current),
		}
	}
	return map[string]any{
		"enabled": versionChecksEnabled(), "intervalSeconds": int(versionCheckInterval.Seconds()),
		"cacheSeconds": int(versionCheckCache.Seconds()),
		"buildCommit":  buildCommit, "buildTime": buildTime,
		"proxy": view(c.proxy, appVersion), "client": view(c.cli, clientVersion),
	}
}

func handleAdminVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: versions.status()})
}

func handleAdminCheckVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	versions.check(ctx)
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: versions.status()})
}

// Applying detected CLI metadata is explicit. Custom headers survive checks;
// proxy releases are only reported and are never executed or installed.
func handleAdminApplyClientVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	versions.mu.Lock()
	latest := versions.cli.Latest
	versions.mu.Unlock()
	if latest == nil {
		writeAPI(w, http.StatusConflict, apiResponse{Error: "请先成功检测 Cline CLI 版本"})
		return
	}
	old := getProxyConfig()
	for name, value := range old.Headers {
		if strings.EqualFold(name, "X-CLIENT-VERSION") && newerVersion(value, latest.Version) {
			writeAPI(w, http.StatusConflict, apiResponse{Error: "当前 CLI 版本高于检测结果，请重新检测后再应用"})
			return
		}
	}
	headers := make(map[string]string, len(old.Headers))
	identity := clientIdentityHeaders(latest.Version, latest.CoreVersion)
	for k, v := range old.Headers {
		replaced := false
		for name := range identity {
			if strings.EqualFold(k, name) {
				replaced = true
				break
			}
		}
		if !replaced {
			headers[k] = v
		}
	}
	for k, v := range identity {
		headers[k] = v
	}
	setProxyConfig(&proxyConfigData{Strategy: old.Strategy, Headers: headers, DefaultModel: old.DefaultModel})
	if err := saveConfigToPool(); err != nil {
		setProxyConfig(old)
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: "保存客户端版本失败: " + err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: versions.status()})
}

func clientIdentityHeaders(cliVersion, coreVersion string) map[string]string {
	return map[string]string{
		"User-Agent": "Cline/" + cliVersion, "X-CLIENT-TYPE": "cline-cli",
		"X-CLIENT-VERSION": cliVersion, "X-PLATFORM": "cli",
		"X-PLATFORM-VERSION": cliVersion, "X-CORE-VERSION": coreVersion,
	}
}

// Upgrade only unchanged historical defaults. Deliberately customized or
// deleted headers remain as saved by the administrator.
func migrateLegacyHeaders(headers map[string]string) map[string]string {
	legacy := map[string]string{
		"User-Agent": "Cline/3.0.47", "X-CLIENT-VERSION": "3.0.47",
		"X-PLATFORM": "terminal", "X-PLATFORM-VERSION": "3.0.47", "X-CORE-VERSION": "0.0.66",
	}
	defaults := clientIdentityHeaders(defaultClientVersion, defaultCoreVersion)
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		for name, previous := range legacy {
			if strings.EqualFold(k, name) && v == previous {
				v = defaults[name]
				break
			}
		}
		result[k] = v
	}
	return result
}
