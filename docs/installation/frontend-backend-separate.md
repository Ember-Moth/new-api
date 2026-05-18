# 前后端分离部署指南

本文档说明如何将后端作为独立 API 服务运行，将 `frontend` 前端作为独立静态站点部署。

## 部署目标

推荐优先使用同源反向代理方案：

| 服务 | 示例地址 | 说明 |
| --- | --- | --- |
| 前端静态站点 | `https://app.example.com` | 部署 `frontend/dist` |
| 后端 API | `https://app.example.com/api/*` | 由边缘函数或网关反代到真实后端 |
| 真实后端 | `https://api.internal.example.com` | 运行 Go 后端服务 |

这种方案对浏览器来说是同源访问，Session Cookie 不会被当作第三方 Cookie，最适合 Cloudflare Pages。

也可以使用两个同一主域名下的子域名：

| 服务 | 示例地址 | 说明 |
| --- | --- | --- |
| 前端静态站点 | `https://web.example.com` | 部署 `frontend/dist` |
| 后端 API | `https://api.example.com` | 运行 Go 后端服务 |

也可以使用同一域名不同路径：

| 路径 | 说明 |
| --- | --- |
| `/` | 前端静态站点 |
| `/api`、`/v1`、`/v1beta`、`/pg`、`/mj`、`/suno`、`/kling`、`/jimeng` | 反向代理到后端 |

同域名路径方案跨域和 Cookie 问题最少；子域名方案更符合前后端独立部署。

## 关键配置

后端使用：

| 变量 | 示例 | 说明 |
| --- | --- | --- |
| `PORT` | `3000` | 后端监听端口 |
| `FRONTEND_BASE_URL` | `https://web.example.com` | 前端公开地址；后端未命中的非 API 路径会跳转到这里，同时用于 CORS 允许来源 |
| `SESSION_SECRET` | 随机字符串 | Web 登录会话密钥，生产环境必须设置 |
| `SESSION_COOKIE_SECURE` | `true` | 是否给 Session Cookie 增加 `Secure`；HTTPS 生产环境应开启 |
| `SESSION_COOKIE_SAMESITE` | `lax` | Session Cookie 的 `SameSite` 策略，可选 `strict`、`lax`、`none`、`default` |
| `SESSION_COOKIE_DOMAIN` | 留空 | Session Cookie 的 `Domain`；Cloudflare Pages Functions 同源反代时应留空 |
| `SESSION_COOKIE_MAX_AGE` | `2592000` | Session Cookie 有效期，单位秒 |
| `SQL_DSN` | 按数据库类型填写 | 可选；不设置时默认使用 SQLite |
| `REDIS_CONN_STRING` | `redis://...` | 可选；多节点或缓存场景建议使用 |

前端使用：

| 变量 | 示例 | 说明 |
| --- | --- | --- |
| `VITE_REACT_APP_SERVER_URL` | `https://api.example.com` | 后端公开地址；构建前端时写入产物 |
| `BACKEND_ORIGIN` | `https://api.example.com` | Cloudflare Pages Functions 使用的真实后端地址 |

`VITE_REACT_APP_SERVER_URL` 是构建期变量，修改后需要重新执行前端构建。
使用 Cloudflare Pages Functions 同源反代时，不要设置 `VITE_REACT_APP_SERVER_URL`，让前端请求 `/api`、`/pg` 等同源路径。

## Cookie 与跨域要求

管理后台依赖后端 Session Cookie。前端和后端完全不同站点时，不建议长期依赖跨站 Cookie。推荐把前端公开域名作为浏览器唯一入口，并使用边缘函数或网关把后端路径反代到真实后端。

Cloudflare Pages Functions 同源反代时：

- 浏览器访问 `https://app.example.com`
- 前端请求 `https://app.example.com/api/status`
- Pages Function 转发到 `https://api.example.com/api/status`
- 后端返回的 `Set-Cookie` 会落在 `app.example.com`，不是 `api.example.com`

后端推荐配置：

```bash
FRONTEND_BASE_URL=https://app.example.com
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_SAMESITE=lax
# SESSION_COOKIE_DOMAIN 保持未设置
```

也可以让前端和后端使用同一主域名的子域名，并且都使用 HTTPS，例如：

- `https://web.example.com`
- `https://api.example.com`

这种部署属于同站点请求，后端的 Session Cookie 可以在前端跨 origin 调用 API 时正常携带。

如果前端和后端是完全不同站点，例如 `https://web.example.app` 调用 `https://api.example.com`，跨站 Cookie 可能被浏览器策略拦截。此时优先使用 Pages Function 同源反代；只有明确需要开放第三方前端直连 API 时，才考虑 `SameSite=None; Secure` 或 JWT 模式。

## Cloudflare Pages Functions 部署

仓库已经在 `frontend/functions/` 中提供 Pages Function 反代实现，匹配以下后端路径：

- `/api`
- `/v1`
- `/v1beta`
- `/pg`
- `/mj`
- `/fast/mj`
- `/relax/mj`
- `/turbo/mj`
- `/suno`
- `/kling`
- `/jimeng`
- `/dashboard/billing`

Cloudflare Pages 项目配置：

| 配置项 | 值 |
| --- | --- |
| Root directory | `frontend` |
| Build command | `bun install && bun run build` |
| Build output directory | `dist` |
| Environment variable | `BACKEND_ORIGIN=https://api.example.com` |

前端构建时不要设置 `VITE_REACT_APP_SERVER_URL`。这样浏览器请求会保持同源，Pages Function 再把请求转发到 `BACKEND_ORIGIN`。

生产流量建议分开：

- 管理后台、登录、OAuth、Playground 可以走 Pages Function 同源反代。
- 对外提供给客户端调用的大流量 `/v1` API 可以继续暴露后端 API 域名，减少边缘函数调用成本和链路复杂度。

## 后端部署

在仓库根目录构建后端：

```bash
go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=$(cat VERSION)'" -o new-api
```

创建后端环境变量文件，例如 `/opt/new-api/backend.env`：

```bash
PORT=3000
FRONTEND_BASE_URL=https://app.example.com
SESSION_SECRET=replace_with_a_random_secret
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_SAMESITE=lax
SQL_DSN=postgresql://user:password@127.0.0.1:5432/new-api
REDIS_CONN_STRING=redis://:password@127.0.0.1:6379
TZ=Asia/Shanghai
```

启动后端：

```bash
set -a
. /opt/new-api/backend.env
set +a

./new-api --log-dir ./logs
```

后端现在是 API-only 服务，不需要 `frontend/dist` 存在。

## 前端部署

进入前端目录：

```bash
cd frontend
bun install
VITE_REACT_APP_SERVER_URL=https://api.example.com bun run build
```

构建完成后，将 `frontend/dist` 发布到静态站点服务。静态站点必须配置 SPA fallback：所有非文件路径返回 `index.html`。

使用 Cloudflare Pages Functions 同源反代时，构建命令改为：

```bash
cd frontend
bun install
bun run build
```

并在 Cloudflare Pages 环境变量中设置：

```bash
BACKEND_ORIGIN=https://api.example.com
```

## Nginx 示例：子域名部署

后端 API：

```nginx
server {
    listen 443 ssl http2;
    server_name api.example.com;

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

前端静态站点：

```nginx
server {
    listen 443 ssl http2;
    server_name web.example.com;

    root /var/www/new-api-web;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    location /static/ {
        try_files $uri =404;
        expires 30d;
        add_header Cache-Control "public, immutable";
    }
}
```

## Nginx 示例：同域名路径部署

这种方式仍然是前后端分离进程：Nginx 负责静态前端，Go 后端只处理 API。

前端构建时可以不设置 `VITE_REACT_APP_SERVER_URL`，让前端请求同源 API：

```bash
cd frontend
bun install
bun run build
```

Nginx：

```nginx
server {
    listen 443 ssl http2;
    server_name example.com;

    root /var/www/new-api-web;
    index index.html;

    location ~ ^/(api|v1|v1beta|pg|mj|suno|kling|jimeng)(/|$) {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }

    location /static/ {
        try_files $uri =404;
        expires 30d;
        add_header Cache-Control "public, immutable";
    }
}
```

## 验证

后端：

```bash
curl https://api.example.com/api/status
```

应返回包含 `success` 字段的 JSON。

前端：

1. 访问 `https://web.example.com`
2. 打开浏览器开发者工具，确认 `/api/status` 请求成功；使用 Pages Function 时浏览器里应显示为前端同源地址
3. 登录后台，刷新页面后确认仍保持登录态
4. 在 Playground 发起一次流式请求，确认 SSE 可以持续返回

## 常见问题

### 前端打开后所有接口 404

检查构建前是否设置了正确的 `VITE_REACT_APP_SERVER_URL`。如果前端和后端不是同源部署，必须设置：

```bash
VITE_REACT_APP_SERVER_URL=https://api.example.com bun run build
```

如果使用 Cloudflare Pages Functions 同源反代，则不要设置 `VITE_REACT_APP_SERVER_URL`，应检查 Cloudflare Pages 环境变量 `BACKEND_ORIGIN` 是否已设置为真实后端地址。

### 登录成功后刷新又变成未登录

优先检查：

1. 前端和后端是否使用同一主域名的子域名
2. 是否都是 HTTPS
3. 后端是否设置了稳定的 `SESSION_SECRET`
4. 浏览器请求是否携带 Cookie
5. 后端响应的 CORS `Access-Control-Allow-Origin` 是否等于前端 origin

### 流式输出中断

确认反向代理关闭了缓冲，并设置了足够长的超时时间：

```nginx
proxy_buffering off;
proxy_read_timeout 3600s;
proxy_send_timeout 3600s;
```

### 直接访问后端根路径返回 404 或跳转

这是预期行为。后端不再托管前端静态资源：

- API 路径继续由后端处理
- 非 API 路径在设置 `FRONTEND_BASE_URL` 时跳转到前端
- 未设置 `FRONTEND_BASE_URL` 时返回 API 风格的 404
