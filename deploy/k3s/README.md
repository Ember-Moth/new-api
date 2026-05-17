# k3s 部署清单

这里放置 new-api 的 k3s/Kubernetes 部署清单。资源已合并为少量文件，默认使用同域名路径部署：

- `/api`、`/v1`、`/v1beta`、`/pg`、`/mj`、`/suno`、`/kling`、`/jimeng`、`/dashboard/billing` 转发到后端。
- 其他路径转发到前端静态服务。

## 准备密钥

复制示例密钥并填写真实值：

```bash
cp deploy/k3s/secret.example.yaml /tmp/new-api-secret.yaml
kubectl create namespace new-api --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f /tmp/new-api-secret.yaml
```

本部署不在 k3s 集群内创建数据库。请对接 k3s 外部的 PostgreSQL/MySQL 或云数据库，并按需对接外部 Redis：

- `SESSION_SECRET`：必须固定，避免重启后登录态失效。
- `CRYPTO_SECRET`：必须固定，避免 Redis 缓存或多副本场景下数据不可解密。
- `SQL_DSN`：必须填写外部 PostgreSQL/MySQL 连接串。
- `REDIS_CONN_STRING`：建议填写外部 Redis 连接串；多副本、缓存、限流场景必须设置。

## 修改镜像和域名

编辑 `deploy/k3s/kustomization.yaml` 中的镜像地址，编辑 `deploy/k3s/app.yaml` 和 `deploy/k3s/ingress.yaml` 中的域名。

## 部署

```bash
kubectl apply -k deploy/k3s
kubectl -n new-api rollout status deploy/new-api-backend
kubectl -n new-api rollout status deploy/new-api-frontend
```

查看服务：

```bash
kubectl -n new-api get pods,svc,ingress
```

## 注意

默认清单不包含数据库、Redis 和 PVC。数据库请使用云数据库或其他 k3s 集群外服务；需要多副本时，请确保外部 Redis 可用，并固定 `SESSION_SECRET`、`CRYPTO_SECRET`。
