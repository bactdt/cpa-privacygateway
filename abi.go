package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int PrivacyGatewayPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void PrivacyGatewayPluginFree(void*, size_t);
extern void PrivacyGatewayPluginShutdown(void);

static int privacygateway_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}

static void privacygateway_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type abiEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *abiError       `json:"error,omitempty"`
}

type abiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type abiLifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
	PluginDir  string `json:"plugin_dir,omitempty"`
}

type abiRequestInterceptRequest struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiResponseInterceptRequest struct {
	pluginapi.ResponseInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiStreamChunkInterceptRequest struct {
	pluginapi.StreamChunkInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
	ManagementAPI          bool `json:"management_api"`
}

type abiHostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level"`
	Message        string         `json:"message"`
	Fields         map[string]any `json:"fields,omitempty"`
}

const (
	logLevelInfo  = "info"
	logLevelWarn  = "warn"
	logLevelError = "error"
)

type hostCallbackIDKey struct{}

func withHostCallbackID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, hostCallbackIDKey{}, id)
}

func hostCallbackIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(hostCallbackIDKey{}).(string)
	return id
}

var abiState = struct {
	sync.RWMutex
	host         *C.cliproxy_host_api
	plugin       *plugin
	stopJanitor  chan struct{}
	shuttingDown bool
	inFlight     sync.WaitGroup
	// lastFailClosed 保存最近一次成功注册配置的 fail_closed 取值：
	// 实例不可用（未注册/shutdown/重建间隙）时以此尊重运维者的显式选择，
	// 从未注册过时为零值 false 的兜底由 gatewayFailClosed 补为 true。
	lastFailClosed bool
	hadConfig      bool
	// panelEnabled 保存最近一次成功注册配置的 panel.enabled 取值：
	// management.register/handle 据此决定是否声明面板资源与是否服务页面。
	// 从未注册过时为零值 false（面板关闭），与历史默认行为一致。
	panelEnabled bool
}{}

const maxCGoBytesLen = C.size_t(1<<31 - 1)

// resetPanelEnabledLocked 清除面板开关（调用方须持有 abiState 写锁）：
// shutdown 与重新 init 都视为面板不可达，未重新 register 前保持关闭。
func resetPanelEnabledLocked() {
	abiState.panelEnabled = false
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, pluginAPI *C.cliproxy_plugin_api) C.int {
	if host == nil || pluginAPI == nil {
		return 1
	}
	abiState.Lock()
	abiState.host = host
	abiState.shuttingDown = false
	resetPanelEnabledLocked()
	abiState.Unlock()

	pluginAPI.abi_version = C.uint32_t(pluginabi.ABIVersion)
	pluginAPI.call = C.cliproxy_plugin_call_fn(C.PrivacyGatewayPluginCall)
	pluginAPI.free_buffer = C.cliproxy_plugin_free_fn(C.PrivacyGatewayPluginFree)
	pluginAPI.shutdown = C.cliproxy_plugin_shutdown_fn(C.PrivacyGatewayPluginShutdown)
	return 0
}

//export PrivacyGatewayPluginCall
func PrivacyGatewayPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, abiErrorEnvelope("invalid_method", "method is required"))
		return 0
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		if requestLen > maxCGoBytesLen {
			writeABIResponse(response, abiErrorEnvelope("request_too_large", "request payload is too large"))
			return 0
		}
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleABIMethod(context.Background(), C.GoString(method), requestBytes)
	if errHandle != nil {
		writeABIResponse(response, abiErrorEnvelope("plugin_error", errHandle.Error()))
		return 0
	}
	writeABIResponse(response, raw)
	return 0
}

//export PrivacyGatewayPluginFree
func PrivacyGatewayPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export PrivacyGatewayPluginShutdown
func PrivacyGatewayPluginShutdown() {
	abiState.Lock()
	abiState.shuttingDown = true
	if abiState.stopJanitor != nil {
		close(abiState.stopJanitor)
		abiState.stopJanitor = nil
	}
	// 关停即清空追踪缓冲，避免残留敏感占位符映射与陈旧事件。
	if abiState.plugin != nil && abiState.plugin.traces != nil {
		abiState.plugin.traces.configure(false, 0, 0)
	}
	abiState.plugin = nil
	abiState.host = nil
	resetPanelEnabledLocked()
	abiState.Unlock()
	abiState.inFlight.Wait()
}

func handleABIMethod(ctx context.Context, method string, request []byte) (raw []byte, err error) {
	switch method {
	case pluginabi.MethodManagementRegister, pluginabi.MethodManagementHandle:
		return handleABIManagement(ctx, method, request)
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleABIRegister(ctx, method, request)
	}

	// TD-CPA-044：请求链路方法 panic 在此恢复为 fail-closed 终止信封，
	// 避免宿主按熔断处理使脱敏网关整体失效。生命周期方法不在此列。
	defer func() {
		if recovered := recover(); recovered != nil {
			logHost(ctx, logLevelError, "privacy gateway recovered from panic in request path", map[string]any{
				"method": method,
				"panic":  fmt.Sprintf("%v", recovered),
			})
			// RT-1：只记 panic，不回显 panic 文本（需求 5）。
			recordABIFailure(abiStateTraces(), method, string(request), "panic")
			raw, err = unavailableEnvelope(ctx, fmt.Errorf("panic: %v", recovered))
		}
	}()

	p, done, errPlugin := beginPluginCall()
	if errPlugin != nil {
		recordABIFailure(abiStateTraces(), method, string(request), "unavailable")
		return unavailableEnvelope(ctx, errPlugin)
	}
	defer done()

	switch method {
	case pluginabi.MethodRequestInterceptBefore:
		return handleRequestIntercept(ctx, method, request, p.InterceptRequestBeforeAuth)
	case pluginabi.MethodRequestInterceptAfter:
		return handleRequestIntercept(ctx, method, request, p.InterceptRequestAfterAuth)
	case pluginabi.MethodRequestComplete:
		var completion pluginapi.RequestCompletion
		if errDecode := json.Unmarshal(request, &completion); errDecode != nil {
			return nil, errDecode
		}
		if errCall := p.HandleRequestComplete(withHostCallbackID(ctx, ""), completion); errCall != nil {
			return nil, errCall
		}
		return abiOKEnvelope(struct{}{})
	case pluginabi.MethodResponseInterceptAfter:
		var req abiResponseInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			// 响应侧无终止协议：解码失败时按空结果直通，由流恢复器尾块兜底。
			logHost(ctx, logLevelWarn, "privacy gateway could not decode response payload", map[string]any{"cause": errDecode.Error()})
			recordABIFailure(p.traces, method, string(request), "decode_failed")
			return abiOKEnvelope(pluginapi.ResponseInterceptResponse{})
		}
		resp, errCall := p.InterceptResponse(withHostCallbackID(ctx, req.HostCallbackID), req.ResponseInterceptRequest)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodResponseInterceptStreamChunk:
		var req abiStreamChunkInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			logHost(ctx, logLevelWarn, "privacy gateway could not decode stream chunk payload", map[string]any{"cause": errDecode.Error()})
			recordABIFailure(p.traces, method, string(request), "decode_failed")
			return abiOKEnvelope(pluginapi.StreamChunkInterceptResponse{})
		}
		resp, errCall := p.InterceptStreamChunk(withHostCallbackID(ctx, req.HostCallbackID), req.StreamChunkInterceptRequest)
		return abiOKEnvelopeWithError(resp, errCall)
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleRequestIntercept(ctx context.Context, method string, request []byte, intercept func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error)) ([]byte, error) {
	var req abiRequestInterceptRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		recordABIFailure(abiStateTraces(), method, string(request), "decode_failed")
		return decodeFailureEnvelope(ctx, errDecode)
	}
	resp, errCall := intercept(withHostCallbackID(ctx, req.HostCallbackID), req.RequestInterceptRequest)
	return abiOKEnvelopeWithError(resp, errCall)
}

func handleABIRegister(ctx context.Context, method string, request []byte) ([]byte, error) {
	var req abiLifecycleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	cfg, errParse := parseConfig(req.ConfigYAML)
	if errParse != nil {
		return rejectLifecycleConfig(ctx, method, errParse)
	}
	abiState.Lock()
	if abiState.shuttingDown {
		abiState.Unlock()
		logHost(ctx, logLevelWarn, "privacy gateway ignored lifecycle call during shutdown", map[string]any{"method": method})
		return nil, fmt.Errorf("privacy gateway is shutting down")
	}
	// Serialize selection, handoff and publication to preserve concurrent calls.
	previous := abiState.plugin
	p := newPlugin(cfg)
	if previous != nil {
		p.states.takeOverFrom(previous.states)
		// 追踪缓冲跨重配保留；configure 按新容量缩容或按新开关清空（需求 7.3/7.4）。
		p.traces = previous.traces
		maxRequests, maxEvents := cfg.traceLimits()
		p.traces.configure(cfg.traceOn(), maxRequests, maxEvents)
	}
	if abiState.stopJanitor != nil {
		close(abiState.stopJanitor)
	}
	stop := make(chan struct{})
	abiState.plugin = p
	abiState.stopJanitor = stop
	abiState.lastFailClosed = cfg.failClosed()
	abiState.hadConfig = true
	abiState.panelEnabled = cfg.Panel.Enabled
	abiState.Unlock()
	go p.runJanitor(stop)
	// 用本次注册的取值构造信封，避免并发重配在解锁后翻转全局状态
	// 导致响应声明错误的能力位。
	return registrationEnvelope(cfg.Panel.Enabled)
}

func rejectLifecycleConfig(ctx context.Context, method string, errParse error) ([]byte, error) {
	abiState.RLock()
	hasPrevious := abiState.plugin != nil
	panelEnabled := abiState.panelEnabled
	abiState.RUnlock()
	if method == pluginabi.MethodPluginReconfigure && hasPrevious {
		logHost(ctx, logLevelError, "privacy gateway rejected new config, keeping previous config", map[string]any{"error": errParse.Error()})
		return registrationEnvelope(panelEnabled)
	}
	return nil, errParse
}

func registrationEnvelope(panelEnabled bool) ([]byte, error) {
	return abiOKEnvelope(abiRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      pluginMetadata(),
		Capabilities:  abiCapabilities{RequestInterceptor: true, RequestLifecyclePlugin: true, ResponseInterceptor: true, StreamChunkInterceptor: true, ManagementAPI: panelEnabled},
	})
}

// abiStateTraces 返回当前活跃插件的追踪缓冲（可能为 nil，供记录路径安全使用）。
func abiStateTraces() *traceBuffer {
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return nil
	}
	return p.traces
}

func beginPluginCall() (*plugin, func(), error) {
	abiState.Lock()
	defer abiState.Unlock()
	if abiState.shuttingDown {
		return nil, nil, fmt.Errorf("privacy gateway is shutting down")
	}
	if abiState.plugin == nil {
		return nil, nil, fmt.Errorf("privacy gateway is not registered")
	}
	abiState.inFlight.Add(1)
	return abiState.plugin, abiState.inFlight.Done, nil
}
func logHost(ctx context.Context, level, message string, fields map[string]any) {
	abiState.RLock()
	host := abiState.host
	abiState.RUnlock()
	if host == nil || host.call == nil {
		return
	}
	raw, errMarshal := json.Marshal(abiHostLogRequest{
		HostCallbackID: hostCallbackIDFromContext(ctx),
		Level:          level,
		Message:        message,
		Fields:         fields,
	})
	if errMarshal != nil {
		return
	}
	method := C.CString(pluginabi.MethodHostLog)
	defer C.free(unsafe.Pointer(method))
	request := C.CBytes(raw)
	defer C.free(request)
	var response C.cliproxy_buffer
	status := C.privacygateway_call_host(host, method, (*C.uint8_t)(request), C.size_t(len(raw)), &response)
	if response.ptr != nil && host.free_buffer != nil {
		defer C.privacygateway_free_host_buffer(host, response.ptr, response.len)
	}
	_ = status
}
func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
