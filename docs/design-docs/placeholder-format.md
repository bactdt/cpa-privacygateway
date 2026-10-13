# 占位符格式与映射

> 隐性知识入库（规则 §5 阶段 2 子任务 6）。本文件描述脱敏占位符的格式契约与映射生命周期。

## 1. 占位符格式

```text
[[PG_<nonce>_<seq>]]
```

| 部分 | 长度 | 说明 |
|---|---|---|
| `[[PG_` | 5 | 固定前缀 |
| `<nonce>` | 16 | 每请求随机 8 字节的十六进制（`crypto/rand`） |
| `_` | 1 | 分隔符 |
| `<seq>` | 1–9 | 该请求内递增序号，从 1 开始 |
| `]]` | 2 | 固定后缀 |

**正则**：`\[\[PG_[0-9a-f]{16}_[0-9]{1,9}\]\]`（`redact/mapping.go` 的 `placeholderRe`）

**最大长度**：`5 + 16 + 1 + 9 + 2 = 33` 字节（前缀 `[[PG_` 长度为 5；实现中的流式边界常量必须与此保持一致）。

## 2. 为什么用 nonce

- 每请求随机，防止跨请求占位符碰撞与猜测。
- 攻击者即使知道占位符格式，也无法伪造其他请求的占位符来诱导还原。

## 3. 映射生命周期

```text
NewMapping(capacity, maxBytes)
  → 生成 nonce
  → Placeholder(raw)：raw → [[PG_<nonce>_<seq>]]
       同一 raw 复用同一占位符（I-1 不变量）
       受 capacity 与 maxBytes 上限约束，超限返回 ErrCapacity
  → Restore / RestoreEscaped / RestoreEscapedBytes：占位符 → 原文
  → Clear / 请求完成时随 stateTable.release 释放
```

映射仅存活于单个请求内（`stateTable` 按 `requestID` 索引），请求完成即释放。

## 4. 三张表的作用

| 表 | 键 → 值 | 用途 |
|---|---|---|
| `rawToPh` | 原文 → 占位符 | 同值复用同一占位符 |
| `phToRaw` | 占位符 → 原文 | 非流式还原 |
| `phToEsc` | 占位符 → JSON 转义后的原文 | 流式还原（响应体是转义过的 JSON） |

## 5. 已知技术债

- 占位符格式在两处独立定义（`mapping.go` 的 `placeholderRe` 与 `stream.go` 的 `isPlaceholderPrefix` 拼接），格式变更需同步改多处 → TD-CPA-015。
