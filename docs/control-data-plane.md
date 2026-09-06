# 控制面与数据面拆分

## 目标与实施顺序

1. 两种启动角色：控制面管理 API、CRUD、前端；数据面转发及关联协议接口。后台维护由控制面启动。
2. DragonflyDB 接管可重建的共享运行状态：节点心跳、执行租约、短期流程状态。PostgreSQL 保留必须持久化的业务事实及任务记录。
3. 配置版本发布、更新通知、数据面只读快照、漏通知后的补偿核对。
4. 统一后台任务恢复和幂等结算，减少数据面数据库访问；不能仅以缓存扣减加不可靠异步写入代替持久化账务。

## 第一批：应用角色和部署入口

`APP_ROLE=control`（默认）与 `APP_ROLE=data` 是互斥角色。`NODE_TYPE` 已移除，仍设置会在数据库连接前报错。

- 控制面注册管理、账单查询和前端，执行 PostgreSQL/ClickHouse 迁移、授权初始化和后台维护。插件路由仍编译验证，但不会在控制面监听器上执行转发。
- 数据面注册模型、聊天、Responses、Gemini、视频、Midjourney、任务及插件协议接口；没有管理路由或前端兜底；不执行迁移、渠道余额刷新、订阅维护或系统任务调度。
- 所有实例保持自己的请求计量刷新、配置读取、渠道缓存同步和节点报告。
- `/healthz` 在应用资源初始化完成、HTTP 监听可用后返回 200；不是持续探测所有下游依赖的就绪检查。
- 两个角色仍共用可执行文件和部分业务装配。数据面 PostgreSQL 访问、PostgreSQL 节点/租约存储及周期性配置同步将在后续批次处理。

两个 Compose 示例都提供入口 3000，直接监听仅绑定本机 3001/3002。Nginx 保留 SSE、WebSocket 和长请求；自定义插件路径需要显式配置分流。多机部署保持 DSN 和签名/加密密钥相同，节点名称唯一。

## 验证

实际服务版本：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。

执行并通过：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' GOWORK=off make test
GOWORK=off go vet ./...
GOWORK=off go run /tmp/new-api-validate-compose.go docker-compose.yml docker-compose.dev.yml
python3 /tmp/verify-new-api-planes.py
```

- 根模块与 RelayKit 完整测试通过；路由回归覆盖控制面不接受转发 POST、数据面不接受管理 API/前端，以及两端健康接口。
- 启动验证脚本创建独立 PostgreSQL/ClickHouse 数据库，并在确认 DragonflyDB 测试库 14 为空后使用它，最终仅清理这些测试资源。两个角色共同启动、正常关闭各两轮，验证业务余额与 ClickHouse 日志保留、迁移版本一致、数据面不执行迁移/维护、控制面退出后数据面监听继续可用。
- 非法 APP_ROLE 和旧 NODE_TYPE 配置在迁移之前被拒绝。
- Compose 文件通过 YAML 解析与基础存储依赖检查。当前环境没有 Docker/Nginx；**没有执行容器启动、入口分流或 SSE/WebSocket 的 Nginx 集成验证**。这部分仍需要在具备容器运行时的环境验证。

本地验证输出：`/tmp/new-api-plane-build.log`、`/tmp/new-api-plane-tests.log`、`/tmp/new-api-plane-vet.log`、`/tmp/new-api-plane-startup.log`。临时验证脚本是本次执行记录，长期路由契约由仓库内 `TestApplicationPlaneRouteIsolation` 维护。


## 第二批：DragonflyDB 节点心跳

节点状态注册器只依赖注入的 DragonflyDB 客户端，不再依赖 GORM/PostgreSQL。每个节点独立键、24 小时 TTL；上报周期 30 秒、离线阈值 90 秒。列表容忍 SCAN 重复以及读取时过期；删除通过 Lua 原子检查最后心跳，避免删掉刚恢复的节点。PostgreSQL 初始 SQL 中的 `system_instances` 表与索引已移除，生产启动强制配置 `REDIS_CONN_STRING`。

原节点报告、资源更新、重复上报、90 秒边界、恢复后禁止删除、批量清理、hostname 回退与取消退出回归迁到 `e2e/dragonfly_test.go` 的真实 DragonflyDB 测试，并覆盖 TTL 和服务端过期。注册器测试不注入 PostgreSQL，验证节点能力不依赖业务库。

验证继续使用 PostgreSQL 18.6、ClickHouse 26.9.1.762、DragonflyDB df-v1.40.2 及上文 DSN：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
# 使用上文 TEST_POSTGRES_DSN / TEST_CLICKHOUSE_DSN / TEST_DRAGONFLY_DSN
GOWORK=off go test ./e2e ./internal/module/system/... ./internal/migration/... -count=1
GOWORK=off make test
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

两角色再次在新建数据库上各启动两轮，确认 PostgreSQL 无节点状态表、DragonflyDB 上报与 TTL 正常，缺少 DragonflyDB 配置在迁移前被拒绝，业务数据和 ClickHouse 日志重启保留。日志位于 `/tmp/new-api-instance-{tests,full-tests,build,vet,startup}.log`。

下一批继续迁移执行租约和短期认证流程；配置版本发布与可靠结算尚未完成。

## 第三批：系统任务执行租约迁入 DragonflyDB

系统任务按类型使用 DragonflyDB TTL 租约，领取使用 SET NX，续期和释放使用 Lua 校验任务与执行者。每次调度尝试生成独立执行者标识。PostgreSQL 初始 schema 移除 `system_task_locks` 表及索引；任务请求、执行历史、完成结果与执行者标识仍作为业务事实持久化。

任务进度/完成提交与过期恢复持有同一任务行锁，并验证 DragonflyDB 租约；过期或外来执行者不能覆盖结果。SQL 领取失败会尝试释放自身缓存租约，清理失败则等待 TTL 到期。缓存故障会阻止领取、续期、提交以及过期判定，不会回退到无锁执行。失联任务目前保留明确失败记录；安全自动重试及外部副作用幂等仍属第四阶段，不能把这一批视为全部后台任务的 exactly-once 保证。

实际验证使用 PostgreSQL 18.6 (Homebrew)、ClickHouse 26.9.1.762、DragonflyDB df-v1.40.2，DSN 同上文：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

真实 DragonflyDB + PostgreSQL 回归覆盖并发领取唯一执行者、续期、外来释放不影响持有者、TTL 到期、旧执行者更新/完成被拒绝、过期恢复解除活动任务限制、旧租约释放不影响新任务、缓存不可用时禁止提交。PostgreSQL 事务回归覆盖领取写入失败后的缓存租约释放。新库两角色两轮启动确认不再创建节点或租约表，业务数据、任务主表及 ClickHouse 日志初始化正常。

输出记录：`/tmp/new-api-lease-{tests,dragonfly,regression,full-tests,build,vet,startup}.log`。独立 Nginx/容器入口验证仍待补；短期认证状态、配置发布、计费可靠交付及其他维护任务统一调度仍未完成。

## 第四批：邮箱验证码与密码重置码共享

原进程内验证码 map 已移除。邮箱验证和密码重置挑战仅存 DragonflyDB，键和值使用带域区分的 HMAC，缓存不存邮箱或验证码明文；TTL 跟随 `VerificationValidMinutes`（默认 10 分钟）。Lua 原子校验并删除匹配验证码，并发请求只有一个成功；错误码、错误邮箱、错误用途不会消耗正确挑战。重新发送会替换旧码。

密码重置不再在数据库更新后按邮箱删除缓存，避免误删并发新签发的验证码。成功验证会立即消费挑战，之后业务数据库操作失败需要重新获取验证码，不重新启用已消费的秘密。DragonflyDB 写入失败时不发送无法验证的邮件；密码重置邮件接口继续对存在和不存在的账号返回相同响应。

实际服务版本：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。以下命令通过（环境 DSN 与前文相同）：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
# 设置前文三个 TEST_*_DSN 后执行
GOWORK=off go test ./e2e ./internal/transport/http/controller ./internal/shared/common -count=1
GOWORK=off go test ./e2e -run 'TestDragonflyCacheContracts/verification' -count=1
GOWORK=off go vet ./internal/shared/common ./internal/transport/http/controller ./e2e
```

真实 DragonflyDB 回归覆盖另一客户端验证、并发单次消费、用途/邮箱绑定、错误码重试、替换旧码、服务端到期和缓存故障。真实 PostgreSQL + HTTP 密码重置回归验证新密码生效、认证版本推进，重放同一码被拒绝且密码/认证版本不再变化。此批没有 schema/启动迁移修改；没有重复执行新库启动验证。

输出：`/tmp/new-api-verification-{build,tests,reset,vet}.log`。OAuth、Passkey、Telegram 和两步登录的短期流程仍有 PostgreSQL 事务耦合，下一步继续迁移；会话状态、配置发布及可靠结算仍未完成。

## 第五批：OAuth、Passkey 与两步登录挑战迁入 DragonflyDB

`ceremony.Flows` 显式接收缓存客户端，短期流程不再读写 PostgreSQL。初始 schema 移除 `auth_flows` 表及索引，流程实体移除 GORM 映射。挑战使用 HMAC 键、Redis hash 和相对 TTL；Lua 原子检查用途、提供方、意图、用户和会话绑定，并消费挑战。payload 作为原始 JSON 字符串返回，不经过 Lua JSON 数值转换；消费后删除 payload，仅保留到期前的已消费标记。相对 TTL 避免应用与缓存主机时钟偏差延长过期时间。

挑战消费发生在业务事务之前。业务事务失败或提交结果不确定时，不会恢复已消费挑战，客户端需重新发起认证。账号硬删除成功后清理该用户的待用挑战；删除被认证版本保护阻止时，挑战保留。用户身份与会话有效性仍由现有业务校验约束。

**防重放凭证属于持久化安全事实。** 外部签名断言消费独立写入 `auth_assertion_receipts`，含哈希、用途、失效时间和消费时间，不含挑战/会话内容。绑定操作与凭证消费使用同一 PostgreSQL 事务，业务回滚时凭证回滚；挑战本身仍保持已消费。签名失效后清理凭证。真实缓存清空测试证明已使用签名不会因缓存丢失再次被接受。

验证服务版本：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。执行并通过：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

根模块与 RelayKit 完整测试通过。既有 OAuth、Telegram、Passkey 注册/登录/删除、2FA 与账号删除回归保留。真实 DragonflyDB + PostgreSQL 集成覆盖绑定字段不匹配、不消费错误挑战、跨客户端并发单次消费、原始 payload 保真、过期、业务回滚不重启挑战、用户挑战清理、缓存故障及缓存清空后的签名防重放。新数据库两角色各启动两轮，确认无 `auth_flows`、`system_instances`、`system_task_locks`，业务数据、ClickHouse 日志及签名消费凭证重启保留。

输出记录：`/tmp/new-api-flow-{full-tests,build,vet,startup}.log`。早期 `flow-tests`、`flow-regression`、`flow-dragonfly` 输出包含迁移过程中的失败；最终结论以上述完整验证结果为准。

下一步：登录会话及其运行限制迁移；随后配置发布/补偿同步、后台任务可靠恢复和计费可靠结算。独立容器入口验证仍未执行，整体目标尚未完成。

## 第六批：登录会话与签发限制迁入 DragonflyDB

登录会话改为 DragonflyDB 权威 hash；移除 PostgreSQL `user_sessions` 及索引、GORM 映射、数据库回源、短期快照回填和分批 Session SQL 清理。记录包含服务端 HMAC 刷新摘要，不保存刷新令牌明文，API 序列化仍隐藏摘要。读取/刷新不延长原会话寿命，使用相对 TTL，缺失/故障时拒绝认证，不从 PostgreSQL 恢复旧登录状态。

签发在一个 Lua 脚本内检查活跃上限与滑动签发窗口，同时创建会话及索引，消除“先计数后插入”的竞争窗口。签发事件独立保留在有序集合中，撤销、密码重置、会话到期不清空历史；账户硬删除才擦除该账号会话元数据。刷新摘要轮换、并发刷新识别、已知旧令牌超出宽限后的撤销、凭刷新秘密注销、会话安全版本 CAS 均采用 Lua。版本比较保留整数精度，覆盖超过 JavaScript 安全整数范围的值。

账户与 `auth_version` 仍是 PostgreSQL 业务事实。设备列表查询当前账户版本后过滤共享会话，并保留当前设备、限制最大返回数量。缓存整体丢失要求重新登录；应用实例重启继续使用仍存在的共享会话。

实际验证：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。使用前文三个 TEST_*_DSN 执行：

```sh
GOWORK=off make test
GOWORK=off go test ./e2e ./internal/module/identity/internal/sessions -count=1
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

完整根模块和 RelayKit 测试通过；最终会话保留窗口调整后补跑真实缓存及会话回归。真实 DragonflyDB 验证多客户端并发签发上限、轮换、宽限与重放、撤销、缓存故障、密码重置保留签发历史。既有身份管理、2FA、Passkey、注销所有权、密码变更及账号删除回归改用会话 API 读取结果，保留行为保护；与旧 PostgreSQL 缓存回填和 SQL 索引相关的测试已移除。

新库两角色各启动两轮，确认无 `user_sessions`/`auth_flows`/节点状态/租约表。验证通过控制面实际注册、登录，重启应用后原 Access Token 仍能访问自己的资料；同时保留业务数据、签名消费凭证和 ClickHouse 日志。

输出：`/tmp/new-api-session-{full-tests,final-regression,build,vet,startup}.log`。其余早期 session 测试输出包含迭代中的失败，不作为最终验证结论。

后续继续审计剩余运行状态，推进配置版本发布与补偿同步，随后处理后台任务恢复和计费可靠结算。容器入口集成验证仍待补，整体目标未完成。

## 第七批：系统配置快照发布与补偿同步

系统 options 由控制面持久化后发布到 DragonflyDB。共享基础设施 `internal/infra/configsync` 提供内容 SHA-256 版本、原子快照/通知发布、完整性检查与通知订阅。通知只携带版本，消费者重新读取当前完整快照；漏通知/断线通过 `SYNC_FREQUENCY` 周期核对补偿。控制面定期从业务库重建快照，缓存丢失后可恢复，不增加 PostgreSQL 缓存或版本状态表。

配置写入和发布期间的源读取使用同一个按 schema 区分的 PostgreSQL advisory transaction lock；发布在源锁释放前完成，并限制缓存发布等待时间，避免迟到的旧源读取覆盖新配置。多控制面写入不同配置键后发布完整合并结果。数据库写入失败不发布；数据库提交后发布失败会报告错误，后续控制面补偿会重新发布已提交值。

数据面 options 初始化和后续同步只依赖 DragonflyDB，拒绝配置写入。启动调整为先连接缓存，再初始化/发布 options；数据面冷启动缺少快照时明确报错。已应用版本写入系统实例 `info.extra.options_version`，便于核对实例是否同步。

这批覆盖 **system options 的传输、版本应用及恢复**。渠道/插件配置和请求级配置读取尚需继续收敛；现有全局设置按各自 setter 应用，不能把这一批当作所有转发设置已实现请求级原子切换。

实际验证版本：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。使用前文 DSN 执行并通过：

```sh
GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

完整根模块/RelayKit 测试通过。真实 DragonflyDB + PostgreSQL 验证只读实例不注入 DB 仍可加载、写入被拒绝、多发布者合并更新、SQL 写入失败不发布、快照损坏拒绝应用、丢失快照恢复、通知和漏通知补偿、取消后 watcher 退出。

新库两角色各启动两轮。数据面使用独立 PostgreSQL 登录角色，明确撤销其 `options` 表的全部权限；仍可从缓存启动，节点上报与发布端版本一致。运行中修改源配置后，数据面版本收敛，期间仍无该表读取权限。原登录会话、业务数据、签名消费凭证及 ClickHouse 日志重启保留。

输出记录：`/tmp/new-api-config-{full-tests,build,vet,startup}.log`。渠道与插件配置、请求级快照、后台任务恢复、可靠计费交付和容器入口验证继续推进；整体目标未完成。
