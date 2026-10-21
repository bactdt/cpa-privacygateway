package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func lifecycleRequest(t *testing.T, cfgYAML []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(abiLifecycleRequest{ConfigYAML: cfgYAML})
	if err != nil {
		t.Fatalf("marshal lifecycle request: %v", err)
	}
	return raw
}

// 生命周期：register 按配置初始化缓冲，shutdown 清空（需求 7.4、需求 12）。
func TestLifecycleTraceBufferInitAndShutdown(t *testing.T) {
	abiState.Lock()
	abiState.shuttingDown = false
	previousHost := abiState.host
	previousPlugin := abiState.plugin
	abiState.Unlock()
	defer func() {
		abiState.Lock()
		abiState.plugin = previousPlugin
		abiState.host = previousHost
		abiState.Unlock()
	}()

	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister,
		lifecycleRequest(t, []byte("enabled: true\ntrace_enabled: true\ntrace_max_requests: 5\ntrace_max_events: 8\n"))); err != nil {
		t.Fatalf("register: %v", err)
	}
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil || !p.traces.traceOn() {
		t.Fatalf("register 后应开启追踪: plugin=%v", p)
	}
	if maxRequests, _ := p.traces.stats(); maxRequests != 5 {
		t.Errorf("max_requests = %d，期望 5", maxRequests)
	}
	if _, maxEvents := p.traces.stats(); maxEvents != 8 {
		t.Errorf("max_events = %d，期望 8", maxEvents)
	}

	// 记录一条后 shutdown 应清空
	p.traces.record("r1", "t1", "request.received", nil)
	if len(p.traces.snapshot(0, 10, "").Requests) == 0 {
		t.Fatal("预置记录失败")
	}
	PrivacyGatewayPluginShutdown()
	if p.traces.traceOn() || len(p.traces.snapshot(0, 10, "").Requests) != 0 {
		t.Errorf("shutdown 后应清空并关闭追踪: on=%v n=%d",
			p.traces.traceOn(), len(p.traces.snapshot(0, 10, "").Requests))
	}
}
