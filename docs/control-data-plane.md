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
