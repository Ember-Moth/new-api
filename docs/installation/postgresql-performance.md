# PostgreSQL 性能与索引策略

本文档说明 new-api 当前分支的 PostgreSQL 查询级优化、生产索引执行方式，以及线上排查慢查询的建议流程。

## 策略原则

- 当前分支使用内嵌 SQL 迁移系统，后端启动时自动执行 PostgreSQL 结构迁移、日志分区迁移、汇总回填和性能索引迁移。
- 新部署默认支持把 `logs` 创建为 PostgreSQL 月度分区表；已有普通 `logs` 表会在启动迁移阶段自动转换为分区表。
- 非日志表性能索引使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 自动创建；`logs` 分区父表使用普通 `CREATE INDEX IF NOT EXISTS`。
- 查询优化优先减少无界扫描、修正 PostgreSQL 下不可靠的写法，再按真实慢查询补索引。
- 大表分页仍以现有 `OFFSET` 兼容前端，后续高流量路径应逐步改为游标分页。

## 嵌入式迁移

SQL 迁移文件位于 `model/migrations/`，通过 `go:embed` 编译进后端二进制。后端启动时会创建并维护 `schema_migrations` 表，记录已执行迁移的 `id`、`checksum`、执行时间和耗时。

迁移执行顺序：

- `000_schema_migrations.sql`：创建嵌入式迁移元数据表。
- `001_quota_rollups.sql`：创建 `quota_data` 日/月汇总唯一索引、触发器函数和触发器。
- `002_quota_rollups_backfill.sql`：一次性回填 `quota_data_daily`、`quota_data_monthly`。
- `010_tokens_model_limits_text.sql`：把 `tokens.model_limits` 历史字段修正为 `text`。
- `011_subscription_plans_price_amount_decimal.sql`：把 `subscription_plans.price_amount` 历史字段修正为 `decimal(10,6)`。
- `003_main_performance_indexes.sql`：创建非日志表性能索引。
- `101_log_partitioning.sql`：创建或转换 `logs` 月度分区表。
- `102_log_performance_indexes.sql`：创建日志表性能索引。
- `103_log_partition_maintenance.sql`：创建日志分区维护函数，启动时按配置补齐过去和未来月份分区。

如果已执行迁移的 checksum 与二进制内嵌 SQL 不一致，应用会拒绝继续启动，避免同一个迁移 ID 被静默改写。

性能索引迁移开关默认开启：

```bash
POSTGRES_AUTO_CREATE_PERFORMANCE_INDEXES=true
```

应急情况下可以临时关闭可选性能索引迁移，让应用只执行必需结构迁移：

```bash
POSTGRES_AUTO_CREATE_PERFORMANCE_INDEXES=false
```

关闭后无需额外 SQL 操作；后续重新设为 `true` 时，应用会继续执行尚未记录的内嵌索引迁移。

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
| `auto` | 默认值。新库没有 `logs` 表时创建分区表；已有普通表时先执行 GORM 表结构迁移，再由内嵌 SQL 自动转换为分区表。 |
| `true` | 强制启用分区。已有普通表同样会自动转换为分区表。 |
| `false` | 不主动创建或维护分区；如果表已经是分区表，会跳过 GORM 对 `logs` 的 AutoMigrate。 |

新部署不需要额外操作，应用会创建当前月份、上一个月和未来 3 个月的分区。分区名称形如 `logs_y2026m05`。

已有普通 `logs` 表会由内嵌迁移自动转换。迁移会短暂独占锁定 `logs`，把原表数据复制到新的月度分区表，然后删除临时旧表。正式生产大库应在维护窗口发布新版本。

启用分区后，历史日志清理会先删除完整过期月份分区，剩余不足一个月的区间再按 ID 分批删除。完整分区删除比逐行 `DELETE` 更少产生膨胀，也显著降低 autovacuum 压力。

## 生产索引执行

性能索引由内嵌迁移自动执行，不再提供安装文档里的额外 SQL 脚本。非日志表索引用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`，可以降低对线上写入的影响；`logs` 分区父表索引用普通 `CREATE INDEX IF NOT EXISTS`，这是 PostgreSQL 对分区父表的限制。

如果 `CREATE INDEX CONCURRENTLY` 在构建过程中失败，PostgreSQL 可能留下同名但不可用的 invalid index。排查时先检查：

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

确认是本项目迁移创建失败留下的索引后，使用 `DROP INDEX CONCURRENTLY IF EXISTS index_name;` 删除，再重启后端让内嵌迁移继续执行。

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
- 性能索引纳入内嵌 SQL 迁移，并保留显式开关用于应急跳过可选索引。
- `quota_data` 写入不再经过 Go 进程内聚合，记录用量时直接执行 `INSERT ... ON CONFLICT DO UPDATE`，唯一键为 `(user_id, username, model_name, created_at)`，用于小时级用量数据的原子累加。
- `quota_data` 增加 PostgreSQL 触发器维护的日/月汇总表：`quota_data_daily`、`quota_data_monthly`。应用仍只写小时表，数据库按增量同步汇总表。
- 看板接口按 `default_time` 选择数据源：`hour` 读 `quota_data`，`day` / `week` 读 `quota_data_daily`，`month` 读 `quota_data_monthly`。
- `logs` 支持 PostgreSQL 月度分区，分区表场景下历史日志清理优先 `DROP TABLE` 整月分区。
- 异步任务轮询和订阅维护使用 PostgreSQL `FOR UPDATE SKIP LOCKED`。任务轮询会写入 `polling_at` 短租约，避免 k3s 多副本 worker 重复拉取同一批任务。
- 钱包余额扣减和订阅额度结算使用 PostgreSQL 条件 `UPDATE ... RETURNING`。余额不足、订阅额度不足由数据库原子判定，避免多副本并发请求超扣。

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

## 计费原子结算

钱包扣费不再依赖“先读余额再写回”的应用层判断，而是使用数据库条件更新：

```sql
UPDATE users
SET quota = quota - $amount
WHERE id = $user_id
  AND quota >= $amount
RETURNING quota;
```

订阅预扣、补扣和退款统一走 `amount_used` 原子更新：

```sql
UPDATE user_subscriptions
SET amount_used = amount_used + $delta
WHERE id = $subscription_id
  AND (amount_total = 0 OR amount_used + $delta <= amount_total)
RETURNING amount_total, amount_used;
```

退款方向使用 `GREATEST(amount_used + $delta, 0)`，避免并发退款把已用额度写成负数。预扣记录使用 `ON CONFLICT DO NOTHING` 保持 `request_id` 幂等，避免 PostgreSQL 唯一约束错误导致事务 abort。

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

历史 `quota_data` 会由 `002_quota_rollups_backfill.sql` 在启动迁移阶段自动回填。该迁移会锁定 `quota_data` 写入并重建 `quota_data_daily`、`quota_data_monthly`，正式生产大库应在维护窗口发布包含该迁移的版本。

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
