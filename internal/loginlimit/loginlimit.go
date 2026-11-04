// Package loginlimit counts sign-in failures and stores bans in a file so they
// survive restarts and can be inspected or edited by an administrator.
package loginlimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
)

// Kind distinguishes a banned IP address from a banned account.
type Kind string

const (
	KindIP      Kind = "ip"
	KindAccount Kind = "account"
)

// ReasonAddedByAdmin is the reason recorded for bans added from the admin panel.
const ReasonAddedByAdmin = "added by administrator"

// ReasonTooManyFailures is the reason recorded for automatic bans.
const ReasonTooManyFailures = "too many failed sign-in attempts"

// maxLoginLength bounds the account value written to the ban file.
const maxLoginLength = 254

// Ban is one banned IP address or account.
type Ban struct {
	Kind      Kind
	Value     string
	Until     time.Time
	Created   time.Time
	LastLogin string
	Reason    string
}

// Status describes the state of the ban file.
type Status struct {
	Path         string
	Exists       bool
	LastLoadedAt time.Time
	LastError    string
	SkippedCount int
	ExpiredCount int
}

// Remaining returns how long the ban is still in effect at the given time.
func (b Ban) Remaining(now time.Time) time.Duration {
	if !b.Until.After(now) {
		return 0
	}
	return b.Until.Sub(now)
}

// RetryAfterSeconds returns the Retry-After value in whole seconds, at least 1.
func (b Ban) RetryAfterSeconds(now time.Time) int {
	seconds := int(b.Remaining(now).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// RetryAfterMinutes returns the remaining time rounded up to whole minutes.
func (b Ban) RetryAfterMinutes(now time.Time) int {
	return (b.RetryAfterSeconds(now) + 59) / 60
}

// Store persists ban records.
type Store interface {
	// Banned returns the active ban for the given kind and value.
	Banned(ctx context.Context, kind Kind, value string) (Ban, bool, error)
	// Ban adds or replaces a ban record.
	Ban(ctx context.Context, b Ban) error
	// Unban removes the ban record for the given kind and value.
	Unban(ctx context.Context, kind Kind, value string) error
	// TouchIPLastLogin records the last login attempted from a banned IP.
	TouchIPLastLogin(ctx context.Context, ip, login string) error
	// List returns all active bans.
	List(ctx context.Context) ([]Ban, error)
	// Reload re-reads the ban file from disk.
	Reload(ctx context.Context) error
	// Status returns the last known file state.
	Status() Status
}

// Counters tracks failure counts within a window.
type Counters interface {
	// Incr increments the counter for key and returns the new value.
	Incr(ctx context.Context, key string, window time.Duration) (int, error)
	// Delete removes a counter.
	Delete(ctx context.Context, key string)
}

// Options configures a Limiter.
type Options struct {
	EnableIP        bool
	IPAttempts      int
	Window          time.Duration
	IPBlockDuration time.Duration
	EnableLockout   bool
	LockoutAttempts int
	LockoutDuration time.Duration
	BanFile         string
}

// Limiter combines failure counting with file-backed bans. All methods are safe
// to call on a nil receiver, which keeps callers working before Init runs.
type Limiter struct {
	store    Store
	counters Counters
	opts     Options
	now      func() time.Time
}

var defaultLimiter *Limiter

// Init creates the process-wide limiter. It is called once at startup.
func Init(opts Options) error {
	store, err := NewFileStore(opts.BanFile, time.Now)
	if err != nil {
		return errors.Wrap(err, "open ban file")
	}
	defaultLimiter = New(store, NewMemoryCounters(time.Now), opts, time.Now)
	return nil
}

// Default returns the process-wide limiter, or nil when Init has not run.
func Default() *Limiter {
	return defaultLimiter
}

// New creates a Limiter with explicit dependencies.
func New(store Store, counters Counters, opts Options, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{store: store, counters: counters, opts: opts, now: now}
}

// Banned reports whether the given client IP or login is banned. It checks the
// ban file regardless of whether the corresponding automatic limit is enabled,
// so bans created earlier or added by hand still apply.
func (l *Limiter) Banned(ctx context.Context, ip, login string) (Ban, bool, error) {
	if l == nil || l.store == nil {
		return Ban{}, false, nil
	}

	if value := NormalizeIP(ip); value != "" {
		ban, ok, err := l.store.Banned(ctx, KindIP, value)
		if err != nil {
			return Ban{}, false, err
		}
		if ok {
			// Keep the last attempted login fresh for the admin page. A failure
			// here must not turn a banned request into an allowed one.
			_ = l.store.TouchIPLastLogin(ctx, value, accountValue(login))
			return ban, true, nil
		}
	}

	if value := accountValue(login); value != "" {
		ban, ok, err := l.store.Banned(ctx, KindAccount, value)
		if err != nil {
			return Ban{}, false, err
		}
		if ok {
			return ban, true, nil
		}
	}

	return Ban{}, false, nil
}

// RecordFailure increments the IP and account failure counters and writes a ban
// when a threshold is reached. It returns the ban that was just created.
func (l *Limiter) RecordFailure(ctx context.Context, ip, login string) (*Ban, bool, error) {
	if l == nil || l.store == nil {
		return nil, false, nil
	}

	now := l.now()
	var hit *Ban

	ipValue := NormalizeIP(ip)
	if l.opts.EnableIP && l.opts.IPAttempts > 0 && ipValue != "" {
		count, err := l.counters.Incr(ctx, ipCounterKey(ipValue), l.opts.Window)
		if err != nil {
			return nil, false, err
		}
		if count >= l.opts.IPAttempts {
			ban := Ban{
				Kind:      KindIP,
				Value:     ipValue,
				Until:     now.Add(l.opts.IPBlockDuration),
				Created:   now,
				LastLogin: accountValue(login),
				Reason:    ReasonTooManyFailures,
			}
			if err := l.store.Ban(ctx, ban); err != nil {
				return nil, false, err
			}
			l.counters.Delete(ctx, ipCounterKey(ipValue))
			hit = &ban
		}
	}

	loginValue := accountValue(login)
	if l.opts.EnableLockout && l.opts.LockoutAttempts > 0 && loginValue != "" {
		count, err := l.counters.Incr(ctx, accountCounterKey(loginValue), l.opts.Window)
		if err != nil {
			return hit, hit != nil, err
		}
		if count >= l.opts.LockoutAttempts {
			ban := Ban{
				Kind:    KindAccount,
				Value:   loginValue,
				Until:   now.Add(l.opts.LockoutDuration),
				Created: now,
				Reason:  ReasonTooManyFailures,
			}
			if err := l.store.Ban(ctx, ban); err != nil {
				return hit, hit != nil, err
			}
			l.counters.Delete(ctx, accountCounterKey(loginValue))
			if hit == nil {
				hit = &ban
			}
		}
	}

	return hit, hit != nil, nil
}

// ResetAccount clears the failure counter for the given login.
func (l *Limiter) ResetAccount(ctx context.Context, login string) {
	if l == nil || l.counters == nil {
		return
	}
	if value := accountValue(login); value != "" {
		l.counters.Delete(ctx, accountCounterKey(value))
	}
}

// Ban adds or replaces a ban record.
func (l *Limiter) Ban(ctx context.Context, ban Ban) error {
	if l == nil || l.store == nil {
		return errors.New("login limiter is not initialized")
	}
	return l.store.Ban(ctx, ban)
}

// ListBans returns all active bans.
func (l *Limiter) ListBans(ctx context.Context) ([]Ban, error) {
	if l == nil || l.store == nil {
		return nil, nil
	}
	return l.store.List(ctx)
}

// Unban removes a ban record immediately.
func (l *Limiter) Unban(ctx context.Context, kind Kind, value string) error {
	if l == nil || l.store == nil {
		return errors.New("login limiter is not initialized")
	}
	return l.store.Unban(ctx, kind, value)
}

// ReloadBans re-reads the ban file from disk.
func (l *Limiter) ReloadBans(ctx context.Context) error {
	if l == nil || l.store == nil {
		return errors.New("login limiter is not initialized")
	}
	return l.store.Reload(ctx)
}

// BanStatus returns the last known ban file state.
func (l *Limiter) BanStatus() Status {
	if l == nil || l.store == nil {
		return Status{}
	}
	return l.store.Status()
}

// NormalizeIP returns the canonical key and ban file value for an IP address.
// IPv4 addresses are returned as-is. IPv6 addresses are masked to their /64
// prefix so address rotation cannot bypass a ban. It returns "" when raw is not
// an IP address.
func NormalizeIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

func accountValue(login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	return truncate(login, maxLoginLength)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) > n {
		runes = runes[:n]
	}
	return string(runes)
}

func ipCounterKey(ip string) string {
	return "ip::fail::" + ip
}

func accountCounterKey(login string) string {
	sum := sha256.Sum256([]byte(login))
	return "account::fail::" + hex.EncodeToString(sum[:])
}

type counterEntry struct {
	count     int
	expiresAt time.Time
}

// memoryCounters keeps failure counts in the process memory. Counts are lost on
// restart, which is acceptable because bans live in the ban file.
type memoryCounters struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]counterEntry
	ops     int
}

// NewMemoryCounters returns a Counters implementation backed by process memory.
func NewMemoryCounters(now func() time.Time) Counters {
	if now == nil {
		now = time.Now
	}
	return &memoryCounters{
		now:     now,
		entries: make(map[string]counterEntry),
	}
}

func (c *memoryCounters) Incr(_ context.Context, key string, window time.Duration) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if window <= 0 {
		window = 5 * time.Minute
	}
	now := c.now()
	entry := c.entries[key]
	if !entry.expiresAt.After(now) {
		entry = counterEntry{}
	}
	entry.count++
	entry.expiresAt = now.Add(window)
	c.entries[key] = entry

	c.ops++
	if c.ops%1024 == 0 && len(c.entries) > 1024 {
		for k, e := range c.entries {
			if !e.expiresAt.After(now) {
				delete(c.entries, k)
			}
		}
	}
	return entry.count, nil
}

func (c *memoryCounters) Delete(_ context.Context, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
