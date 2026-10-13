package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed packaging/panel.html
var panelHTML []byte

// panelEnabled 报告面板是否可达：取最近一次成功注册配置的 panel.enabled
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

type abiManagementRegistrationResponse struct {
	Routes    []struct{}         `json:"routes"`
	Resources []abiResourceRoute `json:"resources"`
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
			return abiOKEnvelope(abiManagementRegistrationResponse{Routes: []struct{}{}, Resources: []abiResourceRoute{}})
		}
		return abiOKEnvelope(abiManagementRegistrationResponse{
			Routes: []struct{}{},
			Resources: []abiResourceRoute{{
				Path:        "panel",
				Menu:        "隐私脱敏检测",
				Description: "检测隐私脱敏网关的加载状态与脱敏/还原效果",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleABIManagementPanel(ctx, request)
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleABIManagementPanel(ctx context.Context, request []byte) ([]byte, error) {
	if !panelEnabled() {
		return abiOKEnvelope(managementPlainTextResponse(http.StatusNotFound, "privacy gateway management resource not found"))
	}
	var req abiManagementHandleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return abiOKEnvelope(managementPlainTextResponse(http.StatusBadRequest, "privacy gateway could not decode management request"))
	}
	if req.Method != http.MethodGet {
		return abiOKEnvelope(managementResponseWithAllowHeader(http.StatusMethodNotAllowed, "privacy gateway management panel only supports GET"))
	}
	if normalizeManagementPath(req.Path) != "panel" {
		return abiOKEnvelope(managementPlainTextResponse(http.StatusNotFound, "privacy gateway management resource not found"))
	}
	logHost(ctx, logLevelInfo, "privacy gateway management panel served", nil)
	return abiOKEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Cache-Control":           []string{"no-store"},
			"Content-Security-Policy": []string{"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src *"},
		},
		Body: panelHTML,
	})
}

func normalizeManagementPath(path string) string {
	normalized := strings.Trim(path, "/")
	if normalized == "v0/resource/plugins/privacygateway/panel" {
		return "panel"
	}
	return normalized
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
