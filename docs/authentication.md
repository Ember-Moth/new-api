# 用户鉴权与登录会话

面板鉴权采用短期 Access Token、HttpOnly Refresh Cookie 与服务端登录会话控制面的组合。面板请求不再依赖 Gin session，也不再要求 `New-Api-User` 请求头。

## 鉴权模型

- Access Token 是有效期 15 分钟的 JWT，只保存在浏览器内存中，通过 `Authorization: Bearer <token>` 发送。
- Refresh Token 是随机不透明值，有效期最长 30 天。浏览器只通过 `HttpOnly`、`SameSite=Strict` Cookie 持有它；服务端仅保存 HMAC 摘要，并在每次刷新时轮换。
- `new_api_has_session` 是 Refresh Cookie 的会话提示，值恒为 `1`，`Path=/`、非 `HttpOnly`，与 Refresh Cookie 同时写入、同时清除、同一过期时间。它只声明"曾签发过 Refresh Cookie"，不含任何凭据，也不参与任何鉴权判定；伪造它唯一的效果是自费一次注定失败的 refresh。它存在的原因是 Refresh Cookie 被 `HttpOnly` 和 `Path=/api/user/auth` 双重限制，`/` 上的页面无法判断自己是否匿名，否则每次冷启动都要发一次注定 401 的 refresh，而该请求还会占用按 IP 计数的 `CriticalRateLimit` 配额。
- 登录会话只存 DragonflyDB，包含设备、IP、登录方式、最后活跃时间、到期时间、刷新摘要与撤销状态。PostgreSQL 不再创建 `user_sessions`。
- 用户的密码、状态、角色或安全因子发生安全相关变化时，`auth_version` 会递增并使旧登录会话失效。订阅带来的分组升降级只刷新授权缓存，不会退出任何登录设备。
- 用户账户与 `auth_version` 仍属于 PostgreSQL 业务数据；登录会话以 DragonflyDB 为唯一权威。会话缺失或缓存不可用会拒绝会话认证，不再回源数据库重建会话。

`SESSION_SECRET` 用于派生 Access Token、Security Proof、Refresh Token 摘要和 AuthFlow 摘要的不同用途密钥。生产环境及多节点部署必须在所有节点配置相同的高强度随机值；更换该值会使现有登录、临时鉴权流程和 Security Proof 全部失效。

## 多节点 DragonflyDB 拓扑

全部应用实例必须共享同一个 PostgreSQL 主库和 DragonflyDB 逻辑数据库，并配置相同的 `SESSION_SECRET`、`CRYPTO_SECRET`。每节点独立缓存的拓扑不再支持跨节点登录会话。

会话 hash 使用剩余有效期作为相对 TTL，读取和刷新不会延长原有会话截止时间。签发操作在同一 Lua 脚本中检查活跃数量、签发窗口并创建会话及计数索引；预读计数不是授权依据。刷新轮换、重放撤销、注销与会话版本推进均通过 Lua 原子执行。旧的短期副本、拒绝标记回填和数据库兜底已移除；`SYNC_FREQUENCY` 不再控制会话存活时间。

缓存键丢失后对应会话要求重新登录。应用实例重启可以继续使用仍保存在 DragonflyDB 中的会话；缓存整体丢失不会从数据库恢复旧登录状态。账号硬删除会擦除会话元数据。

## 浏览器接口

登录成功后，密码登录、2FA、Passkey、OAuth、WeChat 和 Telegram 登录均返回统一数据：

```json
{
  "success": true,
  "data": {
    "access_token": "...",
    "token_type": "Bearer",
    "access_expires_at": 1730000000,
    "user": {},
    "session": {
      "sid": "...",
      "current": true,
      "login_method": "password",
      "ip": "...",
      "user_agent": "...",
      "created_at": 1730000000,
      "last_active_at": 1730000000,
      "expires_at": 1732592000
    }
  }
}
```

会话相关接口：

| 接口 | 鉴权 | 用途 |
| --- | --- | --- |
| `POST /api/user/auth/refresh` | Refresh Cookie；Secure 模式附加 Origin 校验 | 轮换 Refresh Token 并签发新的 Access Token |
| `POST /api/user/auth/logout` | Refresh Cookie；Secure 模式附加 Origin 校验，可同时携带 Bearer | 撤销当前登录会话并清除 Cookie |
| `GET /api/user/sessions` | Bearer | 查看当前鉴权版本的有效登录会话，当前会话优先，最多 100 条 |
| `DELETE /api/user/sessions/:sid` | Bearer | 撤销指定登录会话，包括当前会话 |
| `POST /api/user/sessions/revoke-others` | Bearer | 保留当前会话并撤销其他会话 |

客户端内存中已有会话时，应在 refresh/logout 请求中发送 `X-Auth-Session: <sid>`。Refresh Cookie 与该 SID 不一致时，两个端点都返回 `409 AUTH_SESSION_MISMATCH`，且不会轮换、撤销或清除任何会话；客户端先通过 refresh 清除本标签页的旧 SID、恢复 Cookie 当前对应的会话，再重试 logout。冷启动尚无内存会话时可以省略该请求头。

并发使用同一个 Refresh Token 时，服务端通过确定性轮换恢复同一个后继 Token，多个浏览器标签页不会因丢失“胜者”响应而被迫退出。最近一代 Refresh Token 在短暂容错窗口结束后再次出现会撤销对应会话；无法识别的更早代或随机 Token 只会被拒绝，不会允许攻击者凭猜测踢掉会话。

前端使用 Web Locks 串行化同一浏览器配置文件中的刷新，并通过 BroadcastChannel（不支持时回退到 `storage` 事件）仅同步会话标识和登录/退出事件；Access Token 与 Refresh Token 都不会通过跨标签页消息传递或持久化到 Web Storage。

前端将冷启动状态与登录状态分开管理。网络或服务端临时故障允许后续导航重试 refresh；服务端确认 Refresh Cookie 无效时才进入已完成的匿名状态。内存 SID 与 Cookie SID 不一致时，客户端清除旧内存身份并在不携带旧 SID 的情况下重试一次。

公开页面的冷启动会先读 `new_api_has_session`：提示不存在且内存中没有任何身份时跳过 refresh，直接按匿名渲染，且**不**把这次跳过记为已完成的匿名判定——跳过只是延后，不是服务端结论。会依据鉴权结果做跳转的位置（受保护路由与登录页）不看提示，内存为空时一律回源。因此提示缺失但 Refresh Cookie 有效的用户（该 Cookie 上线前建立的会话，或只清理了 `/` 站点数据的浏览器）会在公开页显示为匿名，并在进入上述任一位置时自动恢复登录态，不需要重新输入密码。提示因服务端撤销而过期时，那次 refresh 返回 401 并在同一响应里清除提示，浪费的请求只发生一次。

## Session 签发限额与保留策略

服务端在所有登录方式的统一 Session 签发出口执行两级账户限制：

- `USER_SESSION_ACTIVE_LIMIT`（默认 `50`）：单用户未过期且状态为 active 的 Session 上限。达到上限时新登录返回 `409 AUTH_SESSION_LIMIT`。
- `USER_SESSION_ISSUANCE_LIMIT`（默认 `100`）和 `USER_SESSION_ISSUANCE_WINDOW_SECONDS`（默认 `86400`）：统计窗口内该用户创建的所有 Session，包含已撤销和旧鉴权版本的记录。达到上限时返回 `429 AUTH_SESSION_ISSUANCE_LIMIT`。
- 这两次计数与插入不加跨数据库锁；极端并发登录可能出现少量超额，但计数失败会拒绝签发，不会降级放行。

升级时已经超过活跃上限的账户不会被自动下线或挤掉旧会话；限制只作用于后续的新 Session 签发。

`USER_SESSION_REVOKED_RETENTION_DAYS`（默认 `7`）限制撤销状态的保留期，且不延长原会话到期时间。签发事件使用独立有序集合，至少保留该配置覆盖的历史窗口，不因会话注销、密码重置或会话键到期而消失。签发窗口仍限制在配置的保留范围内。

活跃数量计入尚未撤销/过期的所有会话，包括旧 `user_auth_version`，设备列表仅显示当前鉴权版本。遇到活跃数上限时可撤销其他会话或通过密码重置撤销全部会话；这些操作不会清空签发窗口计数。

会话及索引通过 TTL 自动回收。控制面每小时读取共享签发计数，超过 `USER_SESSION_HOURLY_ALERT_THRESHOLD`（默认 `5000`）时记录告警；该阈值不会拒绝全站登录。外部签名防重放凭证仍由 PostgreSQL 持久化，失效后清理。

## Refresh/Logout 的 Origin 校验

refresh/logout 的 Origin 防护与 Refresh Cookie 的 Secure 模式绑定：

- 未配置 `SESSION_COOKIE_SECURE` 或显式设为 `false` 时，Refresh Cookie 可用于本地 HTTP，refresh/logout 的 OriginGuard 关闭，并且不得配置 `SESSION_COOKIE_TRUSTED_URL`。这使 `http://localhost` 上不同端口的 Rsbuild/Vite 开发代理可以正常转发请求。该模式仅用于可信的本地开发环境，不应暴露到公网。
- `SESSION_COOKIE_SECURE=true` 时，Refresh Cookie 仅通过 HTTPS 发送，同时启用严格 OriginGuard。`POST /api/user/auth/refresh` 和 `POST /api/user/auth/logout` 会校验浏览器的 `Origin`；缺少 `Origin` 时只接受合法的单一 `Referer` 作为回退。允许来源包括请求自身的精确 Origin，以及 `SESSION_COOKIE_TRUSTED_URL` 中配置的精确 Origin。

Secure 模式的 Origin 校验不信任客户端直接发送的 `X-Forwarded-Proto`。TLS 在反向代理终止时，应将面板的公开 HTTPS Origin 明确写入 `SESSION_COOKIE_TRUSTED_URL`。

`SESSION_COOKIE_TRUSTED_URL` 现在具有明确的新语义：它是 refresh/logout Cookie 端点的可信 Origin 列表，不是 CORS 白名单。配置规则如下：

- 仅在 `SESSION_COOKIE_SECURE=true` 时配置；多个值用英文逗号分隔。
- 每项必须是精确的 HTTPS Origin，例如 `https://panel.example.com` 或 `https://panel.example.com:8443`。
- 不接受通配符、路径、查询参数、用户信息或域名后缀匹配。
- 不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。浏览器使用 `sk-` key 直连 relay 的场景保持不变。

本地 HTTP 开发示例（OriginGuard 关闭）：

```env
SESSION_SECRET=<local-random-value>
SESSION_COOKIE_SECURE=false
# SESSION_COOKIE_TRUSTED_URL 不得设置
```

生产 HTTPS 示例（OriginGuard 开启）：

```env
SESSION_SECRET=<high-entropy-random-value>
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://panel.example.com,https://admin.example.com
```

该开关只控制面板 Refresh Cookie 和 refresh/logout 的 OriginGuard，不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。

## 可信代理与 IP 限流

Gin 默认会信任所有代理提供的客户端 IP 请求头。本项目改为兼顾常见反代拓扑和公网直连安全的三态配置：

- 未配置、空字符串或纯空白的 `TRUSTED_PROXIES` 默认信任 `127.0.0.0/8`、`::1`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16` 和 `fc00::/7`，并输出启动告警。该默认值覆盖同机 Nginx、Docker Compose 和常见内网反代；公网直连地址不在列表中，其伪造的 `X-Forwarded-For` 会被忽略。
- `TRUSTED_PROXIES=none`（大小写不敏感且必须单独使用）启用严格直连模式，不信任任何代理，`ClientIP()` 只使用 TCP 直连地址。
- 其他非空值按英文逗号解析为代理 IP/CIDR，并完全替代默认列表。应填写反向代理自身的地址而不是客户端网段；非法 CIDR、空列表或将 `none` 与其他值混用都会阻止服务启动。

Gin 只在请求的直连来源属于可信代理时解析客户端 IP 请求头，并从转发链右侧向左寻找首个非可信地址。因此常见 Nginx `$proxy_add_x_forwarded_for` 链中的公网客户端地址会阻止更左侧的伪造前缀生效。默认信任私网的残余风险是：能够从同一私网直接访问应用的其他机器或容器仍可伪造这些请求头；需要消除此风险时应使用 `none` 或配置精确代理地址。

DragonflyDB 限流使用原子 Lua 固定窗口，替代旧的近似滑动窗口 List 实现。这是有意的语义变化：窗口边界两侧可分别打满一次，极短时间内通过量最高约为配置值的两倍。例如 `20 次/20 分钟` 在边界可通过约 40 次。帐户级 Session 上限和滑动签发窗口控制共享会话状态增长；如未来需要严格抑制边界突发，需单独迁移为 ZSET 滑动窗口。

用户级模型成功请求限流仍使用原有 DragonflyDB List 近似滑动窗口，但列表时间戳统一写为 UTC。滚动升级期间，旧节点写入的本地时间字符串和新节点写入的 UTC 字符串无法从格式上区分，可能在一个模型限流窗口内临时误放行或误拒绝。所有节点升级完成并经过一个完整窗口后会自然收敛；本次升级不会切换 Key 或主动删除现有列表。

开放注册仍会受 Critical IP 限流保护，但分布式 IP 多账号攻击不能仅靠 IP 限流阻止。公网开放注册的部署应同时启用 Turnstile 和邮箱验证；更强的设备或多维风控需作为独立安全项目设计。

## PAT 调用契约

`User.AccessToken`（面板 PAT）继续支持 `Authorization: Bearer <pat>`，也兼容原有的单值 `Authorization: <pat>`。`New-Api-User` 不再参与鉴权，外部脚本不需要再发送 Bearer 与用户 ID 双请求头。这是有意的调用契约简化；旧 PAT 本身无需重新生成。

PAT 不是浏览器登录会话，不能调用登录会话管理接口，也不能签发绑定具体登录会话的 Security Proof。

## 临时鉴权流程与二次验证

OAuth state、2FA pending、Passkey ceremony、Telegram bind 等临时状态存放在 `auth_flows`。客户端只持有随机 `flow_token`，数据库仅保存 HMAC 摘要；流程具有用途、provider、intent、用户和登录会话绑定，并且只能原子消费一次。OAuth 注册的 affiliate code 也随登录 AuthFlow 保存。

标准 OAuth 绑定回调由 popup 通过同源 `postMessage` 交给 opener；只有 opener 使用自身内存中的 Bearer 调用后端绑定接口。Telegram 绑定先由已登录前端创建绑定 AuthFlow，再让 widget 回调携带路径中的 `flow_token`，回调时会重新确认原登录会话仍有效。Telegram 的已签名 widget assertion 也会登记为一次性凭据，重复回放会被拒绝。

敏感操作使用有效期 5 分钟的 `X-Security-Proof`：

- `channel.key.read`：查看渠道密钥；
- `passkey.register`：注册 Passkey；
- `passkey.delete`：删除 Passkey。

Proof 同时绑定用户、登录会话、用户鉴权版本、会话版本和 scope，不能跨用户、跨会话或跨用途复用。

启用了 2FA 的用户注册 Passkey 时，register begin 与 finish 都必须携带有效的 `passkey.register` Proof；finish 会在消费一次性 AuthFlow 之前重新验证 Proof。未启用 2FA 的首次 Passkey 注册不要求该请求头。

## 部署要求

- 本项目面向新部署，不执行历史 Session/AuthFlow 的迁移或回填。PostgreSQL 不创建 `user_sessions` 和 `auth_flows`。
- 全部实例共享 DragonflyDB 和签名密钥。缓存中会话缺失后需重新登录；应用重启本身不会清除共享会话。
- 外部身份唯一归属、用户鉴权版本、签名消费凭证仍由 PostgreSQL 保存；后者防止缓存丢失后重放已消费的提供方签名。
- `TRUSTED_PROXIES` 可显式配置可信代理地址；生产入口仍需配置对应 HTTPS Origin。
- 客户端遵循 AuthBundle、`flow_token` 和 Security Proof 契约；PAT 不需要 `New-Api-User`。
