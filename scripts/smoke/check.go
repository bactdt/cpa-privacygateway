// Command smoke 是 cgo 宿主模拟器：对 make package 产出的 zip 里解压出的
// privacygateway-v<ver>.so 做加载/注册/脱敏/管理接口冒烟，验证交付物而非
// build/ 目录的中间产物。流程与断言见 .kiro/specs/plugin-package/design.md
// 组件 4 与 requirements.md 第 9 条。
package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// smokeEnvelope 对应 abi.go 的 abiEnvelope（ok/result/error 信封），
// result 保留原始 JSON 以便各步骤按方法自行解码。
type smokeEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *smokeError     `json:"error,omitempty"`
}

type smokeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// findZip 在 dir 里找恰好一个 privacygateway_*_linux_amd64.zip，多于或为零个时报错。
func findZip(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "privacygateway_*_linux_amd64.zip"))
	if err != nil {
		return "", fmt.Errorf("无法匹配 zip：%w", err)
	}
	if len(matches) == 0 {
		return "", errors.New("当前目录没有 privacygateway_*_linux_amd64.zip，请先执行 make package")
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("找到 %d 个 zip 包，期望恰好 1 个：%v", len(matches), matches)
	}
	return matches[0], nil
}

// extractLibrary 从 zipPath 里只取根目录的 privacygateway-v*.so 一个条目
// （官方要求 zip 根目录必须直接包含动态库），校验条目名安全（无 ..、绝对路径、
// 反斜杠），以 0755 写出到 destDir。
func extractLibrary(zipPath, destDir string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("打开 zip 失败：%w", err)
	}
	defer reader.Close()

	var entryName string
	for _, entry := range reader.File {
		if strings.Contains(entry.Name, "/") {
			continue
		}
		if !strings.HasPrefix(entry.Name, "privacygateway-v") || !strings.HasSuffix(entry.Name, ".so") {
			continue
		}
		if entryName != "" {
			return "", fmt.Errorf("zip 内发现多个插件库条目：%s 与 %s", entryName, entry.Name)
		}
		entryName = entry.Name
	}
	if entryName == "" {
		return "", errors.New("zip 根目录没有 privacygateway-v*.so 条目（官方要求动态库必须位于 zip 根目录）")
	}
	if err := validateEntryName(entryName); err != nil {
		return "", err
	}

	src, err := reader.Open(entryName)
	if err != nil {
		return "", fmt.Errorf("打开 zip 条目 %s 失败：%w", entryName, err)
	}
	defer src.Close()

	libPath := filepath.Join(destDir, filepath.Base(entryName))
	dst, err := os.OpenFile(libPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", fmt.Errorf("创建 %s 失败：%w", libPath, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", fmt.Errorf("解压 %s 失败：%w", entryName, err)
	}
	if err := dst.Close(); err != nil {
		return "", fmt.Errorf("写入 %s 失败：%w", libPath, err)
	}
	return libPath, nil
}

// validateEntryName 拒绝包含 ..、绝对路径或反斜杠的条目名，防止 zip 路径穿越。
func validateEntryName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("zip 条目名不安全：%q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return fmt.Errorf("zip 条目名包含 ..：%q", name)
		}
	}
	return nil
}
