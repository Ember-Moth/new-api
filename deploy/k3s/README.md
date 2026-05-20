# k3s 部署清单

这里放置 new-api 的 k3s/Kubernetes 部署清单。资源已合并为少量文件，默认使用同域名路径部署：

- `/api`、`/v1`、`/v1beta`、`/pg`、`/mj`、`/fast/mj`、`/relax/mj`、`/turbo/mj`、`/suno`、`/kling`、`/jimeng`、`/dashboard/billing` 转发到后端。
- 其他路径转发到前端静态服务。

## 准备密钥

复制示例密钥并填写真实值：

```bash
cp deploy/k3s/secret.example.yaml /tmp/new-api-secret.yaml
kubectl create namespace new-api --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f /tmp/new-api-secret.yaml
```

本部署不在 k3s 集群内创建数据库。请对接 k3s 外部的 PostgreSQL 或云 PostgreSQL，并按需对接外部 Redis：

- `SESSION_SECRET`：必须固定，避免重启后登录态失效。
- `CRYPTO_SECRET`：必须固定，避免 Redis 缓存或多副本场景下数据不可解密。
- `SQL_DSN`：必须填写外部 PostgreSQL 连接串，且必须以 `postgres://` 或 `postgresql://` 开头。
- `REDIS_CONN_STRING`：建议填写外部 Redis 连接串；多副本、缓存、限流场景必须设置。

## 修改镜像和域名

编辑 `deploy/k3s/kustomization.yaml` 中的镜像地址，编辑 `deploy/k3s/app.yaml` 和 `deploy/k3s/ingress.yaml` 中的域名。

如果前端部署到 Cloudflare Pages，可以保留 k3s 后端 API 域名，并在 Pages 环境变量中设置 `BACKEND_ORIGIN=https://api.example.com`。后端建议保持 `SESSION_COOKIE_SECURE=true`、`SESSION_COOKIE_SAMESITE=lax`，不要设置 `SESSION_COOKIE_DOMAIN`。

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

当前分支默认通过内嵌 SQL 迁移自动维护 PostgreSQL 结构、汇总回填和性能索引。迁移说明见 `docs/installation/postgresql-performance.md`。

k3s 默认使用 `POSTGRES_LOG_PARTITIONING=auto`。全新数据库会创建月度分区 `logs` 表；已有普通 `logs` 表会在后端启动迁移阶段自动转换为分区表。
