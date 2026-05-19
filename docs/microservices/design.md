# 微服务拆分设计文档

本文档定义 new-api 在 k3s 环境下逐步拆分微服务的目标架构、组件边界、拆分顺序和 gRPC 调用原则。当前阶段仍以 Go 主服务为业务编排中心，拆分优先选择边界清晰、收益明确、可灰度回退的能力。

## 目标

- 保持现有 API、管理后台和前端访问路径稳定。
- 每次只拆出一个清晰组件，主服务保留本地实现作为回退路径。
- 拆分组件统一使用 k3s Deployment + ClusterIP Service 部署。
- 组件间默认使用 gRPC，外部流量仍只通过 Ingress 进入前端和主 API。
- 数据库和 Redis/DragonflyDB 使用 k3s 外部服务，不在默认清单中部署数据库或缓存服务。

## 总体架构

```text
Ingress
  ├─ new-api-frontend
  └─ new-api-backend  Go API Gateway
        ├─ preflight-service  Rust/Go, gRPC
        ├─ async-worker       Go, background jobs
        ├─ billing-service    Go, gRPC
        └─ relay-worker       Go/Rust, gRPC streaming

External dependencies
  ├─ PostgreSQL or cloud PostgreSQL
  └─ Redis-compatible cache, Redis or DragonflyDB
```

`new-api-backend` 仍是唯一公开 API 服务，负责认证、路由、权限、渠道选择、计费编排和对外错误格式。拆出的组件只通过集群内地址访问，例如：

```text
preflight-service.new-api.svc.cluster.local:50051
```

## 组件边界

| 组件 | 状态 | 主要职责 | 技术建议 |
| --- | --- | --- | --- |
| `new-api-backend` | 保留 | HTTP API、认证、用户、渠道、计费编排、数据库主写入 | Go |
| `new-api-frontend` | 已独立 | 前端静态站点 | React/Rsbuild |
| `preflight-service` | 第一阶段 | token 估算、媒体解析、MIME/尺寸识别、音频时长估算 | Rust 优先 |
| `async-worker` | 第二阶段 | 任务轮询、订阅重置、渠道检测、通知、指标刷写 | Go |
| `billing-service` | 第三阶段 | 预扣费、结算、退款、订阅额度、计费表达式 | Go |
| `relay-worker` | 第四阶段 | 上游模型请求、SSE/WebSocket 转发、provider adapter | Go 或 Rust |

## 第一阶段：Preflight Service

优先拆 `preflight-service`，因为它边界清晰、CPU/内存收益明确，且不直接拥有数据库事务。

### 职责

- 文本 token 统计。
- 图片 base64/URL 解析、MIME 检测、宽高识别。
- 音频文件时长估算。
- 多模态请求的预处理摘要。
- 返回计费前所需的 prompt tokens、media tokens 和文件元信息。

### 非职责

- 不做用户认证。
- 不做渠道选择。
- 不直接扣费。
- 不写业务数据库。
- 不保存长期文件。

### 调用路径

```text
controller.Relay
  -> request.GetTokenCountMeta()
  -> preflight client
  -> fallback local estimator when disabled or failed
  -> ModelPriceHelper
  -> PreConsumeBilling
```

### 回退策略

主服务新增环境变量：

| 变量 | 说明 |
| --- | --- |
| `PREFLIGHT_GRPC_ADDR` | 为空时使用本地 Go 实现；非空时调用 gRPC |
| `PREFLIGHT_TIMEOUT_MS` | 单次 gRPC 超时，建议默认 `1500` |
| `PREFLIGHT_FAIL_OPEN` | gRPC 失败时是否回退本地实现，建议默认 `true` |

## 第二阶段：Async Worker

后台任务目前与在线 API 进程共存。拆出 `async-worker` 后，API Pod 可以专注在线请求，降低任务轮询、渠道检测和通知发送对延迟的影响。

### 候选任务

- Midjourney / 视频任务轮询。
- Codex credential 自动刷新。
- 订阅额度重置。
- 渠道自动检测。
- 渠道上游模型更新检查。
- 通知发送。
- 性能指标 flush。

### 约束

- Worker 可以直接访问外部数据库和 Redis/DragonflyDB。
- 需要全局单实例的任务必须加分布式锁。
- Worker 不暴露公网入口。

## 第三阶段：Billing Service

计费服务涉及额度一致性、事务、订阅和日志，是高风险拆分项。必须在 Preflight 和 Worker 稳定后进行。

### 职责

- 预扣费。
- 请求失败退款。
- 请求完成结算。
- 订阅额度扣减。
- 计费表达式执行。
- 消费日志写入。

### 设计原则

- 保持幂等：每次计费会话必须有唯一 `billing_session_id`。
- 保持事务边界清晰：同一次额度变更必须在服务端事务内完成。
- gRPC 返回业务错误码，Gateway 负责映射成对外 API 错误格式。

## 第四阶段：Relay Worker

Relay Worker 是潜在收益最高但风险最大的拆分项。它涉及上游适配器、SSE、WebSocket、重试和 usage 解析，建议最后拆。

### 目标

- 将高并发上游代理从主服务迁出。
- 降低主服务 GC 和长连接压力。
- 为 Rust 实现高性能流式转发保留空间。

### 风险

- 流式响应跨 gRPC 转发会引入协议复杂度。
- 错误格式和重试逻辑必须保持兼容。
- usage 解析、计费结算、渠道自动禁用之间耦合较多。

## gRPC 设计原则

- proto 文件放在 `proto/`，按组件分目录。
- 使用 Protobuf 作为跨语言契约，避免复用 Go 内部 DTO。
- 请求必须携带 `request_id`，便于跨组件日志追踪。
- gRPC 超时必须短于 HTTP 请求整体超时。
- 所有跨组件调用必须有明确 fallback 或错误映射。
- 内部错误码稳定，不直接暴露给外部 API 用户。

## k3s 部署原则

- 每个组件一个 Deployment 和一个 ClusterIP Service。
- gRPC 服务只暴露集群内端口，不通过 Ingress 暴露。
- 组件配置使用 ConfigMap，密钥使用 Secret。
- 数据库和 Redis/DragonflyDB 使用外部服务。
- 新组件先以 `replicas: 1` 上线，稳定后按负载扩容。
- 组件间调用使用 service DNS，不写 Pod IP。

## 观测性

每个组件都必须输出结构化日志字段：

- `request_id`
- `component`
- `method`
- `latency_ms`
- `error_code`

推荐后续接入：

- Prometheus metrics。
- OpenTelemetry trace。
- gRPC health check。
- Pyroscope 或同类 profiler。

## 版本和兼容

- proto 字段只追加，不复用旧 tag。
- 移除字段前先废弃，至少保留一个发布周期。
- Gateway 支持本地实现和 gRPC 实现并存。
- 任何拆分都必须提供回滚开关。

## 阶段路线

1. 新增 proto 目录、gRPC 客户端封装和配置开关。
2. 实现 `preflight-service`，Gateway 默认仍走本地实现。
3. k3s 增加 `preflight-service` Deployment/Service。
4. 灰度启用 `PREFLIGHT_GRPC_ADDR`。
5. 压测和观测稳定后，开始拆 `async-worker`。
6. 计费服务和 Relay Worker 最后拆分。
