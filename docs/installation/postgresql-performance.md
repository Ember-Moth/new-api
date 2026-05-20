# PostgreSQL 性能与索引策略

本文档说明 new-api 当前分支的 PostgreSQL 查询级优化、生产索引执行方式，以及线上排查慢查询的建议流程。

## 策略原则

- 当前分支应用启动时默认创建性能索引，适合未正式上线、内部自用或小数据量阶段。
- 新部署默认支持把 `logs` 创建为 PostgreSQL 月度分区表；已有普通 `logs` 表不会在应用启动时被自动重写。
- 生产环境尽量使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 手动创建索引；`logs` 分区父表除外，PostgreSQL 不支持在分区父表上并发建索引。
- 查询优化优先减少无界扫描、修正 PostgreSQL 下不可靠的写法，再按真实慢查询补索引。
- 大表分页仍以现有 `OFFSET` 兼容前端，后续高流量路径应逐步改为游标分页。

## 应用启动索引开关

默认值：

```bash
POSTGRES_AUTO_CREATE_PERFORMANCE_INDEXES=true
```

保持默认值时，后端会在 GORM 表结构迁移后执行普通 `CREATE INDEX IF NOT EXISTS`。这适合当前未正式上线、数据量较小、后端主要服务内部团队的阶段。

正式上线后，如果 `logs`、`top_ups`、`quota_data` 等表已经有大量数据，建议关闭启动时自动建索引：

```bash
POSTGRES_AUTO_CREATE_PERFORMANCE_INDEXES=false
```

关闭后应用只执行 GORM 表结构迁移，不会自动创建性能索引；此时应使用下面的并发索引脚本。

## 日志表分区

`logs` 是最容易增长的大表。当前分支支持 PostgreSQL 原生月度范围分区，分区键为 `created_at`（Unix 秒）。

默认模式：

```bash
POSTGRES_LOG_PARTITIONING=auto
POSTGRES_LOG_PARTITION_MONTHS_BACK=1
POSTGRES_LOG_PARTITION_MONTHS_AHEAD=3
```

`POSTGRES_LOG_PARTITIONING` 可选值：

| 值 | 行为 |
| --- | --- |
| `auto` | 默认值。新库没有 `logs` 表时创建分区表；已有普通表时保持普通表并输出提示。 |
| `true` | 强制要求 `logs` 是分区表。已有普通表会启动失败，提醒先执行迁移脚本。 |
| `false` | 不主动创建或维护分区；如果表已经是分区表，会跳过 GORM 对 `logs` 的 AutoMigrate。 |

新部署不需要额外操作，应用会创建当前月份、上一个月和未来 3 个月的分区。分区名称形如 `logs_y2026m05`。

已有普通 `logs` 表要转为分区表时，先备份数据库，然后执行：

```bash
psql "$SQL_DSN" -v ON_ERROR_STOP=1 -f docs/installation/postgresql-log-partitioning.sql
psql "$SQL_DSN" -v ON_ERROR_STOP=1 -f docs/installation/postgresql-performance-indexes.sql
```

迁移脚本会短暂独占锁定 `logs`，把原表数据复制到新的月度分区表，然后删除临时旧表。正式生产大库应在维护窗口执行，或按业务规模拆成更细的在线迁移流程。

启用分区后，历史日志清理会先删除完整过期月份分区，剩余不足一个月的区间再按 ID 分批删除。完整分区删除比逐行 `DELETE` 更少产生膨胀，也显著降低 autovacuum 压力。

## 生产执行索引

索引脚本位于：

```bash
docs/installation/postgresql-performance-indexes.sql
```

执行前确认：

- 已备份数据库。
- 不要把脚本放在 `BEGIN` / `COMMIT` 事务中执行；脚本内非日志表使用 `CREATE INDEX CONCURRENTLY`。
- 建议在低峰期执行。
- 云数据库账号需要有创建索引权限。

执行示例：

```bash
psql "$SQL_DSN" -v ON_ERROR_STOP=1 -f docs/installation/postgresql-performance-indexes.sql
```

脚本设置了较短 `lock_timeout`。如果遇到锁等待失败，说明当时有冲突事务，低峰期重跑即可；`IF NOT EXISTS` 保证重复执行是安全的。`logs` 相关索引会使用普通 `CREATE INDEX`，这是为了兼容分区父表；大日志库应优先在维护窗口执行。

如果 `CREATE INDEX CONCURRENTLY` 在构建过程中失败，PostgreSQL 可能留下同名但不可用的 invalid index。重跑前先检查：

```sql
SELECT c.relname AS index_name,
       t.relname AS table_name,
       i.indisvalid,
       i.indisready
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
WHERE NOT i.indisvalid
  AND t.relname IN ('logs', 'top_ups', 'quota_data', 'tasks', 'user_subscriptions', 'abilities');
```

确认是本脚本创建失败留下的索引后，使用 `DROP INDEX CONCURRENTLY IF EXISTS index_name;` 删除，再重新执行脚本。

执行后确认索引状态：

```sql
SELECT schemaname, tablename, indexname
FROM pg_indexes
WHERE tablename IN ('logs', 'top_ups', 'quota_data', 'tasks', 'user_subscriptions', 'abilities')
ORDER BY tablename, indexname;
```

## 本轮查询级优化

- 用户日志和充值搜索的总数统计改为“先取有限 ID 子查询，再统计子查询行数”，避免 `LIMIT` 在 PostgreSQL `COUNT` 上失效导致全表计数。
- 旧日志清理改为先按 `created_at, id` 分批取 ID，再 `DELETE WHERE id IN (...)`，避免依赖 PostgreSQL 不直接支持的 `DELETE ... LIMIT`。
- 日志统计聚合使用 `COALESCE(sum(...), 0)`，避免空结果返回 `NULL`。
- 性能索引从启动流程中抽离到生产脚本，并保留开发环境显式开关。
- `quota_data` 写入不再经过 Go 进程内聚合，记录用量时直接执行 `INSERT ... ON CONFLICT DO UPDATE`，唯一键为 `(user_id, username, model_name, created_at)`，用于小时级用量数据的原子累加。
- `quota_data` 增加 PostgreSQL 触发器维护的日/月汇总表：`quota_data_daily`、`quota_data_monthly`。应用仍只写小时表，数据库按增量同步汇总表。
- 看板接口按 `default_time` 选择数据源：`hour` 读 `quota_data`，`day` / `week` 读 `quota_data_daily`，`month` 读 `quota_data_monthly`。
- `logs` 支持 PostgreSQL 月度分区，分区表场景下历史日志清理优先 `DROP TABLE` 整月分区。
- 异步任务轮询和订阅维护使用 PostgreSQL `FOR UPDATE SKIP LOCKED`。任务轮询会写入 `polling_at` 短租约，避免 k3s 多副本 worker 重复拉取同一批任务。

## Worker 并发领取

异步任务轮询不再使用普通 `SELECT ... LIMIT` 直接扫描未完成任务，而是在事务中执行：

```sql
SELECT ...
FOR UPDATE SKIP LOCKED
```

被选中的 `tasks` / `midjourneys` 行会立刻写入 `polling_at`。其他 Pod 在租约未过期前不会领取同一行。处理完成后会释放租约；如果进程崩溃或上游请求异常，租约到期后会自动重新领取。

默认租约：

```bash
TASK_POLLING_LEASE_SECONDS=120
```

订阅过期和订阅额度重置在单个数据库事务中用 `FOR UPDATE SKIP LOCKED` 领取并处理，不需要额外分布式锁。这样拆出 `async-worker` 或部署多个 worker 副本时，数据库会成为并发协调点。

## 用量汇总表

新增两张用量汇总表：

| 表 | 粒度 | 写入方式 |
| --- | --- | --- |
| `quota_data` | 小时 | 应用直接 `ON CONFLICT DO UPDATE` |
| `quota_data_daily` | 日 | PostgreSQL trigger 从小时表增量同步 |
| `quota_data_monthly` | 月 | PostgreSQL trigger 从小时表增量同步 |

触发器由应用迁移自动创建：

```sql
sync_quota_data_rollups()
trg_quota_data_rollups
```

已有历史 `quota_data` 需要回填日/月汇总表时，在维护窗口执行：

```bash
psql "$SQL_DSN" -v ON_ERROR_STOP=1 -f docs/installation/postgresql-quota-rollups-backfill.sql
```

该脚本会锁定 `quota_data` 写入并重建 `quota_data_daily`、`quota_data_monthly`，适合正式上线前或维护窗口执行。未回填时，新写入的数据仍会自动进入汇总表；看板查询如果汇总表为空，会回退到小时表，避免新旧环境直接空白。

## 慢查询排查

建议生产库开启 `pg_stat_statements`，按总耗时和平均耗时定位 SQL：

```sql
SELECT calls,
       round(total_exec_time::numeric, 2) AS total_ms,
       round(mean_exec_time::numeric, 2) AS mean_ms,
       rows,
       query
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY total_exec_time DESC
LIMIT 20;
```

对高频慢查询使用：

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT ...
```

重点看：

- 是否出现大范围 `Seq Scan`。
- `Rows Removed by Filter` 是否很高。
- 排序是否出现外部磁盘排序。
- `BUFFERS` 是否显示大量 shared read。

## 索引取舍

基础脚本覆盖以下高频路径：

- 渠道能力查找：`abilities` 按分组、模型、启用状态和权重筛选。
- 任务列表和超时任务扫描：`tasks` 按用户、提交时间、未完成状态扫描。
- 任务 worker 领取：`tasks` / `midjourneys` 按 `polling_at`、状态和提交时间扫描，用于 `SKIP LOCKED` 多副本并发领取。
- 充值记录：`top_ups` 按用户、创建时间、ID 分页。
- 用量数据：`quota_data` 按用户、用户名、模型和时间范围聚合，并通过唯一键支持 PostgreSQL 原生 UPSERT。
- 用量汇总：`quota_data_daily`、`quota_data_monthly` 承接中长期看板查询，减少对小时表的重复聚合。
- 订阅任务：`user_subscriptions` 按状态、到期时间、重置时间扫描。
- 日志列表、统计和清理：`logs` 按用户、类型、创建时间和 ID 扫描。
- 日志分区：`logs` 按 `created_at` 月度分区，时间范围查询可以利用 PostgreSQL 分区裁剪。

脚本中保留了 `pg_trgm` 模糊搜索索引示例，但默认注释。只有在后台经常使用包含搜索，例如订单号、用户名、模型名、令牌名模糊搜索，并且 `pg_stat_statements` 证明这类查询占用明显时，再启用这些 GIN 索引。它们会增加写入成本和磁盘占用。

## 连接池建议

连接数按所有后端副本共同预算，不要只看单个 Pod。

计算方式：

```text
总连接需求 = 后端副本数 * SQL_MAX_OPEN_CONNS
```

如果单独设置 `LOG_SQL_DSN` 且指向另一套 PostgreSQL，也要分别计算日志库连接数。如果主库和日志库指向同一个实例，应把两部分连接数合并计入数据库上限。

建议起点：

| 场景 | `SQL_MAX_OPEN_CONNS` | `SQL_MAX_IDLE_CONNS` |
| --- | --- | --- |
| 单机或小实例 | `30` - `50` | `10` - `20` |
| 中等云数据库 | `100` - `200` | `25` - `50` |
| 高并发多副本 | 按连接池代理和数据库上限计算 | 不超过 open 的 25% |

k3s 默认清单使用 `SQL_MAX_OPEN_CONNS=200`、`SQL_MAX_IDLE_CONNS=50`。如果数据库最大连接数较低，优先降低该值或引入连接池代理，例如 PgBouncer。

## 后续优化方向

- 将日志、任务、充值记录等深分页路径逐步改为基于 `id` 或时间游标的分页。
- 对超大 `logs` 表继续做冷热归档，或把旧分区转移到低成本存储。
- 对账单、用量看板等聚合路径评估物化汇总表。
- 对用量汇总表增加按租户、分组或模型维度的更高阶汇总，进一步降低后台统计查询成本。
- 在引入 `pgxpool + sqlc` 前，先用真实慢查询数据确定是否值得重写数据访问层。
