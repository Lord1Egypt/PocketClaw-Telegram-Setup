package pairing

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is a Store for local development and tests.
//
// It is process-local and therefore NOT usable in production on Vercel, where
// consecutive requests land in different function instances. The service
// refuses to select it unless explicitly allowed; see internal/config.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	byName  map[string]string
	tokens  map[string]string
	now     func() time.Time
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: make(map[string]Record),
		byName:  make(map[string]string),
		tokens:  make(map[string]string),
		now:     time.Now,
	}
}

// SetClock replaces the store's clock. For tests.
func (s *MemoryStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *MemoryStore) Describe() string { return "In-memory (development only)" }

func (s *MemoryStore) Ping(context.Context) error { return nil }

func (s *MemoryStore) Create(_ context.Context, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if _, exists := s.records[rec.ID]; exists {
		return ErrConflict
	}
	if _, exists := s.byName[NormalizeUsername(rec.SuggestedUsername)]; exists {
		return ErrConflict
	}
	s.records[rec.ID] = rec
	s.byName[NormalizeUsername(rec.SuggestedUsername)] = rec.ID
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	rec, ok := s.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *MemoryStore) FindByUsername(_ context.Context, username string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	id, ok := s.byName[NormalizeUsername(username)]
	if !ok {
		return Record{}, ErrNotFound
	}
	rec, ok := s.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *MemoryStore) Update(_ context.Context, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	existing, ok := s.records[rec.ID]
	if !ok {
		return ErrNotFound
	}
	// Mirror the Redis KEEPTTL semantics: an update never extends the window.
	rec.ExpiresAt = existing.ExpiresAt
	rec.CreatedAt = existing.CreatedAt
	s.records[rec.ID] = rec
	return nil
}

func (s *MemoryStore) PutToken(_ context.Context, id, token string) error {
	if token == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if _, ok := s.records[id]; !ok {
		return ErrNotFound
	}
	s.tokens[id] = token
	return nil
}

// TakeToken removes the token under the same lock that reads it, which is the
// in-process equivalent of Redis GETDEL.
func (s *MemoryStore) TakeToken(_ context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	token, ok := s.tokens[id]
	if !ok || token == "" {
		return "", ErrNotFound
	}
	delete(s.tokens, id)
	return token, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.records[id]; ok {
		delete(s.byName, NormalizeUsername(rec.SuggestedUsername))
	}
	delete(s.tokens, id)
	delete(s.records, id)
	return nil
}

// Len reports how many records are live. For tests.
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	return len(s.records)
}

func (s *MemoryStore) sweepLocked() {
	now := s.now()
	for id, rec := range s.records {
		if rec.Expired(now) {
			delete(s.records, id)
			delete(s.byName, NormalizeUsername(rec.SuggestedUsername))
			delete(s.tokens, id)
		}
	}
}
