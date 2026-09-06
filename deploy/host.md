# 在服务器构建并复用宿主机服务

适用于 Linux 宿主机、Docker Compose v2，以及已经安装的 PostgreSQL 18+、DragonflyDB、ClickHouse 和 Nginx。`docker-compose.host.yml` 只启动 control/data 两个应用容器，两者使用同一个本地构建的镜像。前端暂时仍由控制面内嵌提供。

下列命令由部署人员在目标服务器执行。仓库中的这套配置未经过实际构建和运行验收。

## 1. 将当前代码放到服务器

使用包含当前修改的仓库提交，或在开发机导出并上传；公共镜像和上游仓库不包含这些本地修改。以下 SSH 地址需要替换：

```sh
git archive --format=tar.gz HEAD -o /tmp/new-api-source.tar.gz
scp /tmp/new-api-source.tar.gz user@server:/tmp/
```

首次部署时，在服务器将源码解压到专用目录：

```sh
mkdir -p /opt/new-api
cd /opt/new-api
tar -xzf /tmp/new-api-source.tar.gz
cp .env.host.example .env.host
chmod 600 .env.host
```

升级时保留 `.env.host`、`data/` 和密钥，不要用示例覆盖已有配置。

## 2. 准备宿主机连接

示例使用 Docker 网段 `172.30.80.0/24`、网关 `172.30.80.1`。确认与现有网络不冲突后，首次部署创建网络：

```sh
docker network create --subnet 172.30.80.0/24 --gateway 172.30.80.1 new-api-app-net
```

如果网络已存在，复用其实际网段与网关。更换网段时同时修改 `.env.host` 中的 DSN、`TRUSTED_PROXIES` 和宿主机访问规则。

容器里的 `127.0.0.1` 是容器自己。宿主机三个服务需要监听容器可访问的地址；只监听宿主机 `127.0.0.1` 无法通过网关连接。允许 Docker 网段访问 PostgreSQL、DragonflyDB 和 ClickHouse 对应端口，PostgreSQL 同时配置 `pg_hba.conf` 的账号、数据库及来源限制。无需向公网开放数据库端口。若绑定 Docker 网关地址，宿主机重启时也需要确保 Docker 网络先于这些服务就绪；已有稳定私网监听地址也可直接用于 DSN。

- PostgreSQL 必须为 18+。创建全新的专用 `new_api` 数据库和应用角色，应用角色需要拥有迁移建表权限。管理员在该数据库预先创建 `pg_trgm`、`pg_stat_statements` 扩展，并在服务器的 `shared_preload_libraries` 中保留已有项、加入 `pg_stat_statements`。
- ClickHouse 预先创建专用 `new_api_logs` 数据库和账号，授权该库的建表、迁移、查询、写入及日志清理操作。示例 DSN 使用原生协议端口 9000，按实际配置调整。
- DragonflyDB 设置认证，关闭 `cache_mode`，所有应用实例使用同一个专用逻辑数据库。这里含有会话、租约等运行状态，应配置持久化和恢复策略，不能作为随时清空的纯缓存。

生产表结构由控制面启动时的 SQL 迁移创建。不需要宿主机安装 Go、Bun 或 migrate CLI。当前面向新部署，首次安装使用新库，不要复用开发中旧版初始化脚本已经创建的表。

## 3. 填写配置并构建

编辑 `.env.host`：

- 填写三个 DSN，密码中的特殊字符做 URL 编码；保留示例的单引号可避免 Compose 对 `$` 插值。
- 分别运行两次 `openssl rand -hex 32`，填写 `SESSION_SECRET` 和 `CRYPTO_SECRET`，后续部署保持不变。
- `SESSION_COOKIE_TRUSTED_URL` 填实际 HTTPS Origin，例如 `https://api.example.com`，不要附加路径。
- 根据 `uname -m` 设置 `TARGETARCH`：`x86_64` 对应 `amd64`，`aarch64`/`arm64` 对应 `arm64`。
- 设置 `NEW_API_IMAGE_TAG` 为本次发布标识，建议使用源码提交号，并把相同值写入根目录 `VERSION` 文件。

例如发布标识为 `release-001` 时，先将 `.env.host` 中的 `NEW_API_IMAGE_TAG` 改为 `release-001`，然后执行：

```sh
printf '%s\n' 'release-001' > VERSION
docker compose --env-file .env.host -f docker-compose.host.yml build control
docker compose --env-file .env.host -f docker-compose.host.yml up -d
docker compose --env-file .env.host -f docker-compose.host.yml logs -f --tail=100
```

只构建一次 control；data 复用相同镜像。镜像构建需要访问镜像仓库及 Go/Bun 依赖源，构建工具只在镜像内运行。填有密钥的 `.env.host` 和运行数据已排除在 Git 与 Docker 构建上下文之外。

控制面先初始化 PostgreSQL/ClickHouse 并发布 DragonflyDB 快照，健康检查通过后数据面启动。启动失败先查看控制面日志。`/healthz` 表示应用已完成初始化，不代表持续检查了数据库健康或完成了账务验收。

## 4. 接入宿主机 Nginx

参考 `deploy/nginx.host.conf`，替换域名、证书路径。该文件需要被 Nginx 的 `http` 上下文包含；若已有对应域名的 HTTPS 站点，将代理配置合并到该站点，避免创建重复 `server`。`map` 放在 `http` 层一次即可。

默认分流如下：

| 请求 | 目标 |
| --- | --- |
| 页面、管理 API、`/v1/dashboard/` | `127.0.0.1:3001` 控制面 |
| `/v1`、`/v1beta`、`/pg`、`/mj`、`/{mode}/mj` | `127.0.0.1:3002` 数据面 |

自定义任务插件如果使用其他路由前缀，需要额外配置数据面 location。SSE/WebSocket 所需的超时、禁用代理缓冲和协议升级已在示例中配置。

```sh
sudo nginx -t && sudo systemctl reload nginx
```

访问配置的 HTTPS 域名完成管理员初始化并添加渠道、令牌。应用端口仅绑定宿主机回环地址，公网通过 Nginx 进入。

## 日常操作

```sh
# 查看状态
docker compose --env-file .env.host -f docker-compose.host.yml ps
# 停止应用；宿主机基础服务及外部 Docker 网络保留
docker compose --env-file .env.host -f docker-compose.host.yml down
```

更新代码时使用新的镜像标签，保留已有环境配置并重新执行构建、启动命令。更换镜像不保证数据库回滚安全；修改迁移前先备份 PostgreSQL 和 ClickHouse，账务及配置一致性仍需在实际环境单独验收。
