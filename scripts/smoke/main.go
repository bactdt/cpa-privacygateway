package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// 以下结构体布局逐字段复制自 /project/web/cpa/abi.go 开头的 C 前导
// （与 CPA 宿主 v8.0.22 internal/pluginhost/loader_unix.go 一致），防止漂移。

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer; // 来源：abi.go cliproxy_buffer

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api; // 来源：abi.go cliproxy_host_api

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api; // 来源：abi.go cliproxy_plugin_api

typedef int (*cliproxy_plugin_init_fn)(cliproxy_host_api*, cliproxy_plugin_api*);

// smoke_host_call 是 host_api.call 的空实现：响应置空、返回 0（宿主日志等回调全部吞掉）。
static int smoke_host_call(void* ctx, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	(void)ctx; (void)method; (void)request; (void)request_len;
	if (response != NULL) {
		response->ptr = NULL;
		response->len = 0;
	}
	return 0;
}

// smoke_host_free 是 host_api.free_buffer 的空实现（宿主从不分配响应缓冲）。
static void smoke_host_free(void* ptr, size_t len) {
	(void)ptr; (void)len;
}

static void* smoke_dlopen(const char* path) {
	return dlopen(path, RTLD_NOW | RTLD_LOCAL);
}

static void* smoke_dlsym(void* handle, const char* name) {
	return dlsym(handle, name);
}

static char* smoke_dupe(const char* s) {
	return strdup(s);
}

static uint8_t* smoke_dupe_bytes(const void* p, size_t n) {
	return (uint8_t*)memcpy(malloc(n), p, n);
}

static const char* smoke_dlerror(void) {
	return dlerror();
}

static int smoke_dlclose(void* handle) {
	return dlclose(handle);
}

static int smoke_call_init(void* fn, cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	return ((cliproxy_plugin_init_fn)fn)(host, plugin);
}

// 以下 static 包装用于从 Go 侧调用 plugin_api 里的 C 函数指针。
static int smoke_call_plugin(cliproxy_plugin_call_fn fn, char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return fn(method, request, request_len, response);
}

static void smoke_free_plugin_buffer(cliproxy_plugin_free_fn fn, void* ptr, size_t len) {
	fn(ptr, len);
}

static void smoke_shutdown_plugin(cliproxy_plugin_shutdown_fn fn) {
	fn();
}

static void smoke_set_host_api(cliproxy_host_api* api) {
	api->abi_version = 1;
	api->host_ctx = NULL;
	api->call = smoke_host_call;
	api->free_buffer = smoke_host_free;
}
*/
import "C"

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"unsafe"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	zipPath, err := findZip(".")
	if stepFailed("findZip", err) {
		return err
	}
	fmt.Println("smoke: findZip OK")

	tmpDir, err := os.MkdirTemp("", "smoke-lib-*")
	if stepFailed("mkdtemp", err) {
		return err
	}
	defer os.RemoveAll(tmpDir)

	libPath, err := extractLibrary(zipPath, tmpDir)
	if stepFailed("extractLibrary", err) {
		return err
	}
	fmt.Println("smoke: extractLibrary OK")

	handle, api, err := loadLibrary(libPath)
	if err != nil {
		return err
	}

	err = callSmokeSteps(api)
	shutdown(handle, api)
	return err
}

// loadLibrary 按 CPA 宿主 loader_unix.go 的顺序：dlopen(RTLD_NOW|RTLD_LOCAL)
// → dlsym("cliproxy_plugin_init") → init 返回 0 → abi_version==1 →
// call/free_buffer/shutdown 非空。
func loadLibrary(libPath string) (unsafe.Pointer, *C.cliproxy_plugin_api, error) {
	cPath := C.CString(libPath)
	defer C.free(unsafe.Pointer(cPath))
	handle := C.smoke_dlopen(cPath)
	if handle == nil {
		msg := C.smoke_dlerror()
		text := "未知错误"
		if msg != nil {
			text = C.GoString(msg)
		}
		return nil, nil, fail("dlopen", fmt.Sprintf("dlopen %s: %s", libPath, text))
	}

	cSymbol := C.CString("cliproxy_plugin_init")
	initFn := C.smoke_dlsym(handle, cSymbol)
	C.free(unsafe.Pointer(cSymbol))
	if initFn == nil {
		C.smoke_dlclose(handle)
		return nil, nil, fail("dlsym", "cliproxy_plugin_init 符号缺失")
	}

	hostAPI := (*C.cliproxy_host_api)(C.malloc(C.size_t(unsafe.Sizeof(C.cliproxy_host_api{}))))
	if hostAPI == nil {
		C.smoke_dlclose(handle)
		return nil, nil, fail("malloc", "分配 host_api 失败")
	}
	C.smoke_set_host_api(hostAPI)

	pluginAPI := (*C.cliproxy_plugin_api)(C.malloc(C.size_t(unsafe.Sizeof(C.cliproxy_plugin_api{}))))
	if pluginAPI == nil {
		C.free(unsafe.Pointer(hostAPI))
		C.smoke_dlclose(handle)
		return nil, nil, fail("malloc", "分配 plugin_api 失败")
	}
	if rc := C.smoke_call_init(initFn, hostAPI, pluginAPI); rc != 0 {
		C.free(unsafe.Pointer(hostAPI))
		C.free(unsafe.Pointer(pluginAPI))
		C.smoke_dlclose(handle)
		return nil, nil, fail("init", fmt.Sprintf("cliproxy_plugin_init 返回 %d", int(rc)))
	}
	if pluginAPI.abi_version != 1 {
		C.free(unsafe.Pointer(hostAPI))
		C.free(unsafe.Pointer(pluginAPI))
		C.smoke_dlclose(handle)
		return nil, nil, fail("init", fmt.Sprintf("plugin_api.abi_version=%d，期望 1", uint32(pluginAPI.abi_version)))
	}
	if pluginAPI.call == nil || pluginAPI.free_buffer == nil || pluginAPI.shutdown == nil {
		C.free(unsafe.Pointer(hostAPI))
		C.free(unsafe.Pointer(pluginAPI))
		C.smoke_dlclose(handle)
		return nil, nil, fail("init", "plugin_api.call/free_buffer/shutdown 存在空指针")
	}
	fmt.Println("smoke: dlopen/dlsym/init OK")
	return handle, pluginAPI, nil
}

// callSmokeSteps 依次执行 register / intercept_before / management.register /
// management.handle / request.complete，响应用 plugin_api.free_buffer 释放。
func callSmokeSteps(api *C.cliproxy_plugin_api) error {
	if err := stepRegister(api); err != nil {
		return err
	}
	if err := stepInterceptBefore(api); err != nil {
		return err
	}
	if err := stepManagement(api); err != nil {
		return err
	}
	return stepRequestComplete(api)
}

// panelEnabledSmoke 读取 PANEL_ENABLED=1 环境变量，决定冒烟流程按
// panel.enabled=true（注册声明 trace 页面、GET trace 返回 200/HTML）还是默认关闭路径验证。
func panelEnabledSmoke() bool {
	return os.Getenv("PANEL_ENABLED") == "1"
}

func stepRegister(api *C.cliproxy_plugin_api) error {
	configYAML := "enabled: true\nfail_closed: true\n"
	if panelEnabledSmoke() {
		configYAML += "panel:\n  enabled: true\n"
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(configYAML))
	raw, err := callPlugin(api, "plugin.register", []byte(fmt.Sprintf(`{"config_yaml":%q,"plugin_dir":""}`, encoded)))
	if err != nil {
		return err
	}
	var env smokeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fail("plugin.register", "信封不是合法 JSON: "+err.Error())
	}
	if !env.OK {
		return envelopeFailure("plugin.register", env.Error)
	}
	var result struct {
		SchemaVersion uint32 `json:"schema_version"`
		Metadata      struct {
			Name             string `json:"Name"`
			Version          string `json:"Version"`
			Author           string `json:"Author"`
			GitHubRepository string `json:"GitHubRepository"`
		} `json:"metadata"`
		Capabilities struct {
			ManagementAPI bool `json:"management_api"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return fail("plugin.register", "result 解码失败: "+err.Error())
	}
	if result.SchemaVersion < 1 || result.SchemaVersion > 6 {
		return fail("plugin.register", fmt.Sprintf("schema_version=%d 不在 1..6", result.SchemaVersion))
	}
	if result.Metadata.Name == "" || result.Metadata.Version == "" || result.Metadata.Author == "" || result.Metadata.GitHubRepository == "" {
		return fail("plugin.register", "metadata 的 Name/Version/Author/GitHubRepository 存在空值")
	}
	if result.Capabilities.ManagementAPI != panelEnabledSmoke() {
		return fail("plugin.register", fmt.Sprintf("capabilities.management_api=%v 与 PANEL_ENABLED=%v 不一致", result.Capabilities.ManagementAPI, panelEnabledSmoke()))
	}
	fmt.Println("smoke: plugin.register OK")
	return nil
}

func stepInterceptBefore(api *C.cliproxy_plugin_api) error {
	body := base64.StdEncoding.EncodeToString([]byte(`{"model":"m","messages":[{"role":"user","content":"mail user@example.com"}]}`))
	raw, err := callPlugin(api, "request.intercept_before", []byte(fmt.Sprintf(`{"RequestID":"smoke-1","SourceFormat":"openai","Body":%q}`, body)))
	if err != nil {
		return err
	}
	var env smokeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fail("request.intercept_before", "信封不是合法 JSON: "+err.Error())
	}
	if !env.OK {
		return envelopeFailure("request.intercept_before", env.Error)
	}
	var result struct {
		Body []byte `json:"Body"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return fail("request.intercept_before", "result 解码失败: "+err.Error())
	}
	decoded := string(result.Body)
	if !contains(decoded, "[[PG_") {
		return fail("request.intercept_before", "返回正文不含 [[PG_ 占位符")
	}
	if contains(decoded, "user@example.com") {
		return fail("request.intercept_before", "返回正文仍含原始邮箱（不回显正文）")
	}
	fmt.Println("smoke: request.intercept_before OK")
	return nil
}

func stepManagement(api *C.cliproxy_plugin_api) error {
	raw, err := callPlugin(api, "management.register", nil)
	if err != nil {
		return err
	}
	var env smokeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fail("management.register", "信封不是合法 JSON: "+err.Error())
	}
	if !env.OK {
		return envelopeFailure("management.register", env.Error)
	}
	var result struct {
		Resources []struct {
			Path string `json:"Path"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return fail("management.register", "result 解码失败: "+err.Error())
	}
	wantResources := 0
	if panelEnabledSmoke() {
		wantResources = 1
	}
	if len(result.Resources) != wantResources {
		return fail("management.register", fmt.Sprintf("PANEL_ENABLED=%v 时 resources 应为 %d 项，实际 %d 项", panelEnabledSmoke(), wantResources, len(result.Resources)))
	}
	fmt.Println("smoke: management.register OK")

	// 资源路由：trace 页面（panel.enabled=true 时 200/HTML，否则 404）。
	handleResult, err := managementHandle(api, "GET", "trace")
	if err != nil {
		return err
	}
	wantStatus := 404
	if panelEnabledSmoke() {
		wantStatus = 200
	}
	if handleResult.StatusCode != wantStatus {
		return fail("management.handle", fmt.Sprintf("PANEL_ENABLED=%v 时 GET trace StatusCode=%d，期望 %d", panelEnabledSmoke(), handleResult.StatusCode, wantStatus))
	}
	if panelEnabledSmoke() && !bytes.Contains(bytes.ToLower(handleResult.Body), []byte("<!doctype html")) {
		return fail("management.handle", "panel.enabled=true 时 GET trace Body 应为 HTML 页面")
	}
	// 数据路由：GET traces 返回 JSON 快照，DELETE 清空缓冲。
	dataResult, err := managementHandle(api, "GET", "traces")
	if err != nil {
		return err
	}
	if panelEnabledSmoke() {
		if dataResult.StatusCode != 200 {
			return fail("management.handle", fmt.Sprintf("GET traces StatusCode=%d，期望 200", dataResult.StatusCode))
		}
		if !bytes.Contains(dataResult.Body, []byte(`"requests"`)) {
			return fail("management.handle", "GET traces Body 应含 requests 字段: "+string(dataResult.Body))
		}
		delResult, errDel := managementHandle(api, "DELETE", "traces")
		if errDel != nil {
			return errDel
		}
		if delResult.StatusCode != 200 {
			return fail("management.handle", fmt.Sprintf("DELETE traces StatusCode=%d，期望 200", delResult.StatusCode))
		}
	} else if dataResult.StatusCode != 404 {
		return fail("management.handle", fmt.Sprintf("PANEL_ENABLED=false 时 GET traces StatusCode=%d，期望 404", dataResult.StatusCode))
	}
	fmt.Println("smoke: management.handle OK")
	return nil
}

// managementHandle 调用 management.handle 并解出 ManagementResponse。
func managementHandle(api *C.cliproxy_plugin_api, method, path string) (struct {
	StatusCode int    `json:"StatusCode"`
	Body       []byte `json:"Body"`
}, error) {
	var result struct {
		StatusCode int    `json:"StatusCode"`
		Body       []byte `json:"Body"`
	}
	raw, err := callPlugin(api, "management.handle", []byte(`{"Method":"`+method+`","Path":"`+path+`"}`))
	if err != nil {
		return result, err
	}
	var env smokeEnvelope
	if errJSON := json.Unmarshal(raw, &env); errJSON != nil {
		return result, fail("management.handle", "信封不是合法 JSON: "+errJSON.Error())
	}
	if !env.OK {
		return result, envelopeFailure("management.handle", env.Error)
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return result, fail("management.handle", "result 解码失败: "+err.Error())
	}
	return result, nil
}

func stepRequestComplete(api *C.cliproxy_plugin_api) error {
	raw, err := callPlugin(api, "request.complete", []byte(`{"RequestID":"smoke-1"}`))
	if err != nil {
		return err
	}
	var env smokeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fail("request.complete", "信封不是合法 JSON: "+err.Error())
	}
	if !env.OK {
		return envelopeFailure("request.complete", env.Error)
	}
	fmt.Println("smoke: request.complete OK")
	return nil
}

// callPlugin 调用 plugin_api.call，返回原始信封字节（拷贝到 Go 堆后立即释放 C 缓冲）。
func callPlugin(api *C.cliproxy_plugin_api, method string, request []byte) ([]byte, error) {
	cMethodConst := C.CString(method)
	cMethod := C.smoke_dupe(cMethodConst)
	C.free(unsafe.Pointer(cMethodConst))
	defer C.free(unsafe.Pointer(cMethod))
	var cRequest *C.uint8_t
	if len(request) > 0 {
		cRequest = C.smoke_dupe_bytes(unsafe.Pointer(&request[0]), C.size_t(len(request)))
		defer C.free(unsafe.Pointer(cRequest))
	}
	var response C.cliproxy_buffer
	rc := C.smoke_call_plugin(api.call, cMethod, cRequest, C.size_t(len(request)), &response)
	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.smoke_free_plugin_buffer(api.free_buffer, response.ptr, response.len)
	}
	if rc != 0 {
		return nil, fail(method, fmt.Sprintf("plugin call 返回 %d", int(rc)))
	}
	return out, nil
}

// shutdown 调用 plugin_api.shutdown 再 dlclose；dlclose 失败不致命，仅打印 warn。
func shutdown(handle unsafe.Pointer, api *C.cliproxy_plugin_api) {
	if api != nil && api.shutdown != nil {
		C.smoke_shutdown_plugin(api.shutdown)
	}
	if handle != nil && C.smoke_dlclose(handle) != 0 {
		fmt.Fprintln(os.Stderr, "smoke: warn dlclose 失败（不致命）")
	}
}

func stepFailed(step string, err error) bool {
	if err == nil {
		return false
	}
	fmt.Fprintln(os.Stderr, "smoke: "+step+" 失败: "+err.Error())
	return true
}

func fail(step, message string) error {
	return fmt.Errorf("smoke: %s 失败: %s", step, message)
}

func envelopeFailure(step string, envErr *smokeError) error {
	if envErr == nil {
		return fail(step, "ok=false 但 error 缺失")
	}
	return fail(step, fmt.Sprintf("错误码=%s 消息=%s", envErr.Code, envErr.Message))
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
