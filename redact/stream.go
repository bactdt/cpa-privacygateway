package redact

import (
	"sync"
)

type StreamRestorer struct {
	mu      sync.Mutex
	mapping *Mapping
	nonce   string
	pending []byte
	text    *textCarry // 按文本流键暂扣的占位符前缀与统计
}

func NewStreamRestorer(m *Mapping) *StreamRestorer {
	restorer := &StreamRestorer{mapping: m, text: newTextCarry(m)}
	if m != nil {
		restorer.nonce = m.Nonce()
	}
	return restorer
}

// Reset 清空跨块缓冲。宿主在请求失败/中止时调用，防止
// 半个占位符泄漏进下一个请求的流（TD-CPA-016 保留契约）。
func (s *StreamRestorer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = s.pending[:0]
	s.text.reset()
}

// Pending 返回未收尾缓冲字节数（字节残留 + 文本流暂扣），
// 供宿主诊断在途流状态（TD-CPA-016 保留契约）。
func (s *StreamRestorer) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) + s.text.pendingBytes()
}

// Stats 返回流式还原统计的副本；调用方修改返回值不影响内部状态。
func (s *StreamRestorer) Stats() StreamStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.stats
}

func (s *StreamRestorer) RestoreChunk(chunk []byte) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restoreChunkLocked(chunk)
}

func (s *StreamRestorer) restoreChunkLocked(chunk []byte) ([]byte, bool) {
	if s.mapping == nil || s.mapping.Size() == 0 {
		return chunk, false
	}
	data := chunk
	if len(s.pending) > 0 {
		merged := make([]byte, 0, len(s.pending)+len(chunk))
		merged = append(merged, s.pending...)
		merged = append(merged, chunk...)
		s.pending = s.pending[:0]
		data = merged
	}
	restored := s.mapping.RestoreEscapedBytes(data)
	tail := s.placeholderTail(restored)
	if tail == 0 {
		if len(restored) == 0 {
			return nil, true
		}
		return restored, false
	}
	s.pending = append(s.pending, restored[len(restored)-tail:]...)
	emit := restored[:len(restored)-tail]
	if len(emit) == 0 {
		return nil, true
	}
	return emit, false
}

func (s *StreamRestorer) Flush() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = s.pending[:0]
	// 文本流暂扣在 Flush 时已无法投递，计入 Leftover 后丢弃。
	s.text.stats.Leftover += s.text.pendingBytes()
	s.text.reset()
	return out
}

func (s *StreamRestorer) placeholderTail(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	start := len(data) - maxPlaceholderLen
	if start < 0 {
		start = 0
	}
	for i := start; i < len(data); i++ {
		if isPlaceholderPrefix(data[i:], s.nonce) {
			return len(data) - i
		}
	}
	return 0
}

const maxPlaceholderLen = len(placeholderOpen) + placeholderNonceHexLen + 1 + placeholderMaxSeqLen + len(placeholderClose)

func isPlaceholderPrefix(value []byte, nonce string) bool {
	prefix := placeholderPrefixFor(nonce)
	for i := 0; i < len(value) && i < len(prefix); i++ {
		if value[i] != prefix[i] {
			return false
		}
	}
	if len(value) < len(prefix) {
		return true
	}
	rest := value[len(prefix):]
	index := 0
	digits := 0
	for index < len(rest) && digits < 9 && rest[index] >= '0' && rest[index] <= '9' {
		index++
		digits++
	}
	if index == len(rest) {
		return true
	}
	if digits == 0 || rest[index] != ']' {
		return false
	}
	index++
	if index == len(rest) {
		return true
	}
	return rest[index] == ']' && index+1 == len(rest)
}
