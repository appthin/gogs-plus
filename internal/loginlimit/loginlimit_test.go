package loginlimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	bans    map[banKey]Ban
	touched []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{bans: make(map[banKey]Ban)}
}

func (s *fakeStore) Banned(_ context.Context, kind Kind, value string) (Ban, bool, error) {
	ban, ok := s.bans[banKey{kind, value}]
	return ban, ok, nil
}

func (s *fakeStore) Ban(_ context.Context, ban Ban) error {
	s.bans[banKey{ban.Kind, ban.Value}] = ban
	return nil
}

func (s *fakeStore) Unban(_ context.Context, kind Kind, value string) error {
	delete(s.bans, banKey{kind, value})
	return nil
}

func (s *fakeStore) TouchIPLastLogin(_ context.Context, ip, _ string) error {
	s.touched = append(s.touched, ip)
	return nil
}

func (s *fakeStore) List(context.Context) ([]Ban, error) { return nil, nil }
func (s *fakeStore) Reload(context.Context) error        { return nil }
func (s *fakeStore) Status() Status                      { return Status{} }

func TestLimiterIPBan(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newFakeStore()
	limiter := New(store, NewMemoryCounters(clock), Options{
		EnableIP:        true,
		IPAttempts:      3,
		Window:          5 * time.Minute,
		IPBlockDuration: 15 * time.Minute,
		EnableLockout:   true,
		LockoutAttempts: 10,
		LockoutDuration: time.Hour,
	}, clock)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		ban, blocked, err := limiter.RecordFailure(ctx, "203.0.113.5", "Alice")
		require.NoError(t, err)
		assert.False(t, blocked)
		assert.Nil(t, ban)
	}

	ban, blocked, err := limiter.RecordFailure(ctx, "203.0.113.5", "Alice")
	require.NoError(t, err)
	require.True(t, blocked)
	require.NotNil(t, ban)
	assert.Equal(t, KindIP, ban.Kind)
	assert.Equal(t, "203.0.113.5", ban.Value)
	assert.Equal(t, "alice", ban.LastLogin)
	assert.Equal(t, ReasonTooManyFailures, ban.Reason)
	assert.Equal(t, now.Add(15*time.Minute), ban.Until)

	// The IP counter is cleared on ban, so the next failure starts a new window.
	_, blocked, err = limiter.RecordFailure(ctx, "203.0.113.5", "Alice")
	require.NoError(t, err)
	assert.False(t, blocked)
}

func TestLimiterAccountLockout(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newFakeStore()
	limiter := New(store, NewMemoryCounters(clock), Options{
		EnableIP:        false,
		EnableLockout:   true,
		LockoutAttempts: 2,
		LockoutDuration: 30 * time.Minute,
	}, clock)

	ctx := context.Background()
	_, blocked, err := limiter.RecordFailure(ctx, "198.51.100.9", "Alice")
	require.NoError(t, err)
	assert.False(t, blocked)

	ban, blocked, err := limiter.RecordFailure(ctx, "198.51.100.9", "Alice")
	require.NoError(t, err)
	require.True(t, blocked)
	require.NotNil(t, ban)
	assert.Equal(t, KindAccount, ban.Kind)
	assert.Equal(t, "alice", ban.Value)
	assert.Equal(t, now.Add(30*time.Minute), ban.Until)
	assert.Empty(t, ban.LastLogin)
}

func TestLimiterDisabledLayers(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newFakeStore()
	limiter := New(store, NewMemoryCounters(clock), Options{
		EnableIP:        false,
		IPAttempts:      1,
		EnableLockout:   false,
		LockoutAttempts: 1,
	}, clock)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		ban, blocked, err := limiter.RecordFailure(ctx, "203.0.113.5", "alice")
		require.NoError(t, err)
		assert.False(t, blocked)
		assert.Nil(t, ban)
	}
	assert.Empty(t, store.bans)
}

func TestLimiterResetAccount(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newFakeStore()
	limiter := New(store, NewMemoryCounters(clock), Options{
		EnableLockout:   true,
		LockoutAttempts: 2,
		LockoutDuration: time.Hour,
	}, clock)

	ctx := context.Background()
	_, blocked, err := limiter.RecordFailure(ctx, "", "alice")
	require.NoError(t, err)
	assert.False(t, blocked)

	limiter.ResetAccount(ctx, "ALICE")

	_, blocked, err = limiter.RecordFailure(ctx, "", "alice")
	require.NoError(t, err)
	assert.False(t, blocked, "reset must start a new failure window")
}

func TestLimiterBanned(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newFakeStore()
	store.bans[banKey{KindIP, "203.0.113.5"}] = Ban{Kind: KindIP, Value: "203.0.113.5", Until: now.Add(time.Hour)}
	store.bans[banKey{KindIP, "2001:db8::/64"}] = Ban{Kind: KindIP, Value: "2001:db8::/64", Until: now.Add(time.Hour)}
	store.bans[banKey{KindAccount, "alice"}] = Ban{Kind: KindAccount, Value: "alice", Until: now.Add(time.Hour)}

	limiter := New(store, NewMemoryCounters(clock), Options{}, clock)
	ctx := context.Background()

	ban, ok, err := limiter.Banned(ctx, "203.0.113.5", "bob")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, KindIP, ban.Kind)
	assert.Equal(t, []string{"203.0.113.5"}, store.touched)

	_, ok, err = limiter.Banned(ctx, "2001:db8::1234", "")
	require.NoError(t, err)
	assert.True(t, ok, "IPv6 bans apply to the whole /64 prefix")

	ban, ok, err = limiter.Banned(ctx, "192.0.2.1", "ALICE")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, KindAccount, ban.Kind)

	_, ok, err = limiter.Banned(ctx, "192.0.2.1", "bob")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestLimiterNilSafe(t *testing.T) {
	ctx := context.Background()
	var limiter *Limiter

	_, ok, err := limiter.Banned(ctx, "203.0.113.5", "alice")
	require.NoError(t, err)
	assert.False(t, ok)

	ban, blocked, err := limiter.RecordFailure(ctx, "203.0.113.5", "alice")
	require.NoError(t, err)
	assert.False(t, blocked)
	assert.Nil(t, ban)

	limiter.ResetAccount(ctx, "alice")

	bans, err := limiter.ListBans(ctx)
	require.NoError(t, err)
	assert.Nil(t, bans)
	assert.Equal(t, Status{}, limiter.BanStatus())
}

func TestNormalizeIP(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: ""},
		{raw: "not-an-ip", want: ""},
		{raw: "203.0.113.5", want: "203.0.113.5"},
		{raw: " 203.0.113.5 ", want: "203.0.113.5"},
		{raw: "::ffff:203.0.113.5", want: "203.0.113.5"},
		{raw: "2001:db8::1", want: "2001:db8::/64"},
		{raw: "2001:db8::ffff", want: "2001:db8::/64"},
		{raw: "fe80::1%eth0", want: "fe80::/64"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			assert.Equal(t, test.want, NormalizeIP(test.raw))
		})
	}
}

func TestBanRetryAfter(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	ban := Ban{Until: now.Add(90 * time.Second)}
	assert.Equal(t, 90, ban.RetryAfterSeconds(now))
	assert.Equal(t, 2, ban.RetryAfterMinutes(now))

	expired := Ban{Until: now.Add(-time.Minute)}
	assert.Equal(t, 1, expired.RetryAfterSeconds(now))
	assert.Equal(t, 1, expired.RetryAfterMinutes(now))
}
