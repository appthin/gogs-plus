package loginlimit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, now func() time.Time) (*FileStore, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "login_bans.json")
	store, err := NewFileStore(path, now)
	require.NoError(t, err)
	return store, path
}

func TestFileStoreBanAndUnban(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store, path := newTestStore(t, func() time.Time { return now })
	ctx := context.Background()

	ipBan := Ban{
		Kind:      KindIP,
		Value:     "203.0.113.5",
		Until:     now.Add(15 * time.Minute),
		Created:   now,
		LastLogin: "alice",
		Reason:    ReasonTooManyFailures,
	}
	require.NoError(t, store.Ban(ctx, ipBan))
	require.NoError(t, store.Ban(ctx, Ban{
		Kind:    KindAccount,
		Value:   "alice",
		Until:   now.Add(15 * time.Minute),
		Created: now,
		Reason:  ReasonTooManyFailures,
	}))

	ban, ok, err := store.Banned(ctx, KindIP, "203.0.113.5")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "alice", ban.LastLogin)

	bans, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, bans, 2)
	assert.Equal(t, KindAccount, bans[0].Kind)
	assert.Equal(t, KindIP, bans[1].Kind)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"kind": "ip"`)
	assert.Contains(t, string(data), `"lastLogin": "alice"`)

	require.NoError(t, store.Unban(ctx, KindIP, "203.0.113.5"))
	_, ok, err = store.Banned(ctx, KindIP, "203.0.113.5")
	require.NoError(t, err)
	assert.False(t, ok)

	bans, err = store.List(ctx)
	require.NoError(t, err)
	require.Len(t, bans, 1)
}

func TestFileStoreExternalEdit(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store, path := newTestStore(t, func() time.Time { return now })
	ctx := context.Background()

	require.NoError(t, store.Ban(ctx, Ban{Kind: KindAccount, Value: "alice", Until: now.Add(time.Hour), Created: now}))

	// Removing the entry from the file must lift the ban without a restart.
	require.NoError(t, os.WriteFile(path, []byte("{\"bans\": []}\n"), 0o600))
	_, ok, err := store.Banned(ctx, KindAccount, "alice")
	require.NoError(t, err)
	assert.False(t, ok)

	// Adding an entry by hand must ban immediately.
	require.NoError(t, os.WriteFile(path, []byte(`{"bans":[{"kind":"account","value":"Bob","until":`+int64String(now.Add(time.Hour).Unix())+`}]}`), 0o600))
	ban, ok, err := store.Banned(ctx, KindAccount, "bob")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "bob", ban.Value)
}

func TestFileStoreMissingFile(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store, path := newTestStore(t, func() time.Time { return now })
	ctx := context.Background()

	require.NoError(t, store.Ban(ctx, Ban{Kind: KindIP, Value: "203.0.113.5", Until: now.Add(time.Hour), Created: now}))
	require.NoError(t, os.Remove(path))

	_, ok, err := store.Banned(ctx, KindIP, "203.0.113.5")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.False(t, store.Status().Exists)

	// The next ban recreates the file.
	require.NoError(t, store.Ban(ctx, Ban{Kind: KindIP, Value: "203.0.113.6", Until: now.Add(time.Hour), Created: now}))
	assert.True(t, store.Status().Exists)
}

func TestFileStoreInvalidJSONKeepsSnapshot(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store, path := newTestStore(t, func() time.Time { return now })
	ctx := context.Background()

	require.NoError(t, store.Ban(ctx, Ban{Kind: KindIP, Value: "203.0.113.5", Until: now.Add(time.Hour), Created: now}))
	require.NoError(t, os.WriteFile(path, []byte("{ this is not json"), 0o600))

	_, ok, err := store.Banned(ctx, KindIP, "203.0.113.5")
	require.NoError(t, err)
	assert.True(t, ok, "a broken file must not lift existing bans")
	assert.NotEmpty(t, store.Status().LastError)
}

func TestFileStoreSkipsInvalidAndExpiredEntries(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "login_bans.json")
	content := `{"bans":[
		{"kind":"ip","value":"203.0.113.5","until":` + int64String(now.Add(time.Hour).Unix()) + `,"lastLogin":"alice"},
		{"kind":"account","value":"Bob","until":` + int64String(now.Add(time.Hour).Unix()) + `},
		{"kind":"nope","value":"x","until":` + int64String(now.Add(time.Hour).Unix()) + `},
		{"kind":"account","value":"carol","until":` + int64String(now.Add(-time.Hour).Unix()) + `}
	]}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	store, err := NewFileStore(path, func() time.Time { return now })
	require.NoError(t, err)

	bans, err := store.List(context.Background())
	require.NoError(t, err)
	require.Len(t, bans, 2)
	assert.Equal(t, "203.0.113.5", bans[1].Value)
	assert.Equal(t, "bob", bans[0].Value)

	status := store.Status()
	assert.Equal(t, 1, status.SkippedCount)
	assert.Equal(t, 1, status.ExpiredCount)
	assert.Empty(t, status.LastError)
}

func TestFileStoreTouchIPLastLogin(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store, path := newTestStore(t, func() time.Time { return now })
	ctx := context.Background()

	require.NoError(t, store.Ban(ctx, Ban{
		Kind:      KindIP,
		Value:     "203.0.113.5",
		Until:     now.Add(time.Hour),
		Created:   now,
		LastLogin: "alice",
	}))

	now = now.Add(time.Second)
	require.NoError(t, store.TouchIPLastLogin(ctx, "203.0.113.5", "bob"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"lastLogin": "bob"`)

	// The second update inside the throttle window only reaches memory.
	now = now.Add(10 * time.Second)
	require.NoError(t, store.TouchIPLastLogin(ctx, "203.0.113.5", "carol"))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "carol")

	ban, ok, err := store.Banned(ctx, KindIP, "203.0.113.5")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "carol", ban.LastLogin)

	// After the window the next update is persisted.
	now = now.Add(61 * time.Second)
	require.NoError(t, store.TouchIPLastLogin(ctx, "203.0.113.5", "dave"))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"lastLogin": "dave"`)
}

func TestFileStoreFilePermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not report POSIX file permissions")
	}

	store, path := newTestStore(t, time.Now)
	require.NoError(t, store.Ban(context.Background(), Ban{
		Kind:    KindIP,
		Value:   "203.0.113.5",
		Until:   time.Now().Add(time.Hour),
		Created: time.Now(),
	}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}
