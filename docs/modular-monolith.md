# new-api 模块化单体重构

目标：按业务归属组织代码，显式组装运行时依赖，以模块公开契约协作。完成标准涵盖整个应用，不以目录移动或某个模块完成替代整体完成。

## 目标布局与依赖

```text
cmd/new-api/                 可执行入口与 CLI
internal/app/                应用组装、启动与关闭
internal/config/             部署配置
internal/infra/              数据库、缓存、出站 HTTP 等基础设施
internal/transport/http/     HTTP 服务、路由和公共中间件
internal/transport/task/     后台任务入口
internal/module/
  identity/                 用户、认证、OAuth、会话
  channel/                  渠道、模型能力、路由配置
  gateway/                  协议入口、请求转发、供应商适配
  billing/                  计价、预扣、结算、退款、钱包
  subscription/             套餐与订阅管理
  usage/                    消费日志、用量统计
  system/                   系统设置、节点与任务管理
internal/migration/schema/  版本化 SQL（保留当前序列与位置）
internal/arch/              可执行的依赖边界约束
pkg/                       与应用业务无关的可复用库
relaykit/                  独立构建的协议转换 Go 模块
web/                       Web 应用与静态资源嵌入
```

模块自己的 handler、请求/响应契约、实体和数据访问随业务聚合。模块根包提供业务入口，私有实现逐步收进模块的 `internal/`。模块只接收实际需要的服务和仓储能力；应用组装包不得成为业务模块获取所有依赖的入口。

HTTP 路由与公共中间件属于入站适配层，核心业务以 `context.Context` 和模块契约交互。出站 HTTP、数据库连接等基础设施不得依赖业务服务。通用 `pkg` 不得依赖应用实现。

账务模块边界围绕现有预扣、结算、退款和幂等契约划分。迁移过程中保持既有事务与失败处理语义，不能仅按表名拆分导致一致性退化。PostgreSQL 18、DragonflyDB 与独立 ClickHouse 日志能力继续受真实服务集成测试保护。

## 完成检查

- [x] 可执行入口位于 `cmd/new-api`；应用组装、HTTP 服务和静态资源职责独立，构建/发布入口一致。
- [ ] 部署配置、基础设施和业务配置归属明确；消减 `common` 混合职责与全局运行时依赖。
- [ ] identity 模块聚合认证和用户能力，通过显式契约协作。
- [ ] channel 模块聚合渠道配置、模型能力和路由配置。
- [ ] gateway 模块聚合转发与供应商适配，RelayKit 保持独立。
- [ ] billing/subscription 按业务职责拆分，并验证账务一致性。
- [ ] usage 模块聚合日志与统计，覆盖 PostgreSQL/ClickHouse。
- [ ] system 模块聚合系统配置、节点和调度；后台入口不再依赖 HTTP controller。
- [ ] 根目录 `controller`、`service`、`model` 等横向大包消除；不存在换名后保留全部业务的通用大包。
- [ ] 模块实现、应用组装及基础设施依赖由架构检查约束；公开契约不形成循环。
- [ ] 主应用和 RelayKit 独立构建、静态检查和有效回归测试通过；新库初始化与重复启动通过。
- [ ] 路由、鉴权、计费、日志、插件和 Web 资源契约保持；文档和部署脚本反映最终布局。

## 进度

当前处于实施阶段，整体目标尚未完成。

按用户最新要求，当前优先拆分控制面的 CRUD：先完成管理接口、业务校验和存储的模块归属，再处理转发执行、健康测试等运行时流程。已开始 identity 的 OAuth 提供商配置管理，后续继续套餐、兑换码、用户/令牌和系统管理等控制面功能。整体模块化目标不变。

已完成的第一批改动：

- 程序入口迁移到 `cmd/new-api`；`internal/app` 组装资源并管理服务启动/关闭，HTTP 服务构建与分析脚本注入归 `internal/transport/http/server`。
- 根目录 router、middleware 已迁移到 HTTP 适配层；静态资源由 `web/assets.go` 嵌入，发布、Docker 和 Makefile 入口同步更新。
- 出站 HTTP 客户端、代理连接池、HTTP/2 分片及 SSRF 安全请求迁移到 `internal/infra/httpclient`；原有相关测试随实现迁移。其对历史 common/setting/logger 的配置依赖仍需后续收窄。
- 渠道预填组完成 handler → 模块服务 → 私有仓储 → 自有实体的整条迁移。服务接收显式数据库依赖，没有回调旧 model/controller/service；HTTP JSON 契约独立于 GORM 实体。
- 增加架构依赖检查，阻止模块反向依赖旧业务大包、基础设施依赖模块、业务核心依赖 Gin，以及应用组装被任意业务包导入。

第二批已将模型目录、供应商、缺失模型发现、同步预览及选择性上游同步迁入 channel：

- 模型与供应商的 JSON 契约和持久化实体分离，CRUD、筛选、计数及模型归属查询由私有 Catalog 仓储负责。
- 原 controller 中的模型信息补全改为模块服务，通过 `CatalogPricing` 接口读取端点、分组与计费类型；应用组装提供旧定价缓存的过渡适配器。模块不导入旧 model/service/controller。
- 上游目录的 HTTP 客户端、ETag 与响应缓存归模块实例所有；同步写入、预览差异和选择性覆盖由模块处理，handler 只绑定请求并返回结果。成功同步批次统一刷新定价视图。
- 旧定价代码通过渠道模块接口加载模型/供应商目录并创建默认供应商，不再直接操作这些实体。
- 修正根二进制的 Git 忽略规则，确保 `cmd/new-api/main.go` 可以被纳入版本控制。

第三批已将渠道配置持久化、能力维护、优先级/权重选择、多 Key 状态、内存路由缓存及任务模型别名迁入 channel 的私有 routing 实现：

- 缓存、锁、别名快照和数据库连接由模块运行时实例持有；应用组装让现有调用者与新模块 handler 使用同一实例。
- 定价失效与渠道用量批处理通过两个明确回调协作。定价代码读取配置快照，不再访问渠道缓存的私有锁和 map。
- 渠道实体仅提供数据和配置解析；读取不符合配置类型的 JSON 时仍回退默认配置，但不再隐式写回数据库。该行为有真实数据库回归覆盖。
- PostgreSQL 数组值类型移入共享数据库基础设施，令牌和渠道继续使用同一 SQL/缓存编码。
- 原渠道 model 实现已移走；`model/channel_bridge.go` 暂时为尚未迁移的调用者提供转发和类型别名。这个桥接文件是待删除的迁移工作，不是最终模块边界。

第四批已迁移渠道 HTTP 管理流程：列表与搜索、增删改、复制、标签批处理、状态切换、密钥查看及多 Key 管理。handler 直接持有渠道服务，授权和审计由应用组装注入；新 handler 不再导入旧 model/service/controller，也不再构造数据库查询。分页、标签及类型统计使用模块查询接口，渠道配置校验由模块公开能力负责。

现有权限/审计集成测试继续调用真实模块 handler；敏感字段分类测试已随实现迁移。

第五批已迁移提供商余额查询、模型列表探测与预览、凭证刷新入口、Ollama 管理接口、上游模型更新检测/应用及周期性余额刷新。模块通过 `ProviderRequests` 接收协议请求构造和供应商 SDK 集成，应用层提供 `channelprovider.Adapter`；通知和自动禁用通过明确回调协作。上游巡检的通知抑制状态归模块实例所有，数据库扫描与配置更新归模块仓储。

上游模型巡检改由 `internal/transport/task` 注册并直接调用渠道服务；手动检测的入队与冲突响应由应用层连接现有任务框架。共享 OpenAI 账单响应类型归独立 RelayKit DTO。原 `controller/channel.go`、`channel-billing.go`、`channel_upstream_update.go` 已移除。

第六批按控制面优先顺序，迁移 OAuth 提供商配置的列表、详情、创建、更新、删除与 Discovery 配置读取。identity 模块拥有请求/响应契约、配置校验、实体及私有仓储，注册表更新通过应用层注入。管理响应不包含 client_secret，空密钥更新保留已有密钥，显式空值及禁用状态继续生效；Slug 归一化后检查唯一性和内置提供商冲突，有绑定的提供商拒绝删除，写库失败不会更新注册表。

认证运行时暂时通过只读适配访问模块配置，用户绑定和登录流程留待后续迁移。渠道健康测试、亲和性和转发执行暂后移；identity、gateway、billing、subscription、usage、system、配置及全局状态仍在完整目标内。工作继续在 `main` 上进行，每批验证后提交。

## 第一批验证（2026-09-05）

Go **1.27.1**，PostgreSQL **18.6**，ClickHouse **26.9.1.762**。本批没有修改缓存 Lua/TTL/事务行为，未重新启用 DragonflyDB 集成实例。

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

构建、静态检查与完整后端回归已通过。应用启动验证使用全新数据库，覆盖共享 PostgreSQL 日志、独立 PostgreSQL 日志和 ClickHouse 日志三种配置；各初始化一次并重启两次，验证迁移版本、数据保留、首页 HTML 与静态资源可访问。

本批输出：`/tmp/new-api-modular-full-tests.log`、`/tmp/new-api-modular-stage1-tests.log`、`/tmp/new-api-modular-startup.log`。这些结果只证明已迁移部分及既有行为回归，不能证明剩余业务已经模块化。

## 第二批验证（2026-09-05）

继续使用 Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**，执行同一组构建、vet、完整后端测试与三种配置的新库/重复启动命令。专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/channel/...
```

新增回归集中在渠道模块现有测试文件，覆盖供应商名称冲突和软删除、模型筛选和显式零值、只改状态时保留其他字段、绑定渠道和定价视图回填、同步预览/选定字段覆盖，以及真实 HTTP ETag/304 请求。专项和完整后端回归通过。

输出：`/tmp/new-api-channel-catalog-tests.log`、`/tmp/new-api-modular-catalog-full-tests.log`、`/tmp/new-api-modular-catalog-startup.log`。本批没有调整 schema、计费算术或缓存服务脚本。

## 第三批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**。完整后端测试、build、vet、RelayKit 独立 build/vet，以及三种日志配置的新库初始化和两次重启通过。渠道回归覆盖数据库/内存两条筛选路径、多 Key 状态、保留不归状态更新所有的字段、路由配置快照和读配置不隐式写库。

执行第一批列出的完整验证命令；输出为 `/tmp/new-api-channel-runtime-final-tests.log`、`/tmp/new-api-routing-focused-tests.log`、`/tmp/new-api-channel-runtime-startup.log`。另用 DragonflyDB **v1.40.2** 验证共享数组类型迁移后的真实缓存路径：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1 -v
```

DragonflyDB 结果位于 `/tmp/new-api-channel-runtime-dragonfly.log`。

## 第四批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。专项渠道/权限回归、主模块 build/vet、RelayKit 独立 build/vet 和完整后端回归通过；完整回归命令如下：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
```

构建与启动命令沿用第一批记录。输出为 `/tmp/new-api-channel-management-tests.log`、`/tmp/new-api-management-full-tests.log`、`/tmp/new-api-management-startup.log`。

## 第五批验证（2026-09-05）

使用 Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**，执行第四批的完整后端命令、主模块 build/vet、RelayKit 独立 build/vet/test 和第一批的全新数据库重复启动命令，均通过。

回归覆盖：普通/高级自定义模型列表、请求头覆盖优先级、多 Key 选择、显式清空预览配置、URL/密钥脱敏、失败探测不生成全量删除、更新通知抑制、手动任务去重；新增真实协议适配器和 PostgreSQL 的余额测试，确认未识别的余额响应只返回原始 JSON、不覆盖已有数值。

输出：`/tmp/new-api-channel-providers-tests.log`、`/tmp/new-api-channel-providers-full-tests.log`、`/tmp/new-api-channel-providers-startup.log`。

## 第六批验证（2026-09-05）

使用 Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。执行第四批的完整后端命令、主模块 build/vet、RelayKit 独立 build/vet，以及第一批的新库与重复启动验证。

OAuth CRUD 专项测试使用 SQL 迁移初始化的独立 schema，覆盖密钥隐藏与保留、可选字段清空、Slug/内置名称冲突、访问策略校验、绑定删除保护以及注入写入失败后的数据库/注册表一致性。

输出：`/tmp/new-api-control-crud-tests.log`、`/tmp/new-api-control-crud-full-tests.log`、`/tmp/new-api-control-crud-startup.log`。
