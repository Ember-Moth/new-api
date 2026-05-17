# k3s 部署指南

本文档说明如何使用 k3s 部署 new-api。当前部署方式保持前后端分离：后端是 API 服务，前端是独立静态站点容器，Ingress 负责路径转发。

数据库和 Redis 不部署在 k3s 集群内。生产环境请对接集群外部 PostgreSQL/MySQL、云数据库以及外部 Redis。

## 镜像

仓库提供两个 OCI 镜像构建文件：

| 文件 | 说明 |
| --- | --- |
| `Containerfile.backend` | 构建 Go 后端 API 镜像 |
| `Containerfile.frontend` | 构建前端静态站点镜像 |

本地构建示例：

```bash
VERSION=$(cat VERSION)

docker build -f Containerfile.backend \
  --build-arg VERSION="$VERSION" \
  -t ghcr.io/your-org/new-api/backend:$VERSION .

docker build -f Containerfile.frontend \
  --build-arg VITE_REACT_APP_VERSION="$VERSION" \
  -t ghcr.io/your-org/new-api/frontend:$VERSION .
```

默认前端镜像适合同域名路径部署。如果前端和后端使用不同域名，构建前端镜像时设置：

```bash
docker build -f Containerfile.frontend \
  --build-arg VITE_REACT_APP_SERVER_URL=https://api.example.com \
  -t ghcr.io/your-org/new-api/frontend:$VERSION .
```

## 工作流

`.github/workflows/container-images.yml` 会构建并发布两个镜像：

- `ghcr.io/<owner>/<repo>/backend`
- `ghcr.io/<owner>/<repo>/frontend`

触发方式：

- push 到 `main`
- push tag
- 手动 `workflow_dispatch`

手动触发时可以传入 `frontend_api_base_url`，用于构建跨域部署的前端镜像。默认留空，表示同域名路径部署。

## k3s 清单

部署清单位于 `deploy/k3s/`，使用 Kustomize 管理：

```bash
deploy/k3s/
├── app.yaml
├── ingress.yaml
├── kustomization.yaml
├── README.md
└── secret.example.yaml
```

默认 Ingress 使用 k3s 自带 Traefik，并采用单域名路径转发：

| 路径 | 服务 |
| --- | --- |
| `/api`、`/v1`、`/v1beta`、`/pg`、`/mj`、`/suno`、`/kling`、`/jimeng`、`/dashboard/billing` | 后端 |
| `/` | 前端 |

## 部署步骤

1. 修改 `deploy/k3s/kustomization.yaml` 中的镜像地址。
2. 修改 `deploy/k3s/app.yaml` 和 `deploy/k3s/ingress.yaml` 中的域名。
3. 创建密钥，并填写外部数据库和 Redis 连接信息：

```bash
cp deploy/k3s/secret.example.yaml /tmp/new-api-secret.yaml
kubectl create namespace new-api --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f /tmp/new-api-secret.yaml
```

4. 应用清单：

```bash
kubectl apply -k deploy/k3s
kubectl -n new-api rollout status deploy/new-api-backend
kubectl -n new-api rollout status deploy/new-api-frontend
```

5. 验证：

```bash
kubectl -n new-api get pods,svc,ingress
curl https://new-api.example.com/api/status
```

## 生产建议

- 生产环境必须使用 k3s 外部 PostgreSQL/MySQL 或云数据库，不在集群内创建数据库 Pod。
- 多副本部署必须设置固定 `SESSION_SECRET`、`CRYPTO_SECRET`，并配置外部 Redis。
- 默认清单不挂载数据库 PVC，后端 Pod 可无状态重建；持久化数据由外部数据库承担。
- 云数据库安全组需允许 k3s 节点或出口网关访问，并建议启用 TLS、备份和监控。
