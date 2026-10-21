package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed packaging/trace.html
var traceHTML []byte

// panelEnabled 报告面板/trace 页是否可达：取最近一次成功注册配置的 panel.enabled
// （见 abi.go 的 abiState.panelEnabled）。从未注册过时为零值 false，
// 与历史"默认关闭"行为一致。面板页面会经浏览器持有管理密钥，默认关闭。
func panelEnabled() bool {
	abiState.RLock()
	defer abiState.RUnlock()
	return abiState.panelEnabled
}

// managementVisible 报告管理资源是否可达：panel.enabled 且 trace_enabled
// （TD-11 联动语义）。任一关闭则注册声明空 resources，页面与数据路由返回 404。
func managementVisible() bool {
	abiState.RLock()
	defer abiState.RUnlock()
	return abiState.panelEnabled && abiState.traceEnabled
}

type abiResourceRoute struct {
	Path        string
	Menu        string
	Description string
}

// abiManagementRoute 是管理路由的线格式元素：宿主侧
// pluginapi.ManagementRoute 无 JSON tag，故本侧也必须用 Go 默认键名
// （Method/Path/Description），Menu 与 Handler 字段不编码（R10.2）。
type abiManagementRoute struct {
	Method      string
	Path        string
	Description string
}

type abiManagementRegistrationResponse struct {
	Routes    []abiManagementRoute `json:"routes"`
	Resources []abiResourceRoute   `json:"resources"`
}

type abiManagementHandleRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleABIManagement(ctx context.Context, method string, request []byte) (raw []byte, err error) {
	abiState.RLock()
	shuttingDown := abiState.shuttingDown
	abiState.RUnlock()
	if shuttingDown {
		return nil, fmt.Errorf("privacy gateway is shutting down")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			logHost(ctx, logLevelError, "privacy gateway recovered from panic in management handler", map[string]any{
				"method": method,
				"panic":  fmt.Sprintf("%v", recovered),
			})
			raw, err = abiOKEnvelope(pluginapi.ManagementResponse{
				StatusCode: http.StatusInternalServerError,
				Headers:    http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
				Body:       []byte("privacy gateway management handler failed"),
			})
		}
	}()

	switch method {
	case pluginabi.MethodManagementRegister:
		if !managementVisible() {
			return abiOKEnvelope(abiManagementRegistrationResponse{Routes: []abiManagementRoute{}, Resources: []abiResourceRoute{}})
		}
		// RT-1：trace 路由替换原 panel 资源路由（需求 12.1–12.4）。
		return abiOKEnvelope(managementTraceRoutes())
	case pluginabi.MethodManagementHandle:
		return handleABIManagementTrace(ctx, request)
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func managementPlainTextResponse(status int, message string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:       []byte(message),
	}
}

func managementResponseWithAllowHeader(status int, message string) pluginapi.ManagementResponse {
	resp := managementPlainTextResponse(status, message)
	resp.Headers.Set("Allow", "GET")
	return resp
}
