# 微服务开发文档

本文档说明在当前 k3s 部署体系下如何开发、联调和发布 new-api 的拆分组件。微服务拆分必须遵循渐进式原则：主服务保留本地实现，拆分组件通过配置开关启用，出现问题可以快速回退。

## 目录约定

建议后续新增目录如下：

```text
proto/
  preflight/v1/preflight.proto
  billing/v1/billing.proto

services/
  preflight/
    README.md
    Cargo.toml
    src/

internal/
  grpcclient/
    preflight/
```

说明：

- `proto/` 存放跨语言契约，不能引用 Go 内部 DTO。
- `services/` 存放独立微服务源码。
- `internal/grpcclient/` 存放 Go 主服务调用拆分组件的客户端封装。
- k3s 清单继续放在 `deploy/k3s/`。

## 开发流程

1. 先定义 proto 契约和错误码。
2. 在 Go 主服务中增加客户端接口和本地 fallback 实现。
3. 实现独立服务。
4. 增加 k3s Deployment/Service。
5. 在 staging 环境通过环境变量启用 gRPC。
6. 压测通过后再考虑默认启用。

## Preflight Service 契约草案

建议 proto 起点：

```proto
syntax = "proto3";

package newapi.preflight.v1;

service PreflightService {
  rpc EstimateTokens(EstimateTokensRequest) returns (EstimateTokensResponse);
  rpc AnalyzeMedia(AnalyzeMediaRequest) returns (AnalyzeMediaResponse);
}

message EstimateTokensRequest {
  string request_id = 1;
  string model = 2;
  string relay_format = 3;
  string token_type = 4;
  string combined_text = 5;
  repeated MediaInput media = 6;
  bool is_stream = 7;
}

message MediaInput {
  string id = 1;
  string kind = 2;
  string mime_type = 3;
  string detail = 4;
  string url = 5;
  string base64_data = 6;
}

message EstimateTokensResponse {
  int64 prompt_tokens = 1;
  repeated MediaAnalysis media = 2;
  repeated PreflightWarning warnings = 3;
}

message AnalyzeMediaRequest {
  string request_id = 1;
  repeated MediaInput media = 2;
}

message AnalyzeMediaResponse {
  repeated MediaAnalysis media = 1;
}

message MediaAnalysis {
  string id = 1;
  string kind = 2;
  string mime_type = 3;
  int64 size_bytes = 4;
  int32 width = 5;
  int32 height = 6;
  double duration_seconds = 7;
  int64 token_estimate = 8;
}

message PreflightWarning {
  string code = 1;
  string message = 2;
}
```

正式落地前可以继续精简字段，但必须保留 `request_id` 和可扩展的 warning 结构。

## Go 主服务接入规范

主服务不要在业务代码里直接依赖具体 gRPC 客户端。建议定义接口：

```go
type TokenEstimator interface {
    EstimateRequestToken(ctx context.Context, input EstimateInput) (EstimateResult, error)
}
```

实现：

- `LocalTokenEstimator`：调用当前 Go 本地逻辑。
- `GrpcPreflightEstimator`：调用 `preflight-service`。
- `FallbackTokenEstimator`：gRPC 失败时按配置回退本地实现。

环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PREFLIGHT_GRPC_ADDR` | 空 | 为空时禁用 gRPC |
| `PREFLIGHT_TIMEOUT_MS` | `1500` | 单次调用超时 |
| `PREFLIGHT_FAIL_OPEN` | `true` | gRPC 失败时回退本地实现 |

## 错误处理

gRPC 错误分三类：

| 类型 | 处理方式 |
| --- | --- |
| 参数错误 | 返回业务错误，不重试 |
| 服务暂时不可用 | 可回退本地实现 |
| 内部错误 | 记录日志，按 `PREFLIGHT_FAIL_OPEN` 决定是否回退 |

Preflight 不应该直接返回 HTTP 状态码。Gateway 负责把内部错误映射为现有 API 错误格式。

## k3s 服务模板

新增组件时，推荐追加到 `deploy/k3s/app.yaml`：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: preflight-service
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: preflight-service
      app.kubernetes.io/component: preflight
  template:
    metadata:
      labels:
        app.kubernetes.io/name: preflight-service
        app.kubernetes.io/component: preflight
    spec:
      containers:
        - name: preflight
          image: ghcr.io/your-org/new-api/preflight:latest
          ports:
            - name: grpc
              containerPort: 50051
          readinessProbe:
            grpc:
              port: 50051
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            grpc:
              port: 50051
            initialDelaySeconds: 15
            periodSeconds: 20
---
apiVersion: v1
kind: Service
metadata:
  name: preflight-service
spec:
  selector:
    app.kubernetes.io/name: preflight-service
    app.kubernetes.io/component: preflight
  ports:
    - name: grpc
      port: 50051
      targetPort: grpc
```

Gateway 通过 ConfigMap 增加：

```yaml
PREFLIGHT_GRPC_ADDR: preflight-service.new-api.svc.cluster.local:50051
```

## 本地联调

推荐流程：

```bash
# 1. 启动拆分服务
cd services/preflight
cargo run

# 2. 启动主服务并指向本地 gRPC
PREFLIGHT_GRPC_ADDR=127.0.0.1:50051 \
PREFLIGHT_FAIL_OPEN=true \
go run . --log-dir ./logs
```

如果拆分服务不可用，主服务必须能按配置回退到本地实现。

## 测试要求

Preflight 第一阶段至少覆盖：

- 纯文本请求 token 估算。
- OpenAI chat 格式消息计数。
- base64 图片 MIME/宽高识别。
- URL 图片下载失败回退。
- 音频时长估算。
- gRPC 超时 fallback。
- gRPC 返回参数错误时不 fallback。

Gateway 侧至少覆盖：

- `PREFLIGHT_GRPC_ADDR` 为空时走本地实现。
- gRPC 成功时使用远程结果。
- gRPC 失败且 `PREFLIGHT_FAIL_OPEN=true` 时回退。
- gRPC 失败且 `PREFLIGHT_FAIL_OPEN=false` 时返回错误。

## 发布要求

- 新组件镜像必须独立构建和发布。
- k3s 清单只能引用 ClusterIP Service，不通过 Ingress 暴露 gRPC。
- 配置必须可回滚，不能删除本地实现后再上线。
- 发布前至少在 staging 环境跑一次真实 relay 请求。

## 观测要求

每个 gRPC 服务必须记录：

- `request_id`
- `grpc_method`
- `latency_ms`
- `input_size_bytes`
- `error_code`

后续接入 Prometheus 时建议提供：

- `grpc_requests_total`
- `grpc_request_duration_seconds`
- `grpc_errors_total`
- `preflight_media_bytes_total`
- `preflight_fallback_total`

## 禁止事项

- 不要让拆分组件直接暴露公网入口。
- 不要让 Preflight 写业务数据库。
- 不要让组件共享本地磁盘状态。
- 不要在 proto 中复用 Go 内部结构体字段名作为唯一契约。
- 不要在没有 fallback 的情况下替换主请求链路。
