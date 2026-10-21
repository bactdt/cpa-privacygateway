package main

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// gatewayFailClosed 报告插件当前是否按 fail_closed 处理不可用/解码失败：
// 已注册实例读其配置；实例不可用时沿用最近一次注册配置的取值，
// 从未注册过时按 fail_closed 处理——隐私网关缺失时的最安全行为是
// 终止请求而非放行原文（TD-CPA-044）。
func gatewayFailClosed() bool {
	abiState.RLock()
	p := abiState.plugin
	last := abiState.lastFailClosed
	had := abiState.hadConfig
	abiState.RUnlock()
	if p != nil {
		cfg, _ := p.settings()
		return cfg.failClosed()
	}
	if had {
		return last
	}
	return true
}

// unavailableEnvelope 处理请求链路方法不可用（未注册/shutdown/panic）：
// fail_closed 返回 OK 信封内的 500 server_error 终止响应，fail_open 返回空直通信封。
// 宿主 v8.0.22 对 RPC error 放行（abi-contract §8 事实 5），因此不可用必须落在
// 信封结果里而非信封错误上（TD-CPA-044）。
func unavailableEnvelope(ctx context.Context, cause error) ([]byte, error) {
	if !gatewayFailClosed() {
		logHost(ctx, logLevelWarn, "privacy gateway unavailable, passing request through", map[string]any{
			"cause": errCauseString(cause),
		})
		return abiOKEnvelope(pluginapi.RequestInterceptResponse{})
	}
	return abiOKEnvelope(terminateUnavailableResponse(http.StatusInternalServerError, "server_error", "privacy gateway is unavailable"))
}

// decodeFailureEnvelope 处理请求解码失败：fail_closed 400 终止，fail_open 空直通。
func decodeFailureEnvelope(ctx context.Context, cause error) ([]byte, error) {
	if !gatewayFailClosed() {
		logHost(ctx, logLevelWarn, "privacy gateway could not decode request, passing through", map[string]any{
			"cause": errCauseString(cause),
		})
		return abiOKEnvelope(pluginapi.RequestInterceptResponse{})
	}
	return abiOKEnvelope(terminateUnavailableResponse(http.StatusBadRequest, "invalid_request_error", "request could not be decoded by privacy gateway"))
}

func terminateUnavailableResponse(status int, errorType, message string) pluginapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	})
	return pluginapi.RequestInterceptResponse{
		Terminate:  true,
		StatusCode: status,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/json"},
		},
		ResponseBody: body,
	}
}

func errCauseString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func abiOKEnvelopeWithError(v any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return abiOKEnvelope(v)
}

func abiOKEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(abiEnvelope{OK: true, Result: raw})
}

func abiErrorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(abiEnvelope{OK: false, Error: &abiError{Code: code, Message: message}})
	return raw
}
