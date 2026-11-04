package loginlimit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
)

const (
	// lastLoginPersistInterval throttles writes caused by TouchIPLastLogin.
	lastLoginPersistInterval = time.Minute
	// maxBanFileSize guards against loading an unexpectedly large file.
	maxBanFileSize = 16 << 20
)

type banKey struct {
	kind  Kind
	value string
}

type banJSON struct {
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Until     int64  `json:"until"`
	Created   int64  `json:"created,omitempty"`
	LastLogin string `json:"lastLogin,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type fileDoc struct {
	Bans []banJSON `json:"bans"`
}

// FileStore keeps ban records in a JSON file so they survive restarts and can
// be edited while the process is running. Every read re-checks the file on disk
// so an external edit takes effect on the next request.
type FileStore struct {
	path string
	now  func() time.Time

	mu             sync.Mutex
	bans           map[banKey]Ban
	modTime        time.Time
	size           int64
	exists         bool
	lastLoadedAt   time.Time
	lastError      string
	skippedCount   int
	expiredCount   int
	lastLoginWrite time.Time
}

// NewFileStore opens path, creating it with an empty document when missing.
func NewFileStore(path string, now func() time.Time) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("ban file path is empty")
	}
	if now == nil {
		now = time.Now
	}

	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errors.Wrap(err, "create ban file directory")
	}

	store := &FileStore{path: path, now: now, bans: make(map[banKey]Ban)}

	store.mu.Lock()
	defer store.mu.Unlock()

	if err := store.reloadLocked(); err != nil {
		return nil, err
	}
	if !store.exists {
		if err := store.writeFileLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Banned returns the active ban for the given kind and value.
func (s *FileStore) Banned(_ context.Context, kind Kind, value string) (Ban, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	ban, ok := s.bans[banKey{kind, value}]
	if !ok || !ban.Until.After(s.now()) {
		return Ban{}, false, nil
	}
	return ban, true, nil
}

// Ban adds or replaces a ban record and rewrites the file.
func (s *FileStore) Ban(_ context.Context, ban Ban) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	s.bans[banKey{ban.Kind, ban.Value}] = ban
	return s.writeFileLocked()
}

// Unban removes the ban record for the given kind and value.
func (s *FileStore) Unban(_ context.Context, kind Kind, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	delete(s.bans, banKey{kind, value})
	return s.writeFileLocked()
}

// TouchIPLastLogin records the last login attempted from a banned IP. The
// in-memory value is updated immediately, while the file is written at most
// once per minute to avoid write amplification from repeated attempts.
func (s *FileStore) TouchIPLastLogin(_ context.Context, ip, login string) error {
	if login == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	key := banKey{KindIP, ip}
	ban, ok := s.bans[key]
	if !ok || ban.LastLogin == login {
		return nil
	}

	ban.LastLogin = login
	s.bans[key] = ban

	now := s.now()
	if !s.lastLoginWrite.IsZero() && now.Sub(s.lastLoginWrite) < lastLoginPersistInterval {
		return nil
	}
	s.lastLoginWrite = now
	return s.writeFileLocked()
}

// List returns all active bans.
func (s *FileStore) List(_ context.Context) ([]Ban, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	now := s.now()
	bans := make([]Ban, 0, len(s.bans))
	for _, ban := range s.bans {
		if ban.Until.After(now) {
			bans = append(bans, ban)
		}
	}
	sort.Slice(bans, func(i, j int) bool {
		if bans[i].Kind != bans[j].Kind {
			return bans[i].Kind < bans[j].Kind
		}
		return bans[i].Value < bans[j].Value
	})
	return bans, nil
}

// Reload re-reads the ban file from disk.
func (s *FileStore) Reload(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

// Status returns the last known file state.
func (s *FileStore) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Path:         s.path,
		Exists:       s.exists,
		LastLoadedAt: s.lastLoadedAt,
		LastError:    s.lastError,
		SkippedCount: s.skippedCount,
		ExpiredCount: s.expiredCount,
	}
}

// Path returns the absolute ban file path.
func (s *FileStore) Path() string {
	return s.path
}

func (s *FileStore) reloadIfChangedLocked() {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			if s.exists {
				s.bans = make(map[banKey]Ban)
				s.exists = false
				s.size = 0
				s.modTime = time.Time{}
				s.skippedCount = 0
				s.expiredCount = 0
				s.lastError = ""
			}
			s.lastLoadedAt = s.now()
			return
		}
		s.lastError = err.Error()
		return
	}

	changed := !s.exists || info.Size() != s.size || !info.ModTime().Equal(s.modTime)
	if !changed {
		return
	}
	_ = s.reloadLocked()
}

func (s *FileStore) reloadLocked() error {
	now := s.now()
	s.lastLoadedAt = now

	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.bans = make(map[banKey]Ban)
			s.exists = false
			s.size = 0
			s.modTime = time.Time{}
			s.skippedCount = 0
			s.expiredCount = 0
			s.lastError = ""
			return nil
		}
		s.lastError = err.Error()
		return nil
	}
	if info.Size() > maxBanFileSize {
		s.lastError = "ban file is too large to load"
		return nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		s.lastError = err.Error()
		return nil
	}

	bans := make(map[banKey]Ban)
	skipped := 0
	expired := 0
	if strings.TrimSpace(string(data)) != "" {
		var doc fileDoc
		if err := json.Unmarshal(data, &doc); err != nil {
			// Keep the last valid snapshot so a hand-edited syntax error does
			// not silently lift every ban.
			s.lastError = err.Error()
			return nil
		}
		for _, item := range doc.Bans {
			ban, err := item.ban()
			if err != nil {
				skipped++
				continue
			}
			if !ban.Until.After(now) {
				expired++
				continue
			}
			bans[banKey{ban.Kind, ban.Value}] = ban
		}
	}

	s.bans = bans
	s.exists = true
	s.size = info.Size()
	s.modTime = info.ModTime()
	s.skippedCount = skipped
	s.expiredCount = expired
	s.lastError = ""
	return nil
}

func (s *FileStore) writeFileLocked() error {
	now := s.now()
	items := make([]banJSON, 0, len(s.bans))
	for _, ban := range s.bans {
		if !ban.Until.After(now) {
			continue
		}
		items = append(items, banJSON{
			Kind:      string(ban.Kind),
			Value:     ban.Value,
			Until:     ban.Until.Unix(),
			Created:   ban.Created.Unix(),
			LastLogin: ban.LastLogin,
			Reason:    ban.Reason,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Value < items[j].Value
	})

	data, err := json.MarshalIndent(fileDoc{Bans: items}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "marshal ban file")
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".login_bans-*.tmp")
	if err != nil {
		return errors.Wrap(err, "create temporary ban file")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.Wrap(err, "write temporary ban file")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.Wrap(err, "sync temporary ban file")
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrap(err, "close temporary ban file")
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return errors.Wrap(err, "change temporary ban file permission")
	}
	if err := renameWithRetry(tmpName, s.path); err != nil {
		return errors.Wrap(err, "replace ban file")
	}

	if info, err := os.Stat(s.path); err == nil {
		s.exists = true
		s.size = info.Size()
		s.modTime = info.ModTime()
	}
	s.lastLoadedAt = now
	s.lastError = ""
	s.skippedCount = 0
	s.expiredCount = 0
	return nil
}

func (j banJSON) ban() (Ban, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(j.Kind)))
	if kind != KindIP && kind != KindAccount {
		return Ban{}, errors.Newf("unknown ban kind %q", j.Kind)
	}

	value := strings.TrimSpace(j.Value)
	if value == "" || j.Until <= 0 {
		return Ban{}, errors.New("ban entry is missing value or until")
	}
	switch kind {
	case KindIP:
		value = NormalizeIP(value)
		if value == "" {
			return Ban{}, errors.Newf("invalid IP address %q", j.Value)
		}
	case KindAccount:
		value = accountValue(value)
		if value == "" {
			return Ban{}, errors.New("ban entry has an empty account")
		}
	}

	ban := Ban{
		Kind:      kind,
		Value:     value,
		Until:     time.Unix(j.Until, 0),
		LastLogin: j.LastLogin,
		Reason:    j.Reason,
	}
	if j.Created > 0 {
		ban.Created = time.Unix(j.Created, 0)
	}
	return ban, nil
}

// renameWithRetry works around editors that briefly hold the destination file
// open, which makes the replace fail on Windows.
func renameWithRetry(oldPath, newPath string) error {
	var err error
	for i := 0; i < 5; i++ {
		err = os.Rename(oldPath, newPath)
		if err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}
