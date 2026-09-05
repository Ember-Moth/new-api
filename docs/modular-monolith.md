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

按用户最新要求，当前优先拆分控制面的 CRUD：先完成管理接口、业务校验和存储的模块归属，再处理转发执行、健康测试等运行时流程。已完成 identity 的 OAuth 提供商配置、令牌和管理员用户管理、subscription 的套餐配置和 billing 的兑换码管理，自助账户管理也已迁入 identity；后续继续安全凭据和系统管理等控制面功能。整体模块化目标不变。

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

第七批将套餐配置列表、创建、完整编辑和启停迁入 subscription 模块。请求/响应契约与 GORM 实体分离；模块服务统一校验价格、额度、分组与重置周期，私有仓储处理显式零值和空值更新。支付合规状态、分组存在性和套餐缓存失效由应用层注入；省略可选支付标志时保留原值，写库失败不会失效缓存。用户列表仍按合规状态和套餐启用状态过滤，路由权限与 JSON 格式保持。

套餐实体、默认值和周期常量归模块所有，尚未迁移的购买、订阅额度和重置流程暂通过类型别名使用；本批没有修改这些事务，也没有添加套餐删除接口。

第八批将兑换码的列表、条件搜索、详情、批量创建、编辑、状态修改、软删除和清理失效码迁入 billing 模块。业务服务持有显式数据库及支付合规依赖；handler 只处理 HTTP、国际化错误和注入的审计，原 `controller/redemption.go` 已移除。创建保留数量/额度/有效期校验和部分成功返回，兑换码与创建者由服务生成和确定。

编辑配置只写名称、额度和过期时间，状态操作只写状态，并用 PostgreSQL `RETURNING` 返回实际写入后的记录，避免把读取之后已完成兑换的状态覆盖回旧值。原筛选测试迁入模块并改用正式 SQL schema；原子充值、溢出回滚和并发兑换仅成功一次的回归保留在 model，兑换钱包事务尚待后续迁移。

第九批将 API 令牌的列表、搜索、详情、密钥查看/批量查看、创建、编辑、状态修改和单个/批量删除迁入 identity。令牌实体及数组配置解析由模块拥有，HTTP 输入/输出与 GORM 实体分离；所有权校验、密钥脱敏、数量/额度约束和自动分组校验由模块服务负责，应用层注入分组策略与缓存失效能力。

自动分组仍区分省略、null 和空数组，非 auto 分组会清空快照并关闭跨组重试。管理操作在写库前调用原有缓存屏障；配置编辑不覆盖状态和用量字段，状态操作只写状态，PostgreSQL `RETURNING` 返回实际记录。注册默认令牌暂经 `model.InsertToken` 适配模块的可信创建能力，鉴权读取、预扣和结算运行时仍留待迁移。

令牌与日志/充值搜索共用的 LIKE 转义和校验迁到数据库查询基础设施；PostgreSQL 和 ClickHouse 的转义路径继续独立。原控制器测试合并迁到 identity，使用正式 SQL 初始化的隔离 schema；原缓存回归改为调用真实模块管理入口。

第十批将用户目录、筛选/排序/分页、管理员创建与编辑、绑定清除、启停、升降权、软删除和硬删除迁入 identity。模块拥有用户实体、管理 DTO、权限规则及 SQL；资料更新与权限写入在同一事务内，锁定当前用户后检查目标角色。密码留空表示保留，状态操作仅更新对应字段，不会用旧快照覆盖账务和个人访问令牌。

认证版本推进、缓存发布、会话撤销和凭据清理通过显式安全运行时端口协作，保留事务前屏障与提交后失效顺序。硬删除在同一事务清理认证数据，降权清除权限并只调用一次会话撤销。创建用户时默认侧栏配置进入初始事务；管理响应显式投影，避免返回密码散列和个人访问令牌。根 model.User 暂保留对模块实体的命名视图及尚未迁移的登录/注册/记账方法。

管理员钱包命令归 billing；加减仍通过现有账务运行时端口，绝对覆盖由模块仓储锁定旧余额后更新，提供准确的审计起始金额。减额度也校验钱包上限，防止越界参数进入记账。缓存、会话、外部身份和权限运行时的过渡端口仍待后续移入对应模块，不能据此认为 identity 或 billing 已完整迁移。

第十一批将自助资料、语言/侧栏偏好、通知设置、个人访问令牌轮换、邀请码读取、邮箱绑定和注销迁入 identity。登录、刷新及 self 共用模块的安全响应契约，认证身份和会话响应类型由模块拥有，尚未迁移的 service 保留类型别名。

改密在锁定用户后核对当前密码与请求认证版本；事务内推进认证版本，提交后发布缓存并推进当前会话，其他会话失效。个人信息更新只写允许的字段，邮箱绑定沿用规范化邮箱的事务级锁与唯一性检查；账户注销保护 root 用户并撤销会话。

设置更新按所属字段合并：通知保存不再清空语言、侧栏或扣费偏好；语言和通知并发更新通过行锁保留彼此结果。非管理员无法覆盖上游模型通知开关，切换通知方式会清除不再使用的凭据。

新部署 schema 的个人访问令牌列改为 `VARCHAR(32)`，与生成器实际输出的 28/32 位长度匹配，避免 `CHAR(32)` 补空格；唯一约束保留。没有添加历史数据兼容路径。

第十二批将权限目录、内置角色、用户权限覆盖、Casbin 存储适配器和策略同步收进 identity。对外入口是 `identity/authz`，具体实现位于模块私有的 `internal/authorization`；旧 `service/authz`、`controller/authz.go` 和 model 中的权限实体已移除。

权限引擎由应用创建并显式注入，替代进程级全局 enforcer。用户管理直接使用同一引擎完成事务内权限写入和提交后的重载，HTTP 中间件及尚未迁移的登录响应通过请求上下文读取它；渠道管理捕获应用实例。周期同步接受生命周期 context，关闭时停止重载。

主节点的角色/基线初始化改为一个事务，失败会保留原有基线和用户覆盖；副本仅加载策略。SQL 集成测试验证两个独立数据库实例的权限互不影响，同库副本在重载后才观察到已提交修改，初始化失败后角色和授权保持完整。权限目录保留现有能力，没有新增角色管理接口。

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

## 第七批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。执行第四批完整后端回归命令、主模块 build/vet、RelayKit 独立 build/vet，以及第一批的新库/两次重启命令，全部通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/subscription/...
```

专项测试以 SQL 迁移初始化隔离 schema 并重复初始化，覆盖套餐默认值、定价精度及 bigint 额度、排序与用户可见性、启停不覆盖配置、显式清空字段、省略支付标志保留 false、校验拒绝和真实写入失败后的缓存失效边界。

输出：`/tmp/new-api-plan-crud-tests.log`、`/tmp/new-api-plan-crud-full-tests.log`、`/tmp/new-api-plan-crud-vet.log`、`/tmp/new-api-plan-crud-startup.log`。

## 第八批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归命令和第一批新库/两次重启命令均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./model \
  -run 'TestModular|TestRedemption|TestSearchRedemptions|TestRedeem' -count=1
```

SQL 迁移在隔离 schema 初始化两次。覆盖：批量生成和创建者归属、唯一约束、搜索状态组合和分页、钱包额度上限、真实写入中断时只返回已提交兑换码、成功审计、软删除保留记录、清理失效码，以及管理读取之后发生兑换时不覆盖已用状态。测试显式初始化国际化资源；原有钱包兑换回归继续通过。

输出：`/tmp/new-api-redemption-crud-tests.log`、`/tmp/new-api-redemption-crud-full-tests.log`、`/tmp/new-api-redemption-crud-vet.log`、`/tmp/new-api-redemption-crud-startup.log`。

## 第九批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。执行第四批完整后端回归、主模块 build/vet、RelayKit 独立 build/vet，以及第一批的三种日志配置新库/两次重启命令，均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./model \
  -run 'TestModular|TestToken|TestProvider|TestQuotaReserve' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

覆盖密钥脱敏和所有权隔离、128 字符密钥与原生数组存储、单个/批量软删除、分组三态、分组策略和数量/额度校验、搜索转义与分页、过期/耗尽启用保护，以及交错写入时保留状态和账务字段。DragonflyDB 集成用正式 SQL schema 初始化两次，验证预热缓存后的分组收紧、禁用、单个/批量删除立即反映在实际鉴权读取上，其他用户令牌不受影响。

输出：`/tmp/new-api-token-crud-tests.log`、`/tmp/new-api-token-crud-dragonfly.log`、`/tmp/new-api-token-crud-full-tests.log`、`/tmp/new-api-token-crud-vet.log`、`/tmp/new-api-token-crud-startup.log`。

## 第十批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归与第一批新库/两次重启命令均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/module/identity/... -run TestUser -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

原用户目录/管理测试合并至 identity 的 SQL 集成测试，覆盖排序后分页、软删除筛选、脱敏、角色边界、权限失败回滚、密码保留/更改、会话只撤销一次、删除失败回滚、认证数据清理、绑定声明释放和钱包上限。真实 DragonflyDB 覆盖预热会话与 API 令牌后禁用立即阻断访问、认证版本只增一次、硬删除使缓存读取失效。

输出：`/tmp/new-api-user-crud-tests.log`、`/tmp/new-api-user-crud-dragonfly.log`、`/tmp/new-api-user-crud-initial-tests.log`、`/tmp/new-api-user-crud-full-tests.log`、`/tmp/new-api-user-crud-vet.log`、`/tmp/new-api-user-crud-startup.log`。

## 第十一批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归，以及第一批三种日志配置的新库初始化和两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./controller ./service ./model \
  -run 'TestModular|TestSelf|TestUser|TestSetupLogin|TestRefresh|TestAdvanceCurrent|TestUpdateUserAccessToken' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

在 SQL 迁移初始化两次的独立 schema 中验证：真实认证中间件下的 self 脱敏和字段白名单、原密码校验、无密码账户保护、过期认证版本拒绝、当前会话续签及其他会话撤销、设置并发合并、通知校验、个人令牌原样读取/轮换/唯一约束、邮箱规范化与冲突、邀请码稳定性和注销。DragonflyDB 覆盖预热缓存后的改密会话转换及通知更新保留额度和其他偏好。

输出：`/tmp/new-api-self-crud-tests.log`、`/tmp/new-api-self-crud-dragonfly.log`、`/tmp/new-api-self-crud-full-tests.log`、`/tmp/new-api-self-crud-vet.log`、`/tmp/new-api-self-crud-startup.log`。

## 第十二批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归和第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./internal/transport/http/... ./controller -count=1
```

权限测试使用正式 SQL 迁移并重复初始化，保留角色基线、用户覆盖、事务回滚和任务插件绑定权限回归，补充实例隔离、跨实例重载和初始化失败回滚。现有用户管理、渠道和请求中间件集成测试已改为显式持有权限实例。

输出：`/tmp/new-api-authz-module-tests.log`、`/tmp/new-api-authz-module-full-tests.log`、`/tmp/new-api-authz-module-vet.log`、`/tmp/new-api-authz-module-startup.log`。
