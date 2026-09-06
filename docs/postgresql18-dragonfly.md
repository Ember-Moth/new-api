# PostgreSQL 18 与 DragonflyDB 基线

主数据库要求 PostgreSQL 18+；启动时读取 `server_version_num`，低版本连接会在迁移前被拒绝。日志库只使用 ClickHouse，`LOG_SQL_DSN` 必填，不能使用 PostgreSQL 或回退到主库。

生产和开发 Compose 使用 PostgreSQL 18、ClickHouse、DragonflyDB v1.40.2。缓存使用 go-redis 的 Redis 兼容协议，连接配置使用 `REDIS_CONN_STRING` 和 `REDIS_POOL_SIZE`。优化按全新部署推进，缓存 key 可随实现需要调整。示例：

```dotenv
SQL_DSN=postgresql://user:password@postgres:5432/new-api
LOG_SQL_DSN=clickhouse://default:password@clickhouse:9000/new_api_logs
REDIS_CONN_STRING=redis://:password@dragonfly:6379/0
REDIS_POOL_SIZE=0
```

DragonflyDB 使用默认原子 Lua 执行方式，部署参数明确设置 `--cache_mode=false`。额度预留、鉴权版本和限流依赖缓存状态，不能为了吞吐开启 `disable-atomicity` 或随意淘汰这些 key。[Dragonfly 配置说明](https://www.dragonflydb.io/docs/managing-dragonfly/flags)

## 全新部署范围

按全新部署设计最终表结构、索引、数据库初始化和缓存 key，不要求兼容历史数据、旧版 schema、旧缓存布局或旧节点滚动升级。测试以空 PostgreSQL 18 数据库、空 DragonflyDB 实例及重复启动为准。

PostgreSQL 18 官方镜像的数据卷挂载到 `/var/lib/postgresql`，默认 `PGDATA` 为 `/var/lib/postgresql/18/docker`。本仓库的两个 Compose 已按这一结构配置。[官方镜像说明](https://hub.docker.com/_/postgres)

## 已落实的优化

已完成版本化 SQL 初始化、原生数组和 JSONB 配置、任务租约原子领取、订阅预扣/退款、缓存脚本复用、批量结算以及日志游标分页。以下对应关系说明每项优化的实现目的与验证契约；性能数据见文末的受控实验。

| 优先级 | 代码切入点 | 已应用的能力 | 验证契约 |
| --- | --- | --- | --- |
| P1 | 数据库初始化和模型定义 | 直接建立 PostgreSQL 18 最终 schema，清理仅用于旧表结构的启动检查和历史迁移链路 | 新库初始化和重复启动正确，业务唯一性与非空约束完整 |
| P1 | 渠道、分组、模型配置 | 列表字段使用 `text[]`；路由关联表与渠道配置在同一事务内更新；结构化配置使用 JSONB | 同步修改 DTO、前端和查询；原样文本及上游协议内容保留其原有语义 |
| P1 | `model/system_task.go` 的租约获取 | PostgreSQL 18 的 `ON CONFLICT ... RETURNING OLD/NEW` 同时返回被接管的旧任务和新租约，省去冲突后的查询 | 同类任务互斥；任务状态与租约在事务内一致；过期任务只结算一次 |
| P1 | `model/utils.go` 的批量更新 | 分批 `UPDATE ... FROM (VALUES ...)`，将逐 ID 更新改为每批更新；按需 `RETURNING` 实际修改行 | 钱包上限、累计溢出保护、负差额、部分失败及幂等性；不能因重试重复扣款 |
| P1 | `model/subscription.go` 的订阅预扣 | 幂等记录使用 `ON CONFLICT`；扣费通过 `RETURNING OLD.amount_used, NEW.amount_used` 获取前后用量 | 订阅到期优先级、额度重置、退款、并发重复 request_id 的一致结果 |
| P1 | `model/quota_reserve.go`、鉴权和限流 Lua | 使用 go-redis `Script.Run` 的 `EVALSHA` 与 `NOSCRIPT` 回退，减少重复发送完整脚本 | 保留全部 `KEYS` 声明和原子性；缓存重启或脚本被清理后仍可工作 |
| P2 | 日志列表和渠道查询 | 游标分页、联合索引、数组 GIN 和名称 trigram 索引；验证 PostgreSQL 18 B-tree skip scan 执行计划 | 时间相同和 request_id 重复时仍可稳定翻页；保持管理员筛选与权限范围 |
| P2 | 数据库扫描和维护 | Compose 配置异步 I/O `io_method=worker` 及 `pg_stat_statements` | 已验证配置生效；异步 I/O 的冷缓存吞吐收益仍需生产负载测量 |
| P2 | DragonflyDB 服务与客户端池 | 默认使用 go-redis 按 CPU 规模配置的连接池；显式正整数可覆盖，0 表示自动 | 保持 Lua 原子性和 TTL；按实际等待与错误率进一步调整线程和连接数 |

`RETURNING OLD/NEW`、异步 I/O 和 skip scan 是 PostgreSQL 18 的新增能力；批量 UPDATE、游标分页和幂等 UPSERT 则是已有能力在本项目里的应用机会。[PostgreSQL 18 发布说明](https://www.postgresql.org/docs/18/release-18.html)

skip scan 的收益取决于索引前导列的不同值数量和筛选条件，不能因此直接删除已有索引。[多列索引说明](https://www.postgresql.org/docs/18/indexes-multicolumn.html)

主键按用途直接选型：内部关联可使用 BIGINT identity；确需跨节点生成且按时间排序的公开资源标识可以采用 UUIDv7。选择依据是访问方式与成本。Session、API key 和签名能力令牌继续使用满足安全要求的随机标识。订阅存在合法重叠，因此有效期不能无条件加 `WITHOUT OVERLAPS`。

本轮已记录批量更新语句数与查询执行计划。生产部署后继续观察锁等待、缓存操作 P95/P99、连接池等待和错误率；这些指标尚无生产样本。Dragonfly 的 `/metrics` 可接入 Prometheus；吞吐收益需要相同数据、请求分布和并发条件下的对比。[Dragonfly 监控说明](https://www.dragonflydb.io/docs/managing-dragonfly/monitoring)

日志只存储在 ClickHouse，按月分区；保留策略由 ClickHouse TTL 控制。内部主键继续使用 BIGINT identity，UUIDv7 用于批次标识和应用生成的 ClickHouse 日志事件标识。

## SQL 迁移组织

`internal/migration/schema/migrate.go` 使用 `golang-migrate/v4` **v4.19.1** 和 `iofs`，SQL 文件通过 `go:embed` 随二进制发布：

- `database/postgres/`：主业务表，版本表为 `schema_migrations`。
- `database/clickhouse_log/`：ClickHouse 日志表，使用独立的日志版本表。

每个序列以 `000001_init_schema.up.sql` / `.down.sql` 为初始结构。后续正式部署后的变更增加递增版本文件。启动只执行 Up；没有待执行版本时正常继续，dirty 状态会使启动失败，不会自动 Force 或猜测修复。

生产启动已移除 `AutoMigrate` 和旧字段/旧索引/旧配置搬迁。SQL 定义结构，GORM 负责映射与数据操作。业务权限等运行时种子继续由相应业务模块维护。ClickHouse TTL 是运行时配置，在结构迁移后同步。

PostgreSQL 迁移使用同一条连接上的 advisory lock，迁移节点应连接直连 PostgreSQL 或保持会话的连接池。ClickHouse 驱动自身的锁仅在进程内有效，因此使用主 PostgreSQL 的事务级 advisory lock 串行化多 master 的日志迁移。迁移结束会释放连接；失败的 SQL 事务会先回滚，避免把未结束的事务放回应用连接池。

初始 SQL 在 `public` 安装 `pg_trgm` 和 `pg_stat_statements` 扩展；迁移账号需要相应建扩展权限，托管数据库可由管理员预装。Compose 已配置 `shared_preload_libraries=pg_stat_statements`；自行部署 PostgreSQL 时也应设置并重启，才能查询统计视图。日志库位于 ClickHouse，不使用 PostgreSQL 扩展。

## 原生数组接口

渠道 `models` / `group`、令牌 `model_limits` / `auto_groups` 使用 PostgreSQL `text[]`。渠道及令牌的公开列表字段以 JSON 数组交互，表单提交与展示已同步。渠道分组筛选使用数组包含运算和 GIN 索引；模型模糊筛选按数组中的单个模型匹配。

`StringList` 分别实现 SQL 数组编码和缓存 JSON 编码，Hash 缓存可读取集合字段。空自动分组仍表示继承默认配置，既有鉴权与限流语义保持。

渠道映射、参数覆盖、设置及预填分组内容使用 JSONB；渠道的 JSON 文本编辑接口保持原有形式。任务原始上游响应 `Task.Data` 保留 `json`，避免 JSONB 的值域和表示规则改变协议内容。[PostgreSQL JSON 类型](https://www.postgresql.org/docs/18/datatype-json.html)

## 日志与缓存

普通日志 API 返回 `items`、`page_size`、`next_cursor` 和 `has_more`，使用上一页/下一页导航，不再为每次翻页执行 COUNT 和 OFFSET。ClickHouse 按 `(created_at, request_id, event_id)` 定位，独立事件标识解决同秒同请求的多条日志排序。游标经过认证加密并绑定查看者和权限，生成游标后才对用户日志重写展示 ID 和隐藏管理字段。

额度、鉴权、Session 和限流脚本使用 `Script.Run`，先执行 EVALSHA，脚本缓存缺失时自动回退。计数和 Hash 修改将 TTL 检查与写入合并为原子 Lua 操作，保持绝对过期时间，也不会重新创建已过期的 Hash。

## 已实施的账务与调度行为

任务租约通过 `INSERT ... ON CONFLICT ... RETURNING OLD/NEW` 获取，过期任务状态和新任务领取在同一事务内提交。领取中途失败会恢复旧租约和旧任务状态。

订阅先用 `ON CONFLICT DO NOTHING` 占用请求幂等记录，再只锁定候选订阅。用量更新通过受容量及 bigint 边界保护的原子 UPDATE 返回前后用量；退款、追加预扣及其幂等记录在同一事务中更新。追加预扣不再使用单独的非幂等退款分支。

批量结算将用户、令牌、渠道增量分别打包为 `UPDATE ... FROM (VALUES ...)`，每段最多 500 行，并在同一事务内写入。批次使用 UUIDv7 标识和数据库回执；COMMIT 回应不确定时重用批次标识可避免重复扣款。失败批次保留在进程内重试，新增增量继续累计；这没有把原有内存累计窗口变成持久队列。已确认成功的批次回执会清理，清理失败不会再次应用增量。

## 验证记录（2026-09-05）

以下是此前双日志引擎阶段的历史验证记录；当前仅支持 ClickHouse 日志，最新验证见模块化重构记录末尾。

真实服务：PostgreSQL **18.6**；用于拒绝旧版本测试的 PostgreSQL **16.15**；DragonflyDB **v1.40.2**（Linux aarch64，2 个 I/O 线程，512 MiB）；ClickHouse **26.9.1.762**。

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_POSTGRES_OLD_DSN='postgres://postgres@127.0.0.1:55432/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' make test

go build -o /tmp/new-api-final-bin ./cmd/new-api
go vet ./...
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
(cd web && bun run typecheck)
(cd web && bun run test)
(cd web && bun run build)
(cd web && bun run i18n:sync)
python3 /tmp/verify-new-api-final-startup.py
```

上述检查通过；前端共 63 个测试文件、417 个测试通过。另在 `web/` 对本轮 21 个变更 TypeScript 文件执行了 `bunx --no-install oxfmt --check <files>` 与 `bunx --no-install oxlint -c .oxlintrc.json <files>`，均通过。DragonflyDB 集成测试覆盖并发余额预留、大整数额度、令牌计费、鉴权版本发布、Session 撤销、Hash 事务与精确过期时间、SCAN 删除范围，以及固定窗口限流的 429/Retry-After。测试要求 `TEST_DRAGONFLY_DSN` 指向专用的空逻辑数据库 15；只在确认其为空后才注册测试数据清理。

完整应用启动验证覆盖：

- PostgreSQL 18 新主库，日志同库，写入数据后重启两次。
- PostgreSQL 18 新主库及独立 PostgreSQL 日志库，写入数据后重启两次。
- PostgreSQL 18 主库与 ClickHouse 日志库，写入数据后重启两次。

应用启动场景均通过，版本为 1、dirty 为 false，写入的 64 位额度保留。自动化数据库测试另行覆盖中文数据、精确订阅价格、索引、唯一性、并发迁移、失败回滚和业务约束。历史版本数据升级不作为验收要求。本次临时输出位于 `/tmp/new-api-final-tests.log`、`/tmp/new-api-all-frontend-tests.log` 和 `/tmp/new-api-final-startup.log`；可长期复用的迁移回归在 `model/database_test.go`。

CI 已配置 PostgreSQL 18、用于负向测试的 PostgreSQL 16、DragonflyDB 和 ClickHouse 服务；本轮执行的是本地真实服务验证，未声称远程 CI 已运行。缓存兼容性回归集中在 `e2e/dragonfly_test.go`。

## 受控性能实验

PostgreSQL 18.6 本地热缓存数据：10 万条日志、1 万个渠道、4 万条路由关联。执行 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`，并核对优化前后返回的记录相同。结果保存在 [测量记录](./performance/postgresql18-measurements.json)。

| 场景 | 原实现 | 当前实现 |
| --- | --- | --- |
| 100 个用户的额度与统计更新 | 200 条 UPDATE | 1 条批量 UPDATE；最终额度、用量和请求数一致 |
| 日志跳过 90,000 条后读取 100 条 | 索引读取 90,100 条，2,187 个缓存块，7.459 ms | 游标读取 100 条，5 个缓存块，0.033 ms |
| 渠道精确分组筛选 | 扫描 10,000 条再过滤 | 数组 GIN 找到 100 个匹配项 |
| 渠道名称包含查询 | 普通 B-tree 无法支持该模式，顺序扫描 | VACUUM ANALYZE 后使用 trigram GIN，19 个缓存块，0.061 ms |
| 路由关联只指定 model | 检查多列主键是否可复用 | 计划显示 9 次索引搜索，返回 20 条匹配项 |

批量 UPDATE 计数不包括事务、幂等回执及清理语句。名称 GIN 在大量插入后、pending list 尚未清理时仍选择顺序扫描（0.679 ms，原实现 0.538 ms）；上表列出正常维护后的计划。分组对照表的行宽不同，适合比较访问路径，不据此推算吞吐倍数。这些数据证明减少了语句或扫描量，不代表生产 P95/P99 或整体吞吐提升；异步 I/O 及 DragonflyDB 的生产容量仍需部署后测量。
