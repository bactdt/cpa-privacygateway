package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"duanjuapp/cpa/redact"
)

func TestStateTableAcquireNew(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	state, ok, reused := tbl.acquire("req-1")
	if !ok || reused {
		t.Fatalf("新请求应成功且非复用：ok=%v reused=%v", ok, reused)
	}
	if state == nil || !state.admitted {
		t.Error("state 应非 nil 且 admitted=true")
	}
	if tbl.activeCount() != 1 {
		t.Errorf("activeCount = %d", tbl.activeCount())
	}
}

func TestStateTableAcquireExisting(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	tbl.acquire("req-1")
	state, ok, reused := tbl.acquire("req-1")
	if !ok || !reused {
		t.Errorf("已存在请求应复用：ok=%v reused=%v", ok, reused)
	}
	if state == nil {
		t.Error("state 不应为 nil")
	}
	if tbl.activeCount() != 1 {
		t.Errorf("activeCount 应仍为 1：%d", tbl.activeCount())
	}
}

func TestStateTableAcquireMaxActive(t *testing.T) {
	tbl := newStateTable(1, 100, time.Minute)
	tbl.acquire("req-1")
	_, ok, _ := tbl.acquire("req-2")
	if ok {
		t.Error("超过 maxActive 应拒绝")
	}
}

func TestStateTableGet(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	tbl.acquire("req-1")
	state := tbl.get("req-1")
	if state == nil {
		t.Error("get 应返回 state")
	}
	if tbl.get("nonexistent") != nil {
		t.Error("不存在的请求应返回 nil")
	}
}

func TestStateTableSetMapping(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	m, _ := redact.NewMapping(10, 1024)
	tbl.acquire("req-1")
	tbl.setMapping("req-1", m)
	if tbl.mapping("req-1") != m {
		t.Error("mapping 应已设置")
	}
}

func TestStateTableSetMappingDoesNotAdmitMissingRequest(t *testing.T) {
	tbl := newStateTable(1, 1, time.Minute)
	m, _ := redact.NewMapping(10, 1024)
	if got, installed := tbl.setMapping("missing", m); got != nil || installed {
		t.Fatalf("缺少准入条目时不应写入 mapping：got=%v installed=%v", got, installed)
	}
	if tbl.activeCount() != 0 || tbl.get("missing") != nil {
		t.Fatal("setMapping 不应隐式创建状态条目")
	}
}

func TestStateTableSetMappingFirstWriterWins(t *testing.T) {
	tbl := newStateTable(2, 2, time.Minute)
	tbl.acquire("req-1")
	first, _ := redact.NewMapping(10, 1024)
	second, _ := redact.NewMapping(10, 1024)
	gotFirst, installedFirst := tbl.setMapping("req-1", first)
	gotSecond, installedSecond := tbl.setMapping("req-1", second)
	if gotFirst != first || !installedFirst {
		t.Fatalf("首个写入应获胜：got=%p installed=%v", gotFirst, installedFirst)
	}
	if gotSecond != first || installedSecond {
		t.Fatalf("后续写入应复用首个 mapping：got=%p installed=%v", gotSecond, installedSecond)
	}
	if tbl.mapping("req-1") != first {
		t.Fatal("状态表不应被后续 mapping 覆盖")
	}
}

func TestStateTableSetFormat(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	tbl.acquire("req-1")
	tbl.setFormat("req-1", "openai")
	state := tbl.get("req-1")
	if state.format != "openai" {
		t.Errorf("format = %q", state.format)
	}
}

func TestStateTableStreamRestorer(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	m, _ := redact.NewMapping(10, 1024)
	tbl.acquire("req-1")
	tbl.setMapping("req-1", m)
	sr := tbl.streamRestorer("req-1")
	if sr == nil {
		t.Fatal("streamRestorer 应非 nil")
	}
	sr2 := tbl.streamRestorer("req-1")
	if sr != sr2 {
		t.Error("同一请求应返回同一 StreamRestorer")
	}
}

func TestStateTableStreamRestorerNoMapping(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	tbl.acquire("req-1")
	if tbl.streamRestorer("req-1") != nil {
		t.Error("无 mapping 时应返回 nil")
	}
}

func TestStateTableRelease(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	tbl.acquire("req-1")
	tbl.release("req-1")
	if tbl.activeCount() != 0 {
		t.Errorf("release 后 activeCount = %d", tbl.activeCount())
	}
	if tbl.get("req-1") != nil {
		t.Error("release 后 get 应返回 nil")
	}
}

func TestStateTableJanitor(t *testing.T) {
	tbl := newStateTable(10, 100, 50*time.Millisecond)
	tbl.acquire("req-1")
	tbl.release("req-1")
	tbl.acquire("req-idle")
	tbl.setMapping("req-idle", &redact.Mapping{})
	time.Sleep(60 * time.Millisecond)
	tbl.janitor(time.Now())
	if tbl.get("req-1") != nil {
		t.Error("release 后过期条目应被清理")
	}
	if tbl.get("req-idle") == nil {
		t.Error("带 mapping 的条目应在途保留，不被 janitor 清理")
	}
}

func TestStateTableReset(t *testing.T) {
	tbl := newStateTable(10, 100, time.Minute)
	tbl.acquire("req-1")
	tbl.reset()
	if tbl.activeCount() != 0 {
		t.Errorf("reset 后 activeCount = %d", tbl.activeCount())
	}
	if tbl.get("req-1") != nil {
		t.Error("reset 后 get 应返回 nil")
	}
}

func TestStateTableEvictOldest(t *testing.T) {
	tbl := newStateTable(10, 2, time.Minute)
	tbl.acquire("req-1")
	time.Sleep(5 * time.Millisecond)
	tbl.acquire("req-2")
	time.Sleep(5 * time.Millisecond)
	tbl.acquire("req-3")
	if len(tbl.entries) > 2 {
		t.Errorf("应驱逐最旧条目，entries = %d", len(tbl.entries))
	}
}

func TestStateTableEvictSkipsActiveWhenFullOfInflight(t *testing.T) {
	tbl := newStateTable(10, 2, time.Minute)
	for _, id := range []string{"a", "b"} {
		if _, ok, _ := tbl.acquire(id); !ok {
			t.Fatalf("acquire(%s) 被拒", id)
		}
	}
	if _, ok, _ := tbl.acquire("c"); ok {
		t.Error("满表全在途时应拒绝新准入（fail-closed）")
	}
	if tbl.get("a") == nil || tbl.get("b") == nil {
		t.Error("在途条目不应被驱逐")
	}
}

func TestStateTableEvictReclaimsTTLIdleEntry(t *testing.T) {
	tbl := newStateTable(10, 1, 50*time.Millisecond)
	tbl.acquire("idle")
	tbl.setMapping("idle", &redact.Mapping{})
	time.Sleep(60 * time.Millisecond)
	if _, ok, _ := tbl.acquire("new"); !ok {
		t.Fatal("TTL 过期条目应可被驱逐腾位")
	}
	if tbl.get("idle") != nil {
		t.Error("过期条目应被驱逐")
	}
	if tbl.get("new") == nil {
		t.Error("新条目应存在")
	}
}

func TestStateTableTakeOver(t *testing.T) {
	old := newStateTable(10, 100, time.Minute)
	old.acquire("in-flight")
	mapping := &redact.Mapping{}
	old.setMapping("in-flight", mapping)
	newTbl := newStateTable(10, 100, time.Minute)
	newTbl.takeOverFrom(old)
	if newTbl.get("in-flight") == nil {
		t.Error("在途条目应移交到新表")
	}
	if newTbl.mapping("in-flight") != mapping {
		t.Error("mapping 应随条目移交")
	}
	if old.get("in-flight") == nil {
		t.Error("旧表读取应跟随移交后的新表")
	}
	if old.mapping("in-flight") != mapping {
		t.Error("旧表读取 mapping 应跟随移交后的新表")
	}
	if newTbl.activeCount() != 1 {
		t.Errorf("activeCount = %d, want 1", newTbl.activeCount())
	}
}

func TestStateTableJanitorReclaimsAdmittedAfterHardDeadline(t *testing.T) {
	tbl := newStateTable(10, 100, 50*time.Millisecond)
	tbl.acquire("stuck")
	time.Sleep(60 * time.Millisecond)
	tbl.janitor(time.Now())
	tbl.mu.Lock()
	kept := tbl.entries["stuck"] != nil
	tbl.mu.Unlock()
	if !kept {
		t.Fatal("1×TTL 内 admitted 条目应保留")
	}
	time.Sleep(60 * time.Millisecond)
	tbl.janitor(time.Now())
	tbl.mu.Lock()
	gone := tbl.entries["stuck"] == nil
	tbl.mu.Unlock()
	if !gone {
		t.Error("超过 2×TTL 的 admitted 条目应被强制清理，防止状态表耗尽")
	}
	if tbl.activeCount() != 0 {
		t.Errorf("activeCount = %d, want 0", tbl.activeCount())
	}
}

func TestStateTableConcurrentAccess(t *testing.T) {
	tbl := newStateTable(64, 100, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("req-%d", n%4)
			for j := 0; j < 50; j++ {
				tbl.acquire(id)
				mapping, _ := redact.NewMapping(10, 1<<20)
				tbl.setMapping(id, mapping)
				_ = tbl.mapping(id)
				_ = tbl.streamRestorer(id)
				tbl.setFormat(id, "openai")
				_ = tbl.get(id)
				if j%10 == 9 {
					tbl.release(id)
				}
			}
		}(i)
	}
	wg.Wait()
}

func BenchmarkStateTableAcquireWithEviction(b *testing.B) {
	tbl := newStateTable(0, 8192, time.Nanosecond)
	for i := 0; b.Loop(); i++ {
		if _, ok, _ := tbl.acquire(fmt.Sprintf("req-%d", i)); !ok {
			b.Fatal("acquire 应成功")
		}
	}
}

func BenchmarkStateTableJanitorFull(b *testing.B) {
	tbl := newStateTable(0, 8192, 10*time.Minute)
	now := time.Now()
	for i := 0; i < 8192; i++ {
		state := &requestState{admitted: true, lastSeen: now.Add(-11 * time.Minute)}
		tbl.entries[fmt.Sprintf("req-%d", i)] = state
	}
	tbl.active = 8192
	b.ReportAllocs()
	for b.Loop() {
		tbl.janitor(now)
		for i := 0; i < 8192; i++ {
			state := &requestState{admitted: true, lastSeen: now.Add(-11 * time.Minute)}
			tbl.entries[fmt.Sprintf("req-%d", i)] = state
		}
		tbl.active = 8192
	}
}

func BenchmarkStateTableJanitorFresh(b *testing.B) {
	tbl := newStateTable(0, 8192, 10*time.Minute)
	now := time.Now()
	for i := 0; i < 8192; i++ {
		state := &requestState{admitted: true, lastSeen: now}
		tbl.entries[fmt.Sprintf("req-%d", i)] = state
	}
	tbl.active = 8192
	b.ReportAllocs()
	for b.Loop() {
		tbl.janitor(now)
	}
}
