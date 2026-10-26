package kv

import (
	"bytes"
	"container/heap"
	"container/list"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// itemOverhead is what an item costs besides its key and value, counted
// against max_size so that many tiny items are bounded too.
const itemOverhead = 96

// errEmptyKey is RoadRunner's: a key that is empty or only blanks.
var errEmptyKey = errors.New("empty key")

// entry is a key-value pair as a call carries it: an absolute RFC 3339
// timeout, empty for none.
type entry struct {
	key     string
	value   []byte
	timeout string
}

// item is a stored entry.
type item struct {
	entry
	expires time.Time     // zero: never
	elem    *list.Element // in storage.lru; Value is the *item
	index   int           // in storage.expiry, -1 if not there
}

func (it *item) size() int64 {
	return int64(len(it.key) + len(it.value) + itemOverhead)
}

func (it *item) expired(now time.Time) bool {
	return !it.expires.IsZero() && !now.Before(it.expires)
}

// storage is one named in-memory storage: bounded by max bytes, with items
// that expire at their timeout and least recently used ones evicted first.
// Expired items are invisible at once, and removed by sweep or when room is
// needed.
type storage struct {
	name string
	max  int64

	mu     sync.Mutex
	items  map[string]*item
	lru    *list.List // front: the most recently used
	expiry expiryHeap
	size   int64

	evicted uint64 // items evicted to make room, for metrics
}

func newStorage(name string, maxSize int64) *storage {
	return &storage{name: name, max: maxSize, items: make(map[string]*item), lru: list.New()}
}

func checkKeys(keys []string) error {
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return errEmptyKey
		}
	}
	return nil
}

// parseTimeout reads an entry's timeout; zero for none.
func parseTimeout(key, timeout string) (time.Time, error) {
	if timeout == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, timeout)
	if err != nil {
		return time.Time{}, fmt.Errorf("key %q: invalid timeout %q: want RFC 3339, like 2026-10-27T10:00:00Z", key,
			timeout)
	}
	return t, nil
}

// get returns the item of key, unless it is missing or expired.
func (s *storage) get(key string, now time.Time) *item {
	it := s.items[key]
	if it == nil || it.expired(now) {
		return nil
	}
	return it
}

// has returns the keys that exist.
func (s *storage) has(keys []string, now time.Time) ([]string, error) {
	if err := checkKeys(keys); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, k := range keys {
		if s.get(k, now) != nil {
			out = append(out, k)
		}
	}
	return out, nil
}

// mget returns the entries of the keys that exist, and marks them used.
func (s *storage) mget(keys []string, now time.Time) ([]entry, error) {
	if err := checkKeys(keys); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []entry
	for _, k := range keys {
		if it := s.get(k, now); it != nil {
			s.lru.MoveToFront(it.elem)
			out = append(out, entry{key: k, value: it.value})
		}
	}
	return out, nil
}

// ttl returns the timeouts of the keys that exist, empty for none.
func (s *storage) ttl(keys []string, now time.Time) ([]entry, error) {
	if err := checkKeys(keys); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []entry
	for _, k := range keys {
		if it := s.get(k, now); it != nil {
			out = append(out, entry{key: k, timeout: it.timeout})
		}
	}
	return out, nil
}

// set stores entries; one with a timeout that has passed is deleted, as
// PSR-16 asks for a TTL of zero or less. Nothing is stored if an entry is
// invalid or larger than the storage.
func (s *storage) set(entries []entry, now time.Time) error {
	expires := make([]time.Time, len(entries))
	for i, e := range entries {
		if strings.TrimSpace(e.key) == "" {
			return errEmptyKey
		}
		t, err := parseTimeout(e.key, e.timeout)
		if err != nil {
			return err
		}
		expires[i] = t
		if size := (&item{entry: e}).size(); size > s.max {
			return fmt.Errorf("key %q: the item takes %d bytes, more than the storage's max_size of %d", e.key, size,
				s.max)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range entries {
		s.remove(s.items[e.key])
		if !expires[i].IsZero() && !now.Before(expires[i]) {
			continue
		}
		// The value is part of the call's payload, which it would keep alive.
		it := &item{key: e.key, value: bytes.Clone(e.value), timeout: e.timeout, expires: expires[i], index: -1}
		it.elem = s.lru.PushFront(it)
		s.items[e.key] = it
		s.size += it.size()
		if !it.expires.IsZero() {
			heap.Push(&s.expiry, it)
		}
		s.makeRoom(now)
	}
	return nil
}

// mexpire sets the timeouts of the keys that exist; a timeout that has
// passed deletes the key. Missing keys are left alone.
func (s *storage) mexpire(entries []entry, now time.Time) error {
	expires := make([]time.Time, len(entries))
	for i, e := range entries {
		if strings.TrimSpace(e.key) == "" {
			return errEmptyKey
		}
		if e.timeout == "" {
			return fmt.Errorf("key %q: MExpire needs a timeout", e.key)
		}
		t, err := parseTimeout(e.key, e.timeout)
		if err != nil {
			return err
		}
		expires[i] = t
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range entries {
		it := s.get(e.key, now)
		switch {
		case it == nil:
		case !now.Before(expires[i]):
			s.remove(it)
		default:
			it.timeout, it.expires = e.timeout, expires[i]
			if it.index >= 0 {
				heap.Fix(&s.expiry, it.index)
			} else {
				heap.Push(&s.expiry, it)
			}
		}
	}
	return nil
}

// delete removes the keys.
func (s *storage) delete(keys []string) error {
	if err := checkKeys(keys); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		s.remove(s.items[k])
	}
	return nil
}

// clear removes every item.
func (s *storage) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.items)
	s.lru.Init()
	s.expiry = nil
	s.size = 0
}

// sweep removes the items that have expired.
func (s *storage) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
}

func (s *storage) sweepLocked(now time.Time) {
	for len(s.expiry) > 0 && s.expiry[0].expired(now) {
		s.remove(s.expiry[0])
	}
}

// makeRoom brings the size within max: expired items go first, then the
// least recently used. The item just stored is at the front of the LRU list
// and fits by itself, so it stays.
func (s *storage) makeRoom(now time.Time) {
	if s.size <= s.max {
		return
	}
	s.sweepLocked(now)
	for s.size > s.max {
		s.remove(s.lru.Back().Value.(*item)) //nolint:forcetypeassert // only *item is stored
		s.evicted++
	}
}

// remove deletes it, which may be nil, from every index.
func (s *storage) remove(it *item) {
	if it == nil {
		return
	}
	delete(s.items, it.key)
	s.lru.Remove(it.elem)
	if it.index >= 0 {
		heap.Remove(&s.expiry, it.index)
	}
	s.size -= it.size()
}

// stats returns the number of items, their size, and how many were evicted.
func (s *storage) stats() (items int, size int64, evicted uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items), s.size, s.evicted
}

// expiryHeap orders items by expiry, the earliest first (container/heap).
type expiryHeap []*item

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }

func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}

func (h *expiryHeap) Push(x any) {
	it := x.(*item) //nolint:forcetypeassert // only *item is pushed
	it.index = len(*h)
	*h = append(*h, it)
}

func (h *expiryHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	old[len(old)-1] = nil
	it.index = -1
	*h = old[:len(old)-1]
	return it
}
