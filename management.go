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

type abiResourceRoute struct {
	Path        string
	Menu        string
	Description string
}

type abiManagementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
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
		if !panelEnabled() {
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
