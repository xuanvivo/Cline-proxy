package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	pool      *AccountPool
	poolMu    sync.Mutex
	poolPath  string
)

func init() {
	exe, _ := os.Executable()
	poolPath = filepath.Join(filepath.Dir(exe), ".cline-accounts.json")
}

func loadPool() *AccountPool {
	poolMu.Lock()
	defer poolMu.Unlock()

	if pool != nil {
		return pool
	}

	data, err := os.ReadFile(poolPath)
	if err != nil {
		if !os.IsNotExist(err) {
			// Carrying on with an empty pool would overwrite the file on the next save.
			log.Fatalf("Cannot read accounts file %s: %v%s", poolPath, err, storageHint(poolPath))
		}
		pool = &AccountPool{Accounts: []*Account{}, Keys: []string{}}
		return pool
	}

	var p AccountPool
	// An empty file (e.g. created with touch before the first start) is a fresh pool.
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &p); err != nil {
			log.Fatalf("Accounts file %s is not valid JSON (%v); refusing to start so it isn't overwritten. Fix it, or move it away to start with an empty pool.", poolPath, err)
		}
	}

	if p.Accounts == nil {
		p.Accounts = []*Account{}
	}
	if p.Keys == nil {
		p.Keys = []string{}
	}
	pool = &p

	// Restore persisted load-balancing config (survives restarts).
	if p.Config != nil {
		cfg := defaultProxyConfig()
		if p.Config.Strategy != "" {
			cfg.Strategy = p.Config.Strategy
		}
		if p.Config.Headers != nil {
			cfg.Headers = p.Config.Headers
		}
		cfg.DefaultModel = p.Config.DefaultModel
		setProxyConfig(cfg)
	}

	return pool
}

var (
	saveMu      sync.Mutex // serializes pool writes; taken at exit so none is cut off
	inPlaceSave bool       // set once replacing poolPath via rename has failed
)

// savePool writes the pool to disk, replacing the file atomically when
// possible so a crash can't leave it truncated. A Docker single-file bind
// mount can't be replaced by rename (EBUSY), so it is then written in place.
func savePool() error {
	saveMu.Lock()
	defer saveMu.Unlock()
	data, err := json.MarshalIndent(pool, "", "  ")
	if err == nil {
		err = writePoolFile(data)
	}
	if err != nil {
		log.Printf("Failed to save accounts: %v", err)
	}
	return err
}

func writePoolFile(data []byte) error {
	if !inPlaceSave {
		tmp, err := os.CreateTemp(filepath.Dir(poolPath), ".cline-accounts-*.tmp")
		if err == nil {
			_, err = tmp.Write(data)
			if err == nil {
				err = tmp.Sync()
			}
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Rename(tmp.Name(), poolPath)
			}
			if err == nil {
				return nil
			}
			os.Remove(tmp.Name())
		}
		log.Printf("Atomic save unavailable for %s (%v), writing in place", poolPath, err)
		inPlaceSave = true
	}
	return os.WriteFile(poolPath, data, 0600)
}

// checkStorage fails fast when the accounts file can't be written, instead of
// serving with accounts that silently live only in memory until a restart.
func checkStorage() {
	for _, f := range []string{sessionsFile, overrideFile} {
		if hint := storageHint(f); hint != "" {
			log.Printf("WARNING: %s is unusable:%s", f, hint)
		}
	}
	if err := savePool(); err != nil {
		log.Fatalf("Cannot write accounts file %s: %v%s", poolPath, err, storageHint(poolPath))
	}
}

// storageHint explains the usual cause when a data file path is a directory:
// Docker bind-mounting a file that doesn't exist on the host yet creates an
// empty directory in its place.
func storageHint(path string) string {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return ""
	}
	return "\n  " + path + " is a directory, not a file. Docker creates one when a bind-mounted file doesn't exist on the host yet." +
		"\n  Fix it in the compose directory on the host:" +
		"\n    docker compose down" +
		"\n    for f in .cline-accounts.json .admin-sessions.json override.md; do [ -d \"$f\" ] && rmdir \"$f\"; touch \"$f\"; done" +
		"\n    docker compose up -d"
}

// exitOnSignal lets an in-flight pool write finish before exiting on
// SIGTERM/SIGINT (docker stop, Ctrl+C), so shutdown never truncates the file.
func exitOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-ch
		saveMu.Lock()
		os.Exit(0)
	}()
}

func addAccount(acc *Account) {
	p := loadPool()
	poolMu.Lock()
	p.Accounts = append(p.Accounts, acc)
	poolMu.Unlock()
	savePool()
}

func removeAccount(accountID string) bool {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	for i, a := range p.Accounts {
		if a.AccountID == accountID {
			p.Accounts = append(p.Accounts[:i], p.Accounts[i+1:]...)
			savePool()
			return true
		}
	}
	return false
}

func getAccountByID(accountID string) *Account {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	for _, a := range p.Accounts {
		if a.AccountID == accountID {
			return a
		}
	}
	return nil
}

func refreshAccountToken(acc *Account) error {
	resp, err := refreshClineToken(acc.RefreshToken)
	if err != nil {
		acc.Status = "expired"
		savePool()
		return fmt.Errorf("token refresh failed: %w", err)
	}

	acc.AccessToken = "workos:" + resp.Data.AccessToken
	if resp.Data.RefreshToken != "" {
		acc.RefreshToken = resp.Data.RefreshToken
	}
	acc.ExpiresAt = parseExpiry(resp.Data.ExpiresAt) - 60000
	acc.Status = "active"
	savePool()
	return nil
}

func pickAccount() *Account {
	if accs := pickAccounts(); len(accs) > 0 {
		return accs[0]
	}
	return nil
}

// cooldownDuration is how long a rate-limited/failed account stays out of
// rotation before pickAccounts automatically brings it back as active.
const cooldownDuration = 5 * time.Minute

// pickAccounts returns the active accounts in the order they should be tried
// for the current request, according to the configured strategy. Cooldown
// accounts whose cooldown has elapsed are automatically restored to active.
// The returned slice is the failover order: callers try index 0, then 1, ...
func pickAccounts() []*Account {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	now := time.Now()
	active := make([]*Account, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		// Auto-recover elapsed cooldowns.
		if a.Status == "cooldown" && !a.CooldownUntil.IsZero() && now.After(a.CooldownUntil) {
			a.Status = "active"
			a.CooldownUntil = time.Time{}
			log.Printf("  account %s cooldown elapsed, back to active", truncateEmail(a.Email))
		}
		if a.Status == "active" {
			active = append(active, a)
		}
	}

	if len(active) == 0 {
		savePool()
		return nil
	}

	cfg := getProxyConfig()
	ordered := make([]*Account, 0, len(active))

	switch cfg.Strategy {
	case "fill":
		// Keep natural order: always drain active[0] first.
		ordered = append(ordered, active...)
	case "random":
		// Shuffle so the primary pick is random; the rest form the failover tail.
		perm := make([]*Account, len(active))
		copy(perm, active)
		for i := len(perm) - 1; i > 0; i-- {
			j := int(now.UnixNano()>>uint(i%16)) % (i + 1)
			if j < 0 {
				j = -j % (i + 1)
			}
			perm[i], perm[j] = perm[j], perm[i]
		}
		ordered = append(ordered, perm...)
	default: // round_robin
		if p.CurrentIdx >= len(active) {
			p.CurrentIdx = 0
		}
		start := p.CurrentIdx
		ordered = append(ordered, active[start:]...)
		ordered = append(ordered, active[:start]...)
		p.CurrentIdx = (start + 1) % len(active)
	}

	savePool()
	return ordered
}

// markCooldown puts an account out of rotation until cooldownDuration elapses.
func markCooldown(acc *Account) {
	poolMu.Lock()
	defer poolMu.Unlock()
	acc.Status = "cooldown"
	acc.CooldownUntil = time.Now().Add(cooldownDuration)
	savePool()
}

// markExpired marks an account's token as permanently failed (manual reset needed).
func markExpired(acc *Account) {
	poolMu.Lock()
	defer poolMu.Unlock()
	acc.Status = "expired"
	savePool()
}

// markUsed records a successful upstream call.
func markUsed(acc *Account) {
	poolMu.Lock()
	defer poolMu.Unlock()
	acc.LastUsed = time.Now()
	acc.UsageCount++
	savePool()
}

// saveConfigToPool persists the current load-balancing config so it survives restarts.
func saveConfigToPool() {
	p := loadPool()
	cfg := getProxyConfig()
	poolMu.Lock()
	defer poolMu.Unlock()
	p.Config = cfg
	savePool()
}

func ensureAccountToken(acc *Account) (string, error) {
	if acc.AccessToken != "" && time.Now().UnixMilli() < acc.ExpiresAt {
		return acc.AccessToken, nil
	}

	if err := refreshAccountToken(acc); err != nil {
		return "", err
	}

	return acc.AccessToken, nil
}

func listAccounts() []*Account {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	result := make([]*Account, len(p.Accounts))
	for i, a := range p.Accounts {
		// Don't expose tokens
		result[i] = &Account{
			AccountID:  a.AccountID,
			Email:      a.Email,
			Status:     a.Status,
			LastUsed:   a.LastUsed,
			UsageCount: a.UsageCount,
			CreatedAt:  a.CreatedAt,
		}
	}
	return result
}

func addAccountFromDeviceAuth() (*Account, error) {
	fmt.Println("\n=== Add New Cline Account (OAuth) ===\n")

	device, err := workosDeviceAuth()
	if err != nil {
		return nil, err
	}

	authURL := device.VerificationURIComplete
	if authURL == "" {
		authURL = device.VerificationURI
	}

	fmt.Println("  1. Open this URL in your browser:")
	fmt.Println("     " + authURL)
	fmt.Println("  2. Enter code: " + device.UserCode)
	fmt.Println("  3. Log in with Google, GitHub, or email\n")

	_ = openBrowser(authURL)
	fmt.Println("  Waiting for authorization...")

	interval := device.Interval
	if interval < 5 {
		interval = 5
	}
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 300
	}

	workosTok, err := pollWorkosToken(device.DeviceCode, interval, expiresIn)
	if err != nil {
		return nil, err
	}

	fmt.Println("  WorkOS authorized. Registering with Cline...")

	cline, err := registerWithCline(workosTok.AccessToken, workosTok.RefreshToken)
	if err != nil {
		return nil, err
	}

	if cline.Data.RefreshToken == "" {
		return nil, fmt.Errorf("cline registration missing refresh token")
	}

	email := "unknown"
	if cline.Data.UserInfo != nil && cline.Data.UserInfo.Email != "" {
		email = cline.Data.UserInfo.Email
	}

	acc := &Account{
		AccountID:    fmt.Sprintf("acc_%d", time.Now().UnixMilli()),
		Email:        email,
		RefreshToken: cline.Data.RefreshToken,
		AccessToken:  "workos:" + cline.Data.AccessToken,
		ExpiresAt:    parseExpiry(cline.Data.ExpiresAt) - 60000,
		Status:       "active",
		CreatedAt:    time.Now(),
	}

	addAccount(acc)
	fmt.Printf("  Account added! Email: %s\n", email)
	return acc, nil
}
