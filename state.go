package main

import (
	"sync"
	"time"

	"duanjuapp/cpa/redact"
)

type requestState struct {
	mu       sync.Mutex
	mapping  *redact.Mapping
	stream   *redact.StreamRestorer
	admitted bool
	format   string
	lastSeen time.Time
}

func (s *requestState) setMapping(mapping *redact.Mapping) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mapping = mapping
	s.stream = nil
	s.lastSeen = time.Now()
}

func (s *requestState) setFormat(format string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.format = format
	s.lastSeen = time.Now()
}

func (s *requestState) getStreamRestorer() *redact.StreamRestorer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mapping == nil {
		return nil
	}
	if s.stream == nil {
		s.stream = redact.NewStreamRestorer(s.mapping)
	}
	s.lastSeen = time.Now()
	return s.stream
}

func (s *requestState) getMapping() *redact.Mapping {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeen = time.Now()
	return s.mapping
}

func (s *requestState) getFormat() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.format
}

type stateTable struct {
	mu        sync.Mutex
	entries   map[string]*requestState
	active    int
	maxActive int
	maxItems  int
	ttl       time.Duration
	redirect  *stateTable
}

func newStateTable(maxActive, maxItems int, ttl time.Duration) *stateTable {
	if maxActive < 0 {
		maxActive = 0
	}
	if maxItems < 1 {
		maxItems = 1024
	}
	return &stateTable{
		entries:   make(map[string]*requestState),
		maxActive: maxActive,
		maxItems:  maxItems,
		ttl:       ttl,
	}
}

func (t *stateTable) acquire(requestID string) (*requestState, bool, bool) {
	t.mu.Lock()
	if t.redirect != nil {
		next := t.redirect
		t.mu.Unlock()
		return next.acquire(requestID)
	}
	defer t.mu.Unlock()
	now := time.Now()
	if existing, ok := t.entries[requestID]; ok {
		existing.mu.Lock()
		existing.lastSeen = now
		existing.mu.Unlock()
		return existing, true, true
	}
	if t.maxActive > 0 && t.active >= t.maxActive {
		return nil, false, false
	}
	if len(t.entries) >= t.maxItems {
		if !t.evictOldestLocked() {
			return nil, false, false
		}
	}
	state := &requestState{admitted: true, lastSeen: now}
	t.entries[requestID] = state
	t.active++
	return state, true, false
}

func (t *stateTable) get(requestID string) *requestState {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.entries[requestID]
	if !ok {
		return nil
	}
	state.mu.Lock()
	state.lastSeen = time.Now()
	state.mu.Unlock()
	return state
}

func (t *stateTable) setMapping(requestID string, mapping *redact.Mapping) {
	t.mu.Lock()
	if t.redirect != nil {
		next := t.redirect
		t.mu.Unlock()
		next.setMapping(requestID, mapping)
		return
	}
	state, ok := t.entries[requestID]
	if !ok {
		if t.maxActive > 0 && t.active >= t.maxActive || len(t.entries) >= t.maxItems {
			t.mu.Unlock()
			return
		}
		state = &requestState{admitted: true, lastSeen: time.Now()}
		t.entries[requestID] = state
		t.active++
	}
	state.mu.Lock()
	hasMapping := state.mapping != nil
	state.mu.Unlock()
	if hasMapping {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	state.setMapping(mapping)
}

func (t *stateTable) setFormat(requestID, format string) {
	t.mu.Lock()
	state, ok := t.entries[requestID]
	t.mu.Unlock()
	if ok {
		state.setFormat(format)
	}
}

func (t *stateTable) streamRestorer(requestID string) *redact.StreamRestorer {
	t.mu.Lock()
	state, ok := t.entries[requestID]
	t.mu.Unlock()
	if !ok {
		return nil
	}
	return state.getStreamRestorer()
}

func (t *stateTable) mapping(requestID string) *redact.Mapping {
	t.mu.Lock()
	state, ok := t.entries[requestID]
	t.mu.Unlock()
	if !ok {
		return nil
	}
	return state.getMapping()
}

func (t *stateTable) release(requestID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.entries[requestID]
	if !ok {
		return
	}
	delete(t.entries, requestID)
	if state.admitted {
		t.active--
	}
	if t.active < 0 {
		t.active = 0
	}
}

func (t *stateTable) activeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

// janitor 清理空闲超过 TTL 的条目。admitted 条目若其映射仍被在途响应引用，
// 清理会丢失还原能力；这里在超过 2×TTL 后才强制清理，避免宿主丢失
// complete 通知时永久耗尽状态表（正常请求在 1×TTL 内完成）。
func (t *stateTable) janitor(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	hardDeadline := 2 * t.ttl
	for id, state := range t.entries {
		state.mu.Lock()
		age := now.Sub(state.lastSeen)
		state.mu.Unlock()
		if age <= t.ttl {
			continue
		}
		if state.admitted && age <= hardDeadline {
			continue
		}
		delete(t.entries, id)
		if state.admitted {
			t.active--
		}
	}
	if t.active < 0 {
		t.active = 0
	}
}

func (t *stateTable) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = make(map[string]*requestState)
	t.active = 0
}

func (t *stateTable) takeOverFrom(other *stateTable) {
	if other == nil || other == t {
		return
	}
	other.mu.Lock()
	other.redirect = t
	taken := other.entries
	other.entries = make(map[string]*requestState)
	other.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	active := 0
	for _, state := range taken {
		if state.admitted {
			active++
		}
	}
	t.entries = taken
	t.active = active
}

func (t *stateTable) setLimits(maxActive, maxItems int, ttl time.Duration) {
	if maxActive < 0 {
		maxActive = 0
	}
	if maxItems < 1 {
		maxItems = 1024
	}
	t.mu.Lock()
	t.maxActive, t.maxItems, t.ttl = maxActive, maxItems, ttl
	t.mu.Unlock()
}

func (t *stateTable) evictOldestLocked() bool {
	var oldestID string
	var oldest time.Time
	for id, state := range t.entries {
		state.mu.Lock()
		seen := state.lastSeen
		state.mu.Unlock()
		if oldestID == "" || seen.Before(oldest) {
			oldestID = id
			oldest = seen
		}
	}
	if oldestID == "" {
		return false
	}
	if time.Since(oldest) <= t.ttl {
		return false
	}
	if state := t.entries[oldestID]; state != nil && state.admitted {
		t.active--
	}
	delete(t.entries, oldestID)
	return true
}
