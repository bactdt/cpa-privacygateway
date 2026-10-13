# cpa（CLIProxyAPI 隐私脱敏网关插件）构建与门禁入口
# 所有目标均可一条命令复现；详见 AGENTS.md 与 docs/CI.md。

GO ?= go
GOFLAGS ?=
BUILD_DIR := build
LIB_NAME := privacygateway

ifeq ($(OS),Windows_NT)
  SHARED_EXT := dll
else
  UNAME_S := $(shell uname -s)
  ifeq ($(UNAME_S),Darwin)
    SHARED_EXT := dylib
  else
    SHARED_EXT := so
  endif
endif
SHARED_LIB := $(BUILD_DIR)/lib$(LIB_NAME).$(SHARED_EXT)

.PHONY: all package smoke smoke-panel build test vet fmt fmt-check lint coverage race vulncheck docs-check clean check

all: check

## build：产出 C 共享库（供 CPA 宿主 dlopen 加载）
build:
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -buildmode=c-shared -o $(SHARED_LIB) .

## test：单元测试
test:
	$(GO) test $(GOFLAGS) ./...

## race：数据竞态检测（并发结构门禁）
race:
	$(GO) test -race $(GOFLAGS) ./...

## vulncheck：依赖漏洞扫描（独立目标，不阻塞 make check；发现漏洞时以非零退出）
vulncheck:
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...; \
	fi

## vet：静态检查
vet:
	$(GO) vet ./...

## fmt：就地格式化
fmt:
	$(GO) fmt ./...

## fmt-check：格式化校验，有未格式化文件则失败
fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "gofmt 未通过，请执行 'make fmt'。以下文件需要格式化："; \
		echo "$$files"; \
		exit 1; \
	fi

## lint：自定义品味不变量（文件大小、结构化日志、重复实现）
lint:
	$(GO) run ./scripts/lint

## coverage：生成覆盖率报告并校验下限
coverage:
	$(GO) test $(GOFLAGS) -coverprofile=coverage.out $(GOFLAGS) ./... | grep -v "^?" | grep -v "scripts/" || true
	$(GO) test $(GOFLAGS) -coverprofile=coverage.out ./redact/ ./internal/archtest/ .
	$(GO) run ./scripts/covercheck -profile coverage.out

## docs-check：文档知识库校验（死链、索引完整性、必填节、新鲜度）
docs-check:
	$(GO) run ./scripts/check-docs

## check：本地门禁总入口（CI 与提交前使用）
check: fmt-check vet build lint test race coverage docs-check

## package：构建并在当前目录产出 privacygateway_<ver>_linux_amd64.zip
package: build
	$(GO) run ./scripts/package -lib $(SHARED_LIB) -out .

## smoke：对 make package 产出的 zip 解压后的 .so 做 dlopen/init/register/脱敏/管理页面冒烟
## PANEL_ENABLED=1 时按 panel.enabled=true 路径验证（面板 200/HTML）
smoke: package
	CGO_ENABLED=1 $(GO) run ./scripts/smoke

## smoke-panel：同 smoke，但按 panel.enabled=true 路径验证
smoke-panel: package
	PANEL_ENABLED=1 CGO_ENABLED=1 $(GO) run ./scripts/smoke

## clean：清理构建与覆盖率产物
clean:
	rm -rf $(BUILD_DIR) coverage.out
