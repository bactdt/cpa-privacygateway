package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"duanjuapp/cpa/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func concurrencyConfig() config {
	cfg := defaultConfig()
	cfg.CustomRules = []string{`PRIVATE_[0-9]{3}`}
	return cfg
}

func concurrencyBody(n int) []byte {
	return []byte(fmt.Sprintf(`{"content":"PRIVATE_%03d"}`, n))
}

func assertConcurrentRoundTrip(t *testing.T, p *plugin, id string, response pluginapi.RequestInterceptResponse, body []byte, err error) {
	t.Helper()
	if err != nil || response.Terminate {
		t.Fatalf("请求未成功脱敏：err=%v status=%d", err, response.StatusCode)
	}
	if bytes.Equal(response.Body, body) || !bytes.Contains(response.Body, []byte("[[PG_")) {
		t.Fatal("合成敏感值应被替换为占位符")
	}
	restored, err := p.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{RequestID: id, Body: response.Body})
	if err != nil || !bytes.Equal(restored.Body, body) {
		t.Fatalf("状态表必须能还原每个并发正文：err=%v", err)
	}
}

func TestConcurrentFirstRedactionsShareRequestMapping(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	const workers = 8
	responses := make([]pluginapi.RequestInterceptResponse, workers)
	errors := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := pluginapi.RequestInterceptRequest{RequestID: "same-id", SourceFormat: "openai", Body: concurrencyBody(n)}
			if n%2 == 0 {
				responses[n], errors[n] = p.InterceptRequestBeforeAuth(context.Background(), req)
			} else {
				responses[n], errors[n] = p.InterceptRequestAfterAuth(context.Background(), req)
			}
		}()
	}
	close(start)
	wg.Wait()
	for n := range workers {
		assertConcurrentRoundTrip(t, p, "same-id", responses[n], concurrencyBody(n), errors[n])
	}
	if p.states.activeCount() != 1 || p.states.mapping("same-id").Size() != workers {
		t.Fatal("相同 ID 只占一个准入条目，映射须保存每个正文的原值")
	}
}

func TestFirstRedactionReusesMappingInstalledByOtherCallback(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	p.states.acquire("same-id")
	winner, err := redact.NewMapping(10, 1024)
	if err != nil {
		t.Fatal(err)
	}
	p.states.setMapping("same-id", winner)
	cfg, detector := p.settings()
	body := concurrencyBody(1)
	response, err := p.redactInterceptRequest(cfg, detector, "openai", pluginapi.RequestInterceptRequest{
		RequestID: "same-id", SourceFormat: "openai", Body: body,
	}, false)
	assertConcurrentRoundTrip(t, p, "same-id", response, body, err)
	if p.states.mapping("same-id") != winner {
		t.Fatal("竞争失败的回调必须使用状态表已存的映射")
	}
}

func TestAfterAuthEnforcesAdmissionLimits(t *testing.T) {
	for _, limit := range []string{"max_concurrency", "max_requests"} {
		t.Run(limit, func(t *testing.T) {
			cfg := concurrencyConfig()
			if limit == "max_concurrency" {
				cfg.MaxConcurrency = 1
			} else {
				cfg.MaxRequests = 1
			}
			p := newPlugin(cfg)
			for n := range 3 {
				resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
					RequestID: fmt.Sprintf("after-%d", n), SourceFormat: "openai", Body: concurrencyBody(n),
				})
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					assertConcurrentRoundTrip(t, p, "after-0", resp, concurrencyBody(0), err)
				} else if !resp.Terminate || resp.StatusCode != http.StatusTooManyRequests || resp.ResponseHeaders.Get("Retry-After") != "1" {
					t.Fatalf("%s 超限应返回 429 和 Retry-After", limit)
				}
			}
			if p.states.activeCount() != 1 {
				t.Fatal("after-auth 不得绕过准入计数")
			}
		})
	}
}

func TestOldCallbacksFollowStateHandoff(t *testing.T) {
	old := newPlugin(concurrencyConfig())
	old.states.acquire("pending")
	next := newPlugin(concurrencyConfig())
	next.states.takeOverFrom(old.states)
	cfg, detector := old.settings()
	body := concurrencyBody(0)
	resp, err := old.redactInterceptRequest(cfg, detector, "openai", pluginapi.RequestInterceptRequest{
		RequestID: "pending", SourceFormat: "openai", Body: body,
	}, true)
	assertConcurrentRoundTrip(t, next, "pending", resp, body, err)
	assertConcurrentRoundTrip(t, old, "pending", resp, body, err)
	old.states.setFormat("pending", "openai")
	if next.states.get("pending").getFormat() != "openai" {
		t.Fatal("旧回调写入格式须跟随移交")
	}
	if old.states.streamRestorer("pending") != next.states.streamRestorer("pending") {
		t.Fatal("旧回调须共享新实例的流恢复器")
	}
	if err := old.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{RequestID: "pending"}); err != nil {
		t.Fatal(err)
	}
	if next.states.activeCount() != 0 || next.states.get("pending") != nil {
		t.Fatal("旧完成回调须释放新实例条目")
	}
}

func TestConcurrentReconfigureAndOldRequests(t *testing.T) {
	resetABIState(t)
	t.Cleanup(func() {
		abiState.Lock()
		if abiState.stopJanitor != nil {
			close(abiState.stopJanitor)
			abiState.stopJanitor = nil
		}
		abiState.plugin = nil
		abiState.Unlock()
	})
	raw, err := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("enabled: true\ncustom_rules: ['PRIVATE_[0-9]{3}']\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw); err != nil {
		t.Fatal(err)
	}
	abiState.RLock()
	old := abiState.plugin
	abiState.RUnlock()
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	responses := make([]pluginapi.RequestInterceptResponse, workers)
	errors := make([]error, workers)
	reloadErrors := make([]error, workers)
	for n := range workers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, reloadErrors[n] = handleABIRegister(context.Background(), pluginabi.MethodPluginReconfigure, raw)
		}()
		go func() {
			defer wg.Done()
			<-start
			responses[n], errors[n] = old.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
				RequestID: fmt.Sprintf("reload-%d", n), SourceFormat: "openai", Body: concurrencyBody(n),
			})
		}()
	}
	close(start)
	wg.Wait()
	abiState.RLock()
	current := abiState.plugin
	abiState.RUnlock()
	for n := range workers {
		if reloadErrors[n] != nil {
			t.Fatal(reloadErrors[n])
		}
		assertConcurrentRoundTrip(t, current, fmt.Sprintf("reload-%d", n), responses[n], concurrencyBody(n), errors[n])
	}
	if current.states.activeCount() != workers {
		t.Fatal("并发重配不得丢失准入条目")
	}
}

func TestConcurrentLimitUpdatesAndAdmission(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	var wg sync.WaitGroup
	for n := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 32 {
				if n == 0 {
					cfg := concurrencyConfig()
					cfg.MaxConcurrency = 3
					cfg.MaxRequests = 3
					p.reconfigure(cfg)
				} else {
					id := fmt.Sprintf("limits-%d", n)
					p.states.acquire(id)
					p.states.janitor(time.Now())
					p.states.release(id)
				}
			}
		}()
	}
	wg.Wait()
	if p.states.activeCount() != 0 {
		t.Fatal("准入和释放应保持计数一致")
	}
}

func TestTightenedItemLimitPreservesOldRequestsAndRejectsNewIDs(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	for n := range 3 {
		p.states.acquire(fmt.Sprintf("old-%d", n))
	}
	cfg := concurrencyConfig()
	cfg.MaxRequests = 1
	p.reconfigure(cfg)
	// Even an evictable oldest entry must not allow one-for-one replacement
	// while the retained table is larger than the new limit.
	state := p.states.get("old-0")
	state.mu.Lock()
	state.lastSeen = time.Now().Add(-2 * cfg.ttl())
	state.mu.Unlock()
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "new", SourceFormat: "openai", Body: concurrencyBody(0),
	})
	if err != nil || !resp.Terminate || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatal("保留的条目数大于收紧后的限额时，新 ID 必须被拒绝")
	}
	if p.states.activeCount() != 3 {
		t.Fatal("收紧限额不应丢弃已有请求")
	}
	for n := range 3 {
		p.states.release(fmt.Sprintf("old-%d", n))
	}
	resp, err = p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "new", SourceFormat: "openai", Body: concurrencyBody(0),
	})
	assertConcurrentRoundTrip(t, p, "new", resp, concurrencyBody(0), err)
	if p.states.activeCount() != 1 {
		t.Fatal("已有请求清理后才能在新限额内准入")
	}
}

func TestAfterAuthFirstRequestValidatesFormat(t *testing.T) {
	for _, closed := range []bool{true, false} {
		t.Run(fmt.Sprintf("fail_closed=%t", closed), func(t *testing.T) {
			cfg := concurrencyConfig()
			cfg.FailClosed = &closed
			p := newPlugin(cfg)
			body := concurrencyBody(0)
			resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
				RequestID: "unsupported", SourceFormat: "unsupported", Body: body,
			})
			if err != nil {
				t.Fatal(err)
			}
			if closed {
				if !resp.Terminate || resp.StatusCode != http.StatusUnsupportedMediaType || p.states.activeCount() != 0 {
					t.Fatal("首次 after-auth 的不支持格式应返回 415 并回收空条目")
				}
			} else if resp.Terminate || !bytes.Equal(resp.Body, body) {
				t.Fatal("fail_open 的不支持格式应原样放行")
			}
		})
	}
}

func TestAfterAuthFirstRequestBindsFormat(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	body := concurrencyBody(0)
	first, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "format", SourceFormat: "openai", Body: body,
	})
	assertConcurrentRoundTrip(t, p, "format", first, body, err)
	changed, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "format", SourceFormat: "claude", Body: concurrencyBody(1),
	})
	if err != nil || !changed.Terminate || changed.StatusCode != http.StatusBadRequest {
		t.Fatal("首次 after-auth 记录的格式必须阻止同 ID 换协议")
	}
	assertConcurrentRoundTrip(t, p, "format", first, body, nil)
}

func TestConcurrentFirstFormatsRejectConflict(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	formats := []string{"openai", "claude"}
	responses := make([]pluginapi.RequestInterceptResponse, len(formats))
	errors := make([]error, len(formats))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n, format := range formats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses[n], errors[n] = p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
				RequestID: "format", SourceFormat: format, Body: concurrencyBody(n),
			})
		}()
	}
	close(start)
	wg.Wait()
	accepted := 0
	for n, resp := range responses {
		if errors[n] != nil {
			t.Fatal(errors[n])
		}
		if resp.Terminate {
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatal("并发格式冲突须返回 400")
			}
		} else {
			accepted++
			assertConcurrentRoundTrip(t, p, "format", resp, concurrencyBody(n), nil)
		}
	}
	if accepted != 1 {
		t.Fatal("首次格式绑定只能接受一种协议")
	}
}

func TestFailedFirstCallbackPreservesPublishedMapping(t *testing.T) {
	p := newPlugin(concurrencyConfig())
	body := concurrencyBody(0)
	first, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "shared", SourceFormat: "openai", Body: body,
	})
	assertConcurrentRoundTrip(t, p, "shared", first, body, err)
	cfg, detector := p.settings()
	// Simulate the first admitted callback finishing after another callback has
	// already published a usable mapping under this ID.
	failed, err := p.redactInterceptRequest(cfg, detector, "openai", pluginapi.RequestInterceptRequest{
		RequestID: "shared", SourceFormat: "openai", Body: []byte(`{"content":`),
	}, true)
	if err != nil || !failed.Terminate || failed.StatusCode != http.StatusBadRequest {
		t.Fatal("坏正文须按解析失败拒绝")
	}
	assertConcurrentRoundTrip(t, p, "shared", first, body, nil)
	if p.states.activeCount() != 1 {
		t.Fatal("失败回调不得回收其它回调已经发布的映射")
	}
}

func TestFindingsFailurePreservesPublishedMapping(t *testing.T) {
	cfg := concurrencyConfig()
	cfg.MaxFindings = 1
	p := newPlugin(cfg)
	body := concurrencyBody(0)
	first, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "shared", SourceFormat: "openai", Body: body,
	})
	assertConcurrentRoundTrip(t, p, "shared", first, body, err)
	cfg, detector := p.settings()
	failed, err := p.redactInterceptRequest(cfg, detector, "openai", pluginapi.RequestInterceptRequest{
		RequestID: "shared", SourceFormat: "openai", Body: []byte(`{"content":"PRIVATE_000 PRIVATE_001"}`),
	}, true)
	if err != nil || !failed.Terminate || failed.StatusCode != http.StatusInternalServerError {
		t.Fatal("检出超限须拒绝当前正文")
	}
	assertConcurrentRoundTrip(t, p, "shared", first, body, nil)
	if p.states.activeCount() != 1 {
		t.Fatal("检出超限不得回收已发布映射")
	}
}
