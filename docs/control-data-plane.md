# 控制面与数据面拆分

> 当前开发批次按用户要求暂停验证，未安装或启动数据库、缓存及代理服务。下文早期批次的通过记录不能用于证明最新代码已验收或可上线。

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

## 第八批：渠道路由快照

渠道与对应能力通过单条 PostgreSQL MVCC 查询读取，发布者先取得按 schema 区分的事务 advisory lock，防止迟到发布覆盖后续版本。控制面通过共享 configsync 发布完整渠道快照，数据面只读 DragonflyDB。选路、按 ID 获取配置、模型列表、定价能力投影和任务模型别名在数据面使用本地快照，替换不完整的新视图之前不会清空旧路由。

启动显式检查快照加载结果，移除原 panic 后自动修复 abilities 的分支；后台订阅支持取消并保留周期补偿。路由映射一次性替换，保留现有本地多 Key 轮询位置。本地乐观修改会标记脏状态，使同一源版本也能校正未持久化的本地变更。节点上报 `info.extra.channels_version`。

本批没有把所有渠道写入迁出数据面：额度累计、自动停用及多 Key 运行状态仍需在后续后台/计费路径中收敛。自动状态写入目前仍可能依赖控制面周期发布；请求级配置隔离和插件配置也未完成。

验证使用 PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**，沿用前文三个 TEST_*_DSN：

```sh
GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

完整根模块与 RelayKit 测试通过。真实 DragonflyDB/PostgreSQL 回归覆盖无数据库连接的数据面选路、优先级回退、模型列表/能力、读取密钥时的副本隔离、渠道停用、同版本纠正本地状态，以及源查询失败保留旧快照。

新库双角色各启动两轮。数据面的 PostgreSQL 角色被撤销 options、channels、abilities 权限，仅对 channels 额外授予额度累计所需的 id/used_quota 列权限。运行中创建渠道并发布后，通过真实 API Key 向数据面发送聊天请求，本地测试上游收到正确请求和渠道密钥，返回结果成功；重启后同样通过。该验证针对应用直连端口，尚未覆盖 Nginx/容器入口。

输出：`/tmp/new-api-channel-snapshot-{full-tests,regression,dragonfly,build,vet,startup}.log`。下一批继续插件配置快照，再推进请求级快照、运行状态和可靠结算。

## 第九批：插件配置快照

控制面在事务 advisory lock 内读取活动插件覆盖集合并发布到 DragonflyDB，数据面仅消费共享快照。原有按整批配置编译、原子替换路由运行代、失败保留已有插件的机制保持不变；缓存不可用不清空现有运行代。内容版本与原有数据库语义 revision 分开，节点 `info.extra.plugins_version` 只在完整应用目标配置时上报，部分失败时为空。

应用先配置 HTTP 路由准备器，再同步初始插件快照，随后启动系统任务执行器与节点报告；后台配置订阅可取消，通知丢失由 30 秒周期补偿。控制面管理操作继续同步发布新配置，定期读取可重建丢失快照。

完整测试发现旧模型漂移回归依赖同一个 JavaScript VM 中的可变全局计数，VM 池回收时可能漏测。已改为显式改变最终请求内容，确定性地验证最终解码不能改变已固定的模型。

验证服务：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。沿用前文三个 TEST_*_DSN，以下验证通过：

```sh
GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
```

真实 DragonflyDB/PostgreSQL 回归覆盖无 DB 客户端加载与编译、源码保真、重复配置复用运行代、坏源码保留原插件、快照缺失保留运行代、停用移除覆盖及版本报告。既有插件路由冲突、协议与账务回归通过。

新库双角色两轮启动中，数据面账号被禁止读取 `task_plugins`，仍能在运行中加载新增插件的动态路由，重启后继续可用。该路由在数据面进入正常鉴权拦截，在控制面返回 404；没有执行测试插件的业务任务。原生 OpenAI 请求仍通过受限数据面转发到本地测试上游，业务数据、会话、日志和安全凭证保留。

输出：`/tmp/new-api-plugin-snapshot-{tests,dragonfly,adaptor,full-tests,build,vet,startup}.log`。该批完成插件配置传输和初始化顺序，尚未完成所有运行状态、请求级配置隔离、后台任务恢复、可靠结算以及 Nginx/容器入口验证。

## 第十批：共享多 Key 轮询游标

polling 模式由 DragonflyDB Lua 原子选取下一个可用密钥，游标始终限制在密钥池范围内，跳过已停用密钥。键名包含渠道 ID 和密钥列表 HMAC 指纹，缓存不保存密钥明文；密钥池变化后的新游标不受旧快照请求影响。空闲 24 小时后自动过期。缓存不可用时返回选 Key 错误，不退回本地独立轮询。

移除 `ChannelInfo.MultiKeyPollingIndex`、数据库序列化字段及前端 Zod 默认值。轮询不再触发 SaveChannelInfo，也不再在渠道快照刷新时搬运本地游标。原 per-channel mutex 更名为 key-state lock，继续保护进程内的密钥状态编辑；自动停用状态的共享与写入路径仍待下一批处理。

验证使用 PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**，沿用前文 TEST_*_DSN：

```sh
GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
python3 /tmp/verify-new-api-planes.py
cd web
bun install --frozen-lockfile
bun run typecheck
bunx --no-install oxlint -c .oxlintrc.json src/features/channels/types.ts
bun run test src/features/channels/lib/__tests__
```

完整 Go/RelayKit 测试通过。真实 DragonflyDB 回归覆盖跨客户端连续轮询、并发轮次、跳过禁用项、密钥池切换隔离、TTL、缓存失败及 PostgreSQL JSON 不被轮询修改。新库双角色两轮启动的真实转发使用 polling 密钥池，重启后的下一次请求使用下一枚密钥，且渠道 JSON 没有游标字段。

前端依赖按锁文件安装，类型检查与改动文件 lint 通过；渠道相关 5 个测试文件、14 项测试通过。没有新增界面文案或视觉改动。

输出：`/tmp/new-api-polling-{full-tests,build,vet,startup,typecheck,lint,web-tests}.log`。自动停用状态、后台任务恢复、可靠结算和入口容器验证仍未完成。

## 第十一批：入口代理协议验收

新增可重复运行的 `e2e/verify_proxy.py`，用仓库 `deploy/nginx.conf` 启动临时 Nginx，只替换测试监听地址和两个临时后端地址。验证控制面/数据面路径分流、`/v1/dashboard` 管理例外、SSE 首个事件在上游结束前抵达客户端，以及 WebSocket 升级和双向帧转发。

```sh
python3 e2e/verify_proxy.py
```

在本机 Nginx **1.31.5** 通过。使用临时目录和 loopback 端口，没有启动系统服务或修改现有代理配置。输出 `/tmp/new-api-proxy-verification.log`。这一项补齐真实 Nginx 的协议验收；Docker 镜像/Compose 容器启动仍未验证，不将两者混为一谈。

## 第十二批：维护任务统一调度与退出等待

认证凭证清理、Codex 凭证刷新、订阅维护移入现有系统任务调度器，周期分别为 1 小时、10 分钟、1 分钟。多个控制面通过已有 DragonflyDB 租约竞争执行，同类型活动任务由 PostgreSQL 唯一约束去重；数据面不运行调度。单次回调传播 context 与 I/O 错误，任务记录成功或失败，失败后到下个周期重新调度。删除各自独立的后台循环。

系统 runner 返回完成信号，停止调度后等待已派发 handler 退出；应用先取消并等待 runner，再关闭数据库。验证可取消的阻塞 handler、重复启动/data role 完成信号、回调失败持久化、订阅维护及认证清理取消路径。

本批使用 PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。沿用前文 TEST_*_DSN，相关包测试通过：

```sh
GOWORK=off go test ./internal/module/system/internal/tasks ./internal/transport/task ./internal/module/subscription ./internal/legacy/service
GOWORK=off go test -race ./internal/module/system/internal/tasks -run '^TestStartSystemTaskRunnerWaitsForDispatchedHandlers$' -count=1
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
python3 /tmp/verify-new-api-planes.py
```

新库控制面和数据面各启动两次，真实转发、角色隔离、业务数据/会话保留通过。包内调度测试使用真实 PostgreSQL 和 miniredis，既有 e2e 使用真实 DragonflyDB 验证租约。未把本批等同于所有后台恢复完成：渠道余额同步循环、已超时业务任务的重试策略仍待收敛。

## 第十三批：持久化账务投递与同步余额

用户/Token 的余额变动和预扣始终同步提交 PostgreSQL，缓存只作投影。提交前后失效缓存，取消先修改 Redis 再入进程内队列的路径；不会因缓存更新失败再扣一次数据库，也不会在冷缓存已读到新余额后再 HINCRBY 同一差额。

批量模式中的用户用量、请求数、渠道用量写入 PostgreSQL `quota_batch_deliveries`。这是未完成的持久化业务记账指令，不是缓存或租约状态。worker 以 `FOR UPDATE SKIP LOCKED` 每批领取最多 500 行，在同一事务中合并统计、保存凭证并删除已处理投递；失败保留投递，另一实例可以接手。控制面无论本机是否启用请求批量记账都会启动 drain，数据面仅提交投递。删除进程内待写 map 和 pending batch，渠道回调传播错误且无二次 SQL fallback。

真实 PostgreSQL 回归覆盖新库迁移两次、替换 Store 接手、两个 Store 并发处理 525 行、SQL trigger 故障回滚、批次凭证重放、聚合溢出和直接渠道统计。真实 DragonflyDB 回归覆盖余额预扣、退款、结算、充值、奖励和资料更新；修正旧测试中“预扣后数据库余额不变”的预期。

服务版本：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。以下命令在前文 TEST_*_DSN 下通过：

```sh
GOWORK=off go test ./internal/module/billing/internal/accounting -count=1 -v
GOWORK=off make test
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
python3 /tmp/verify-new-api-planes.py
```

完整根模块与 RelayKit 测试通过；新库双角色各启动两次并完成受限数据面真实转发。日志见 `/tmp/new-api-luna-acceptance-final.log`、`/tmp/new-api-luna-startup.log`。

本批保证已入库投递的事务处理，不代表请求级结算已具备完整幂等性。余额 API 仍未接收稳定请求 ID，调用者在不确定提交后重试可能形成新业务操作；旧用户缓存填充也仍可能短暂覆盖提交后的失效，需要后续版本栅栏。legacy 统计调用失败目前记录错误，尚未把统计、资金和请求结果合并为统一结算事件。

## 第十四批：渠道自动运行状态迁入 DragonflyDB

自动停用/恢复及多 Key 自动状态改用 DragonflyDB hash 与 Lua 原子操作，键按渠道和密钥池 HMAC 隔离；维持既有显式恢复语义，没有额外引入自动冷却时长。控制面和数据面读取同一状态，数据面不读取或写入 PostgreSQL 渠道配置。手动渠道/密钥停用仍是持久化管理策略，并优先于自动恢复。

渠道选择、模型能力和别名、管理列表叠加自动状态。管理编辑会剥离自动 Key 状态，恢复顶层状态和停用原因/时间的配置基线，防止把运行投影保存到 PostgreSQL。密钥轮换清理相应池状态；请求错误与恢复携带选 Key 时捕获的池指纹，旧池结果不能影响新池，包含重叠密钥和 MJ 原任务切换渠道。

真实 DragonflyDB e2e 覆盖两个独立客户端共享停用、全部 Key 失效后不再选路、单 Key 恢复、手动停用优先、PostgreSQL 配置内容不变、旧池请求隔离、缓存不可用时拒绝读取运行状态。路由包回归覆盖自动投影保存、手动停止后保存旧投影、自动原因/时间不落库。

服务版本：PostgreSQL **18.6 (Homebrew)**、ClickHouse **26.9.1.762**、DragonflyDB **df-v1.40.2**。沿用前文 TEST_*_DSN：

```sh
GOWORK=off make test
GOWORK=off go test ./internal/module/channel/... ./internal/legacy/relay ./internal/transport/http/controller ./e2e -count=1
GOWORK=off go test ./internal/transport/http/middleware ./internal/legacy/relay ./e2e -count=1
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
(cd relaykit && GOWORK=off go build ./...)
python3 /tmp/verify-new-api-planes.py
```

完整 Go/RelayKit 测试、最后补丁的定向回归、独立 RelayKit 构建、双角色新库两轮启动及受限数据面转发通过。输出 `/tmp/new-api-luna-{acceptance-final,channel-final,request-fence,build-final,vet,startup-final}.log`。

剩余工作集中在请求级配置一致性、全链路幂等结算/故障恢复、其他后台循环与容器部署验收。当前渠道状态按渠道读取，后续应批量读取并验证大规模渠道列表和选路开销；本批只验收跨实例行为，没有宣称性能提升倍数。

## 第十五批：账务生命周期与请求配置一致性（未验证）

本批先撤下中断的渠道状态批量读取草稿，保留已提交的共享状态实现；修正 Dockerfile、开发镜像与 release workflow 的版本号注入包路径。未完成的批量读取优化不作为本次上线前提。

用户明确要求不安装测试环境、暂不进行验证。本批仅实施代码、格式化和静态审查；新增或调整的回归用例尚未执行，没有运行新的数据库/DragonflyDB/容器验收，也没有将编译或测试视为已通过。

### 持久化账务

新增 `billing_sessions` 记录请求身份、资金来源、预扣、最终金额及终态意图，`billing_adjustment_receipts` 记录独立业务事件。用户钱包或订阅、Token、统计投递与账务凭证在一个 PostgreSQL 事务中提交；同一业务身份的重放不再次记账，参数或归属冲突被拒绝。Token 轮换和软删除不抹掉已经授权的账务事实，Playground 则显式跳过 Token 账本。

结算和退款先保存明确意图，再提交资金事务。通用恢复只重试已知金额的普通请求；依赖任务标记的结算带有 `intent_requires_commit`，必须由对应任务恢复器带事务回调完成，通用恢复器不能跳过该回调。控制面通过已有系统任务租约每分钟执行有界恢复。

任务和 MJ 的初始结算、完成调整、退款具有不同的稳定事件身份。任务标记与资金、Token、统计在同一事务内完成；普通轮询更新不再覆盖账务字段。用户查询保留上游真实状态，并单独显示 `billing_status`。缺少上游身份、网络结果不明、查询失败和本地超时保留预扣，进入待核对状态；只有明确的上游失败才建立退款意图。

异步提交在网络调用前保存 `reconcile` 边界；明确拒绝后才允许正常重试或退款。已经收到的成功任务结果会在有界、独立于客户端取消的上下文中持久化，避免客户断开后丢掉任务并退还已发生的费用。没有获得确定上游结果的记录不会自动猜测最终金额。

Realtime 不再在每个 `response.done` 独立扣费后又按累计量结算。连接内以一个执行循环累计用量，按累计目标追加预留，结束时结算一次；重复事件不再重复累计，流异常结束也保留已观察到的用量。

### 请求配置

选项完整应用后原子发布不可变请求配置快照。请求创建时捕获该版本，后续定价、表达式、分组倍率、工具附加费、违规费用和协议转换均从该快照读取。渠道重试可以更新渠道元数据和所选分组，但仍从原快照取得对应规则。映射、切片及转换回调绑定固定副本，避免回调重新读取热更新全局值。

选项批次失败后保留旧请求快照，并阻止单项更新将半应用的源状态发布为完整版本。异步任务的最终计价使用提交时持久化的计费上下文，不读取管理员后来修改的模型或分组倍率。权限、限流等需要动态生效的控制未被一并冻结。

### 尚未证明的上线条件

本批不提供已通过的构建、测试、数据库兼容或生产可靠性结论。后续恢复验证时，需要在新建 PostgreSQL 18+、DragonflyDB 和 ClickHouse 测试环境执行迁移两次、事务失败/重放/恢复、任务并发、配置切换及真实请求链路验收。前后端独立构建部署和生产环境备份/告警仍是后续工作。

上游未确认的记录需要通过其请求身份、任务身份和提供方记录进行人工核对；稳定内部事件身份不等同于对任意客户端重发请求提供上游幂等性。ClickHouse 日志与 PostgreSQL 的资金事务也不是一个跨库事务，账务凭证是资金是否提交的依据。
