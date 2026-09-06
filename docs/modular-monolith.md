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
- [x] 根目录 `controller`、`service`、`model` 已清空，完成整包目录迁移。
- [x] `common`、`constant`、`dto`、`types`、`logger`、`setting`、`oauth`、`relay` 已整包移入 internal；RelayKit 保持独立。
- [ ] `internal/legacy` 和 `internal/transport/http/controller` 的过渡实现继续按业务归属拆分，完成最终模块边界。
- [ ] 模块实现、应用组装及基础设施依赖由架构检查约束；公开契约不形成循环。
- [ ] 主应用和 RelayKit 独立构建、静态检查和有效回归测试通过；新库初始化与重复启动通过。
- [ ] 路由、鉴权、计费、日志、插件和 Web 资源契约保持；文档和部署脚本反映最终布局。

## 进度

当前处于实施阶段，整体目标尚未完成。

按用户最新要求，先完成目录迁移，再继续拆分逻辑。剩余 178 个文件保持原有 Go 包和函数实现，controller 整包迁至 internal/transport/http/controller，service/model 迁至 internal/legacy 下的同名包；导入路径与架构检查同步更新。已拆出的业务模块继续保持现有边界。这些目录的迁移已经完成；后续八个包也已整包移动到下表位置。旧包内部和包间的业务耦合仍待后续整理。

当前迁移位置：

| 原目录 | 当前位置 |
|---|---|
| controller/ | internal/transport/http/controller/ |
| service/ | internal/legacy/service/ |
| model/ | internal/legacy/model/ |
| common/ | internal/shared/common/ |
| constant/ | internal/shared/constant/ |
| dto/ | internal/shared/dto/ |
| types/ | internal/shared/types/ |
| logger/ | internal/infra/logger/ |
| setting/ | internal/config/setting/ |
| oauth/ | internal/legacy/oauth/ |
| relay/ | internal/legacy/relay/ |
| relaykit/ | relaykit/（独立 Go 模块，保留） |

下文历史批次中的路径和验证命令记录的是当时状态；当前代码请按上表定位。

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

第十三批将后台任务管理接口、任务/租约存储、调度、抢占执行、进度报告和日志清理迁入 system。公开入口是模块服务，私有实现位于 `internal/tasks`；任务实体与响应归模块所有。原 `model/system_task.go`、`service/system_task.go` 及控制器管理接口已移除。

注册表、启动状态和唤醒通道由服务实例持有。查询和写入携带 context，应用取消会传给执行中的任务和租约心跳。保留 PostgreSQL 18 的原子租约抢占、同类型去重、过期锁处理及旧执行器写入保护。

周期任务适配器迁到 `internal/transport/task`。应用层暂以明确回调连接尚未迁移的渠道健康测试与 Midjourney 执行代码；system 核心通过日志操作端口支持 PostgreSQL/ClickHouse，没有依赖旧 model、service 或 controller。节点报告与系统设置仍待继续迁移。

第十四批将节点状态上报、实例列表和过期实例清理迁入 system。节点身份、版本、启动时间及资源采样通过应用配置注入；存储位于模块私有的 `internal/instances`，HTTP 响应与实体分别定义。原 model/service/controller 的实例管理文件已移除。

重复心跳按 node_name 更新同一记录并保留创建时间，过期删除直接使用数据库时间条件，在线节点和已恢复心跳的节点受到保护。上报循环的启动状态归实例所有，接受应用 context 并返回退出信号，应用关闭时等待该循环结束后再关闭数据库。

第十五批将通用设置接口、配置读取/校验、默认配置加载、数据库持久化与周期重载迁入 system 的私有 options 实现。模块对管理接口保留敏感值过滤、计费默认表达式投影、合规字段保护、提供商配置校验和任务插件表达式校验；应用注入定价缓存失效、任务资源 URL 校验及模型别名解析。

保存使用 PostgreSQL upsert 并检查写入结果，批量写入放在显式事务内；已覆盖的 JSON 倍率校验不再提前修改内存，写库失败不会发布配置。重载先校验已支持的值约束，再应用数据库快照，周期循环接受应用 context。

旧 model/option.go 缩为 setup、支付和插件写入方的过渡适配；common.OptionMap 及 setting 下的业务配置仍是共享运行时投影，并非最终的配置边界。后续要继续将业务消费者归到所属模块，不能将本批视为全局配置整理完成。

第十六批将用户 OAuth 绑定列表、解绑、绑定写入和第三方身份占用记录迁入 identity。列表一次联表读取提供商公开元数据，响应不包含提供商密钥；管理员解绑在锁定目标用户后检查角色，自助操作固定使用当前用户。

绑定创建/替换依靠 PostgreSQL 唯一约束并锁定关联提供商和用户，注册调用继续复用外层事务；提供商删除在同一事务锁定提供商并检查绑定，避免并发删除产生无主绑定。外部身份占用仍支持同一映射重复声明，其他用户或同用户的冲突身份会被拒绝。

原 controller/custom_oauth.go 已移除，model 中的绑定与占用函数仅保留认证运行时的过渡适配。模块内部清除 Telegram 绑定直接操作自有仓储，不再需要应用层释放身份回调；缓存发布和尚未迁移的登录流程保持原有边界。

第十七批将双重验证初始化、启停、状态、备用码重建和管理员重置迁入 identity，TOTP 与备用码实体和存储归模块私有 twofa 实现。model/twofa.go 仅保留登录挑战、安全验证及既有调用方的过渡接口；controller/twofa.go 仅剩登录挑战入口。

待验证密钥和备用码现在在同一事务替换，任一步写入失败都会保留原来的待验证配置。生命周期写入先锁用户再锁验证器，和账户删除保持一致；管理员角色与当前认证版本在变更锁内复核。启停与备用码更新继续原子推进认证版本，提交后发布缓存并续签当前会话，管理员重置撤销全部会话。

既有失败计数、锁定和备用码单次使用逻辑随存储迁入模块；无浏览器会话的请求在消费验证材料之前被拒绝。操作日志通过明确回调连接尚未迁移的日志模块。

第十八批将 Passkey 凭据存储、WebAuthn 适配、注册/登录/升阶验证、解绑与管理员重置迁入 identity。一次性认证挑战的实体与数据库操作同时归入模块，原 model 文件仅保留尚未迁移认证调用方的适配，旧 service/passkey 和 controller/passkey.go 已移除。

注册与删除在锁定用户后复核认证版本或管理员角色，凭据替换与版本推进处于同一事务。断言更新继续只写计数器、验证状态和使用时间，不能替换注册身份。安全证明检查在挑战消费之前，挑战按用途、用户和会话匹配且只能消费一次。

Passkey 登录通过应用注入的完成回调连接剩余登录传输层，并携带验证时的用户认证版本，避免验证后发生安全配置变更仍创建旧状态会话。证明验证与安全审计也由明确接口连接现有中间件；这些认证外围依赖仍须继续整理。

第十九批将访问 JWT、安全证明、登录会话创建/验证/刷新及撤销编排迁入 identity 的私有 authentication 实现，公开边界为 authn。用户与会话共享投影及会话错误契约归模块所有；存储和用户缓存通过明确依赖接口接入。

会话列表、撤销其他会话、单会话撤销、刷新和退出接口迁入模块传输层。保留刷新令牌竞争恢复、重用拒绝、会话绑定及安全证明用途隔离；普通刷新不撤销尚未过期的访问令牌，注销或安全版本变更才会使它们失效。刷新响应使用安全用户投影。

旧 service/auth_token.go 只保留调用适配；service/auth_session.go 保留存储接口适配与 Cookie 输出。底层 model/user_session.go 和用户缓存仍是后续迁移项，这些过渡依赖不能视为身份模块完成。

第二十批将会话创建、查询、刷新轮换、认证版本推进、撤销、清理及缓存脚本迁入 identity 的私有 sessions 实现。认证编排直接持有会话仓储，删除了上一批逐个注入会话操作的适配接口；model/user_session.go 仅保留旧调用方的转发。

保留短缓存窗口、绝对回填截止时间、撤销墓碑、刷新重用检测和分批清理语义。原会话存储测试随实现迁入模块并改用正式 SQL 初始化的隔离 schema；其中用户资料/密码更新相关测试归回既有 user_update 测试，避免依赖错误层级。

会话仓储已不依赖旧 model/service。共享缓存客户端配置、用户缓存和剩余登录传输适配仍待后续消减，整体身份模块尚未完成。

第二十一批将用户安全元数据缓存、认证版本的待提交屏障/已提交下限、缓存发布和分组刷新迁入 identity 的私有 usercache 实现。应用安全操作与认证编排直接使用模块缓存服务；model 中相应代码缩为旧调用方适配，额度增量继续由现有账务路径负责。

用户到缓存投影的纯转换归实体，发布元数据不携带余额覆盖。原缓存回归随实现迁移，继续保护回滚屏障超时恢复、已提交版本下限、延迟安全快照拒绝及同版本分组刷新修复。共享缓存客户端配置和旧调用适配仍未清除，整体目标继续推进。

第二十二批建立 usage 模块，迁入日志实体、角色元数据过滤、加密游标、管理端/用户端查询、统计、令牌日志读取和清理。日志仓储绑定明确的数据库类型，PostgreSQL 与 ClickHouse 的排序、转义和清理路径独立；渠道名称通过 channel 模块提供的接口批量获取。

用户统计固定按用户 ID 隔离，避免将用户名当通配符过滤而扩大范围。PostgreSQL 清理使用有限 ID 子查询，真正限制每次删除的批次；ClickHouse 保持一次同步 mutation 删除全部匹配记录。日志写入也经模块仓储补齐请求 ID，保留调用方已提供的请求 ID。

原 controller/log.go 已移除，model 日志查询与存储函数为剩余调用方提供适配。计费、任务结算和审计事件的组装仍留在原调用路径，后续继续按业务归属收拢。

第二十三批将消费、错误、任务结算、登录、充值和管理审计日志的事件组装迁入 usage。核心写入接收 context 和明确的请求元数据，不再依赖 Gin；用户名、令牌名、IP 记录偏好及汇总输出由业务接口提供。

消费与任务日志继续保留计费参数、请求/上游请求 ID 和发起节点；审计日志归属于操作者，目标用户留在操作参数中。IP 偏好控制仍在模块内执行，关闭消费日志时不写入也不输出汇总事件。汇总事件参数归模块契约，按小时聚合的具体实现留待下一步迁移。

model/log.go 仅保留旧入口适配与业务依赖连接；元数据测试迁至 usage，剩余调用方将随各业务模块迁移继续消减。

第二十四批将小时汇总实体、内存批次、持久化、流量图、时间序列与排行榜底层查询归入 usage，5 个看板 HTTP 接口随之迁移。令牌名称由 identity 批量查询，渠道名称由 channel 提供，核心查询仅接收 context 与身份参数；model 只保留旧事件生产者和排行榜编排所需的适配。

PostgreSQL 初始 SQL 为全部汇总维度增加非空和组合唯一约束；写入用 ON CONFLICT 原子累加，跨实例不会新增重复行或覆盖已有计数。一次快照的所有 SQL 批次在同一事务提交，失败时保留该快照，期间新请求使用独立缓冲，数据库 I/O 不持有写入缓冲锁。定时任务支持取消，HTTP 关闭后等待任务退出并刷新尚未落库的数据；关闭导出开关不会丢弃此前已接收的数据。

汇总依然是内存缓冲的看板统计，不是持久事件队列：进程异常终止可能丢失尚未落库的数据，提交结果因断连而不确定时也没有跨重启去重保证。没有宣称持久化恰好一次投递。排行榜的服务编排和其余业务事件调用方继续随业务模块迁移。

第二十五批将排行榜的周期计算、模型/供应商排名、增长率、升降榜、历史曲线和 5 分钟缓存迁入 usage 的私有 rankings 实现，响应类型归模块契约，HTTP 接口随之迁移。原 service/rankings.go、controller/rankings.go 和 model/usedata_rankings.go 已移除。

排行榜通过模块内聚合仓储查询主 PostgreSQL，缓存绑定服务实例，供应商信息通过应用层的定价目录适配输入。缓存只发布完整成功响应，查询使用请求 context；同用量模型增加名称排序，保证本期/前期名次与升降比较稳定。接口的周期选项、导航访问控制和响应结构保持。

供应商元数据暂时仍来自旧定价目录，定价运行时将在 billing 迁移时接管；日志生产者适配及其他统计能力继续按归属消减，usage 的全部外围依赖尚未清除。

第二十六批将性能统计实体、采样计数、时间桶持久化、保留期清理、分组查询与模型摘要迁入 usage 的私有 performance 实现，两个 HTTP 接口和响应契约随模块移动。原 pkg/perf_metrics 已移除；model 只提供同一采集实例的过渡组装，service 保留 RelayInfo 到采样事件的薄适配，查询模块不依赖转发结构。

同一请求的全部计数在一个临界区更新，落库事务失败后整组恢复，期间仍可接收新样本。查询在 SQL 与内存快照之间协调本实例的刷新，避免数据从内存移至 SQL 时被漏算或重复计算。后台任务由应用持有取消和完成信号，关闭 HTTP 后刷新当前桶；关闭采集开关不丢弃此前已接收的数据。统计小时数仍限制在 30 天，保留期/定时间隔的大整数转换不会溢出为误删除或空转。

原 DragonflyDB 性能指标写入没有活跃读取调用，写入及未使用的读取代码均删除。保留原有“主 PostgreSQL 已落库数据 + 当前实例尚未落库数据”的查询范围，近期样本并未变成跨实例共享缓冲；进程异常退出可能丢失内存样本，提交结果不确定时不提供跨重启的恰好一次投递保证。

性能配置仍通过原配置注册表提供，活跃分组仍通过倍率配置投影输入；这些配置与 RelayInfo 适配将随剩余配置/网关模块迁移消减。

第二十七批将用户订阅实体、有效期/重置时间计算、分配、查询、管理员额度重置、取消、删除和到期降级收进 subscription 的 memberships 实现；7 个管理员接口、请求/结果契约随模块迁移。identity 提供参与调用方事务的用户分组锁定/更新接口，模型层的支付完成、余额购买和预扣调用通过薄适配复用同一订阅能力。

分配、取消、删除和到期处理统一先锁用户再锁订阅；分配次数检查包含已取消订阅，防止并发绕过累计购买上限。到期处理按订阅顺序加锁，只有提交成功才累加完成数，分组读取/更新失败时回滚状态变更。账户软删除或硬删除后仍允许清理残留订阅，分配则要求有效用户。分组缓存刷新发生在提交后，失败不把已提交的分配伪装成失败，也不改变认证版本或已扣减余额。

重置保留“默认推进重置时间、显式 false 保留时间”的接口语义，用户日志与管理员审计继续经应用注入。原重置回归迁入模块；套餐缓存、支付/预扣结算、周期重置任务编排、自助偏好接口及旧审计适配仍待后续归属整理。

第二十八批将套餐/订阅标题缓存、预扣凭据实体、额度预扣/调整/退款/结算、到期额度重置和凭据清理迁入 subscription 的 catalog/quota 实现。原后台重置 service 文件移除，维护任务由模块服务持有实例状态，由应用负责启动、取消和等待；套餐与订阅缓存不再使用模型包的全局单例。

事务中的套餐读取直接使用同一 PostgreSQL 事务，既不读缓存，也不向缓存发布未提交数据。管理员分配和支付完成时复用该读取路径；普通展示查询继续使用 DragonflyDB 或本实例内存缓存。配置名和命名空间保留，缓存容量/TTL 从明确依赖输入，内存缓存不额外启动全局清理协程。

额度操作保留请求幂等、退款原子性、追加预扣可退款及 PostgreSQL numeric 边界保护。自动重置在行锁内重新检查有效期和状态、读取当前套餐，数据库错误返回给维护任务，仅提交成功后计数；关闭重置周期会清理旧的到期时间而不清空已用额度，避免反复处理同一批记录。共享数据库时间读取收进订阅模块内部。

模型层只保留这些能力的旧调用适配；支付订单、余额购买和各支付网关接口仍待迁移，自助订阅偏好与审计适配也尚未清除。完整目标继续推进。

第二十九批将订阅订单实体、创建/完成/失败/过期状态和余额购买迁入 subscription 的 payments 实现，余额购买 HTTP 接口随模块迁移。billing 开始拥有收款记录实体、订阅收款登记和参与调用方事务的钱包扣减；剩余钱包充值入口仍通过原模型视图使用同一实体映射。

订单完成在一笔事务内完成订阅发放、分组变化、收款登记和订单终态，重复回调不再次发放。实际支付方式先更新再登记收款，同时记录支付提供商；同一流水号的其他用户、提供商或钱包充值记录不能被订阅回调覆盖。失败/过期命令仅改变 pending 订单，迟到的结账失败不会覆盖成功回调。数据库故障保留原错误，不再伪装成“订阅订单不存在”而误入钱包充值分支。

余额购买通过 billing 的锁定扣款接口与订阅、订单一起提交，保留向上取整和钱包额度上限，额外拒绝非有限价格/额度单位。只有提交后才扣减缓存、刷新分组和记录日志，提交后的请求取消不取消日志写入，缓存刷新失败也不会把已提交购买报告成失败。原模型订阅文件已缩为剩余调用方适配，并移除无人调用的管理/维护桥接。

各支付网关的结账请求和 webhook 传输入口、自助订阅偏好、钱包充值运行时及审计适配仍待迁移；本批未将整个支付体系视为完成。

第三十批将自助扣费偏好的读写归入 identity，自助订阅视图归入 subscription，两个接口保留原路由。偏好更新在用户行锁内读取当前设置，只更新 setting 列，提交后使用已有用户缓存发布机制；语言、侧边栏、通知设置及余额等字段不会被旧用户快照覆盖，偏好变更不推进认证版本。

自助订阅视图从同一份订阅列表筛出仍有效的条目，保留 subscriptions/all_subscriptions 响应字段，不再将数据库错误伪装成无订阅。订阅模块通过应用注入的身份模块查询偏好；身份、账务和订阅服务由应用各创建一个入口实例。

原 controller/subscription.go 已删除，订阅重置用户日志直接通过应用注入的 usage 服务写入。公共 HTTP 审计身份提取收进 transport/http/audit，控制器审计与重置日志使用同一映射。旧的整份用户设置替换函数、无调用方的订阅列表适配及对应旧测试移除，数据/账户字段保护改由身份模块的行为回归覆盖。

支付网关传输、其余身份入口、钱包与转发运行时、配置和旧调用适配仍在完整目标内。

第三十一批将 Stripe、Creem、Waffo Pancake 和 Epay 的订阅结账编排及 HTTP 入口迁入 subscription，Epay 通知与浏览器返回入口一并迁移。共用当前套餐、账户投影和购买上限校验，身份模块只输出结账所需的 ID、用户名、邮箱与 Stripe 客户标识；控制器不再持有四份重复的订阅订单创建流程。

billing 的 checkout 客户端封装 Stripe SDK、Creem HTTP、Waffo SDK 和 Epay 签名协议。Creem 与 Waffo 的钱包旧入口复用模块客户端，原配置通过一个薄适配提供。Stripe 使用实例上的请求密钥，不修改 stripe.Key，并按固定版本 SDK 的参数约束移除 subscription 模式不支持的 customer_creation。Creem 请求携带 context，JSON 读写统一使用 common 包装。

所有订阅网关调用前先保存本地 pending 订单，避免 Stripe 快速回调先于本地订单。网关调用失败或超时不猜测远端是否已受理，保留订单供后续经过验证的完成/过期通知处理；已经完成的快速回调不会被请求失败覆盖。Epay 使用模块订单事务处理重复通知，不再依赖进程内订单锁，响应与跳转格式保留。

四个旧订阅支付控制器文件已删除。其余支付渠道与钱包共用的 webhook 分发/验签控制器、配置适配及钱包运行时仍待继续迁移。

第三十二批将五种钱包充值渠道的订单完成、补单、记录查询和兑换码入账统一收进 billing 的 topups 实现。HTTP 查询/补单/兑换入口归 billing，账户支付资料通过 identity 的事务接口更新；剩余钱包结账及 webhook 控制器只保留调用适配。原模型兑换码实现与测试已移除，相关原子性回归归模块所有。

入账金额按持久化单位明确区分：Stripe 用 Money 换算额度，Creem 的 Amount 已是额度，Epay/Waffo/Waffo Pancake 的 Amount 按额度单位换算；管理员补单使用同一规则，修复 Creem 被再次放大的问题。所有渠道重复成功通知只返回已完成，不再重复增加缓存或写日志；订单状态、用户额度与支付资料在同一事务内提交，条件 UPDATE 守住钱包上限，非有限金额/单位及非法额度直接拒绝。

失败/过期更新只作用于 pending 订单，旧整行 Save 调用已替换，避免覆盖成功入账。Creem 支付邮箱只填充空账户邮箱，不覆盖已经绑定的邮箱；提交后按增量同步缓存并发布更新的账户元数据。兑换码保留用户级处理中响应和入口延迟，活动请求结束即移除锁标记，数据库事务仍是跨实例正确性的依据。

充值记录保留普通用户 30 天范围与管理员全量范围，关键词仍走原转义规则；搜索计数使用实际限制 10,000 条匹配记录的子查询，修复在 COUNT 聚合外加 LIMIT 不生效的问题。旧普通转发额度预扣/结算、批量缓存落库、充值结账配置和 webhook 编排仍待后续归属整理。

第三十三批将 Stripe 与 Creem 的 webhook 验签、事件解析和订单分发迁入 billing 的 webhooks 实现及 HTTP 适配。钱包和订阅通过各自事务入口处理，只有明确的订阅订单不存在错误才允许继续查找钱包订单，数据库错误或提供商不匹配不会绕过订阅分支。

Stripe 不再吞掉入账错误后返回 200，处理失败返回 500，使支付方能够重试。异步成功、失败和过期事件均支持订阅/钱包分发；迟到的失败/过期不会覆盖成功状态。Creem 保留 paid 状态、一次性钱包订单和订阅优先规则，签名错误、无效载荷和入账失败分别返回相应错误状态。原显式打印完整 webhook 请求体和签名的日志已移除。

回调可用性只依赖支付确认开关和对应 webhook 密钥，不再依赖钱包商品列表、StripePriceId 或 API 密钥，支持只部署订阅商品的配置。原控制器的钱包结账可用性仍独立保留。旧 Stripe/Creem 回调处理函数、签名工具和无人调用的模型桥接已移除。

Waffo/Waffo Pancake 回调、钱包结账与报价、配置适配以及转发额度运行时仍待继续整理。

第三十四批将旧 Waffo 与 Waffo Pancake 的回调验签、响应签名、订单查找和分发迁入 billing。旧控制器中的回调处理、进程订单锁，以及 service 中的 Pancake webhook DTO/解析/订单映射已移除；配置仍经现有设置适配进入模块。

旧 Waffo 只在 PAY_SUCCESS 入账，在 ORDER_CLOSE 关闭订单；PAY_IN_PROGRESS、AUTHORIZATION_REQUIRED、AUTHED_WAITING_CAPTURE 不再错误地标成失败。成功/失败响应按原协议返回 HTTP 200 的签名 JSON；业务失败返回签名 failed，签名与服务配置错误独立处理。迟到的关闭通知不会覆盖已入账订单。

Pancake 绑定路由环境对应的验签公钥，并验证签名载荷 mode、已配置的 StoreID、商户外部订单号、提供商及买家身份。StoreID 使用现有配置保存流程已要求的字段，避免其他 Store 的签名事件被错误关联；回调不再依赖钱包商品 ID，订阅商品可独立使用。已验证事件的数据库错误或暂时找不到本地订单返回 500/retry；环境、Store 或身份不匹配不触发入账。钱包记录不能替代订阅前缀对应的订阅订单。

Waffo 两套协议按各自 SDK 的签名与确认约定处理。钱包结账、报价、Epay 钱包回调、配置适配及普通转发额度运行时仍待继续整理。

第三十五批将钱包支付信息、通用金额/额度换算、Epay 钱包报价/下单/回调迁入 billing。旧 controller/topup.go 与进程内订单锁移除；Stripe/Creem/Waffo 旧结账入口暂时通过薄校验适配复用模块的额度边界校验。

报价一次生成原始数量、持久化整数单位、预计到账额度和价格，下单复用同一配置快照的计算结果。保留 Token 展示模式既有的整数单位截断，转换前检查整数范围、额度上限与非有限配置，避免极端额度单位导致溢出或 decimal 转换异常。用户分组由 identity 的账户投影提供，余额容量仍在报价阶段检查、入账事务中再次验证。

支付信息按原字段返回，但复制支付方式/折扣等集合后组装，避免修改共享配置；钱包下单显式执行支付确认门槛。Epay 钱包先持久化订单，再生成签名支付参数，回调以签名和已配置凭据为准，不依赖当前可选支付方式列表。报价、成功/失败确认和回调地址保持原接口约定。

其余钱包提供商的结账/报价、配置适配、普通转发额度与批量缓存落库仍待继续迁移。

第三十六批将 Stripe 钱包报价/结账与 Creem 商品结账迁入 billing 的 purchases 实现，旧 Stripe/Creem 钱包控制器和相应计算 helper 已移除。Stripe 钱包使用 checkout 模块的独立 SDK 客户端，请求显式携带 context、商品数量、促销码开关和重定向地址，不再修改 stripe.Key。

两个渠道均先持久化 pending 订单再访问支付方，网络失败不会删除或错误终止可能已受理的订单。Stripe 保留既有数量上限、展示报价与分组额度基数；Creem 从服务端商品配置选择价格和原生额度，客户端不能覆盖。金额/倍率/单位的非有限值被拒绝，额度继续经过钱包上限验证。自定义重定向沿用可信 URL 校验。

统一的结账错误保留服务端原因和订单关联日志，HTTP 只返回既有用户提示。入口、配置、DTO 和通用校验归属随迁移更新；Waffo 两套钱包结账、通用配置与转发额度运行时仍待继续整理。

认证运行时暂时通过只读适配访问模块配置，用户绑定和登录流程留待后续迁移。渠道健康测试、亲和性和转发执行暂后移；identity、gateway、billing、subscription、usage、system、配置及全局状态仍在完整目标内。工作继续在 `main` 上进行，每批验证后提交。

第三十七批迁移剩余的 Waffo 钱包结账与 Pancake 管理配置：

- Waffo/Pancake 报价与订单归 billing 私有 purchases；复用有界单位换算、钱包容量、分组倍率和折扣校验。Token 模式使用整数/decimal 归一化，支付方式以服务端列表为准；小于一个可持久化单位的请求继续拒绝。
- Waffo SDK 请求构造及签名归 checkout，保留支付币种、零小数币种格式、回调地址、付款方式和客户端响应字段；Pancake 使用同一模块客户端绑定规范买家身份、价格快照和认证 token。
- 两条路径均先持久化 pending 订单再请求网关；不将含糊的超时立即变成终态，后续验签成功的支付回调可继续入账，重复通知仍只记一次额度。
- Pancake 店铺/产品创建与发布、目录查询和配置保存归 billing 的 paymentconfig；system 选项管理器通过应用层注入，保存使用请求 context 和单个事务。空密钥保留现值，临时凭据不混用保存的密钥；创建产品失败继续返回已经创建的店铺，查询与创建不隐式保存配置。
- 删除根目录的 Waffo 控制器、支付校验/返回地址包装、service/waffo_pancake.go 与 model/topup.go。回归和 DragonflyDB 集成直接使用 billing 实体与接口，不再为测试保留生产转发包装。

第三十八批将钱包/令牌额度运行时与批处理归入 billing/accounting：

- 私有 accounting 实现拥有钱包增减、令牌增减、原子预扣、缓存补偿、用户使用量/请求数，以及参与同一事务的渠道使用量增量；公开 facade 接收数据库、缓存客户端和启用策略，不导入旧 model/service/controller。
- 全局批处理 maps、锁和 pending 批次改为模块实例状态。失败保留同一个批次 ID，数据库 receipt 防止不确定提交后的重复扣费；用户、令牌、渠道计数继续在一个事务里完成。定时任务可取消，Stop 等待任务退出后排空剩余队列；应用启动/关闭显式管理生命周期。
- 额度调整在直接写库成功或批量入队后同步更新缓存，移除原来脱离调用生命周期的额度 goroutine；数据库明确失败时不会先污染钱包/令牌缓存。原子缓存预扣写库失败时继续补偿，补偿不继承已取消请求的取消信号。
- 令牌缓存初始化、冷缓存查询、TTL 与变更 fence 归 identity/tokencache；不会用数据库快照覆盖已有缓存中的实时额度。billing 通过 identity 的公开缓存接口完成水合。
- 删除 model/quota_batch.go、quota_reserve.go、token_cache.go，移走 model/user.go、token.go 和 utils.go 中的账务实现。model/accounting_runtime.go 仅为尚未迁移的请求计费/任务调用方绑定共享实例及转发 API；这个适配器后续继续删除，不是最终模块边界。
- 支付配置快照移到 internal/app/payment.go，应用共享一个 checkout 客户端；service/payment_checkout.go 和 epay.go 删除。管理钱包调整直接使用模块账务服务，不再回调旧 model 的增减额度函数。

第三十九批迁移请求计费会话、资金来源和直接额度调整：

- billing/sessions 公开 Engine/Session，私有实现只持有模块请求契约、账务/订阅服务和自己的状态；不依赖 Gin、RelayInfo 或旧业务大包。转发层适配器负责 HTTP 错误映射与日志字段同步。
- 钱包和订阅的预扣、追加预留、结算、退款各自归私有资金来源实现；保持钱包补扣可记欠费、订阅最低预扣 1、强制预扣禁用信任旁路、严格订阅禁止钱包回退等现有行为。
- 订阅额度不足增加哨兵错误，使用 errors.Is 分类，不再通过错误消息决定资金来源回退。数据库错误即使包含“subscription quota insufficient”文本也不能触发钱包扣款；令牌回滚失败返回账务错误，阻止第二次预扣。
- Session 串行处理状态转换。退款在返回前执行，使用脱离请求取消信号的 context，多个退款调用不会重复记账；Playground 钱包预扣也纳入需要退款的状态。已退款会话拒绝再次结算，资金已提交但令牌更新失败的会话不会误退资金。
- 直接按次/任务额度调整同样由模块执行，结果保留 FundingApplied/TokenApplied，供旧任务调用方处理部分提交；单请求金额和调整量在算差额前检查边界。
- 删除 service/billing_session.go、funding_source.go；service/billing.go 留下转发适配和通知衔接，service/quota.go 不再实现预扣及资金/令牌更新。原 model/subscription_billing_test.go 的退款回归迁至模块，用直接可观察的数据库结果替代异步通知等待。

第四十批迁移模型定价目录、价格展示与分组策略：

- billing/pricing 拥有模型价格、供应商、端点和分组索引。每个实例构建完整快照后原子发布；读者拿到独立副本，不能改写缓存的分组切片、端点、价格指针、插件 usageSchema 或示例。刷新失败返回上一份完整快照和错误，价格 HTTP 接口明确报告失败。
- 渠道能力查询带 context 并按渠道/模型/分组排序；价格模型、供应商与自定义端点采用确定顺序。渠道变更只发出不阻塞的失效信号；新插件 generation 会立即触发下一次价格读取刷新，保留任务别名的表达式与 usageSchema 继承。
- 默认供应商匹配采用最长模式优先，显式元数据保持优先。PostgreSQL 初始 SQL 将供应商名称唯一性改为有效记录的部分唯一索引，阻止 deleted_at=NULL 时重复同名供应商；并发默认创建在冲突后读取已存在记录，软删除后仍可复用名称。
- 价格展示、倍率公开/重置接口迁入 billing transport；分组列表和自助可用分组归 identity。identity/groups 统一默认/特殊可用分组、用户自己的分组、Auto 去重/过滤/上限和用户分组倍率，认证、令牌配置和价格页使用同一策略。
- 应用层渠道管理和排行榜从 billing 快照获得元数据。删除三个根 controller 文件及 model/pricing.go、pricing_default.go、pricing_refresh.go、model_extra.go；原两个定价回归文件合并至模块，并使用正式 SQL 的隔离数据库。
- model/pricing_runtime.go 暂为未迁移的模型列表/转发调用方绑定模块实例。上游价格同步与请求实际计价仍在后续拆分范围内。

第四十一批迁移上游定价同步：

- billing/pricesync 拥有来源选择、并发抓取、格式转换与差异计算，私有实现按 fetch/decode/converters/differences 分开。controller/ratio_sync.go 删除；管理请求/差异 DTO 移至 billing/contract，RelayKit 的控制面 ratio_sync DTO 文件删除。
- 保留 ratio_config、pricing 数组、OpenRouter 和 models.dev 四种格式，以及官方/models.dev 预设、零值价格、表达式、倍率差异和可信度字段。结果按输入来源顺序返回，不再由网络完成顺序决定。
- 抓取复用应用的 HTTP 设施和配置中的 SSRF/代理策略；固定八个工作任务处理来源列表，排队、请求和重试响应取消。默认超时 10 秒，上限 120 秒；响应实际超过 10 MiB 明确拒绝。
- 渠道模块提供带 context 的来源查询，目录投影不读取密钥。OpenRouter 从选择的渠道取得启用密钥，校验目标同源；带认证请求禁止跨源重定向，避免把渠道密钥发送到替换地址。
- 转换结果只允许有限的非负价格与字符串表达式，拒绝负数、NaN/Inf、嵌套对象等无效值。保留模型间零价格和上游部分失败的报告；比较不修改本地定价，应用层仍负责本地配置快照。

第四十二批迁移剩余的钱包控制面功能：

- billing 拥有签到记录、月份统计、随机奖励、邀请奖励转入余额、OpenAI 兼容账单及令牌用量读取；删除 controller/checkin.go、billing.go、token.go、payment_compliance.go 与 model/checkin.go，并移走用户文件中的邀请转账实现。
- 签到在事务内锁定有效用户、创建唯一日期记录并增加有界额度；重复签到、不存在/已删除用户、无效奖励配置及余额上界失败不会留下孤立记录或错误奖励。成功提交后同步增量缓存并记录一次系统日志。
- 邀请转入只更新 aff_quota 和 quota 两个字段，持有用户行锁并检查邀请余额及钱包上界；提交后更新额度缓存，保留已预扣余额、认证版本与其他账户字段。
- 月份输入严格解析，查询使用月初到下月月初的半开区间；记录和累计统计在只读 repeatable-read 事务中读取。累计奖励和令牌总额度用精确 JSON 数字表达，避免整数相加溢出。
- 账单保留 USD/CNY/TOKENS、无限令牌与过期时间语义，先检查查询结果再访问数据；用户读取失败不再被后一次查询覆盖。Token usage 仍由原只读令牌鉴权保护，响应包含用量和模型限制，不返回密钥。
- 支付合规确认由 paymentconfig 调用系统选项管理器一次事务保存五个字段，保留禁止 API access token 确认的限制；数据库错误不会发布部分确认配置。

第四十三批按最新要求优先完成整包目录迁移：

- controller、service、model 的剩余 178 个文件一次性移动到上表位置，保留 package 名称和原有实现。189 个 Go 文件中的相关导入路径同步更新。
- 已逐个对照 HEAD 中的原文件，确认移动文件除导入路径外内容一致；根目录三个目录均不存在，也没有残留旧路径的 Go 导入。
- 架构检查禁止所有生产代码重新导入旧根包，同时继续禁止已迁移模块依赖 internal/legacy 或入站适配层。目录移动没有放开已有模块的依赖边界。
- 这一批完成目录层面的整理；业务拆分不作为本批迁移的前置条件，后续按用户优先级继续进行。

第四十四批继续按约定整包移动共享设施、配置、OAuth 和转发代码：

- common/constant/dto/types 移至 internal/shared 下的同名包；logger 移至 internal/infra/logger；setting 移至 internal/config/setting；oauth/relay 移至 internal/legacy 下的同名包。
- 400 个文件连同嵌入的 Lua 资源整体移动，656 个 Go 文件更新包路径。逐一比对 829 个受影响文件，确认除 Go 路径替换外内容不变；保留 package 名称和函数实现。
- 根目录上述八个目录均不存在；架构检查禁止重新导入旧路径，继续限制已拆模块对过渡包的依赖。
- relaykit 目录、独立 go.mod、模块路径和主模块 replace 配置均保持原样，独立构建检查继续执行。

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

## 第十三批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归，以及第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test ./internal/arch ./internal/module/system/... ./controller ./internal/transport/task -count=1
```

原任务存储和调度测试迁入模块，改为 SQL 迁移初始化的独立 schema；覆盖任务生命周期、去重、并发抢占只有一个持有者、抢占失败回滚、租约过期、旧执行器写入保护、实例注册表隔离和应用取消传播。日志清理管理接口分别在真实 PostgreSQL 和 ClickHouse 上完成任务，验证进度、删除数量、保留较新日志、终态及租约释放。

输出：`/tmp/new-api-system-tasks-tests.log`、`/tmp/new-api-system-tasks-full-tests.log`、`/tmp/new-api-system-tasks-vet.log`、`/tmp/new-api-system-tasks-startup.log`。

## 第十四批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归与第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test ./internal/arch ./internal/module/system/... ./internal/transport/http/routes -count=1
```

在正式 SQL 初始化的隔离 schema 中验证重复上报、角色/资源信息序列化、创建时间保留、90 秒过期边界、心跳恢复后的删除保护、列表和清理响应、主机名回退及取消后的上报循环退出。任务调度与日志清理回归继续通过。

输出：`/tmp/new-api-system-instances-tests.log`、`/tmp/new-api-system-instances-full-tests.log`、`/tmp/new-api-system-instances-vet.log`、`/tmp/new-api-system-instances-startup.log`。

## 第十五批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归和第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test ./internal/arch ./internal/module/system/... ./controller ./model -count=1
```

读取并遵循 `pkg/billingexpr/expr.md`，保留插件 usage schema、模型别名和非负表达式校验。SQL 集成测试覆盖单项失败不更新倍率、分批写入时的事务回滚、显式空值、无效倍率拒绝、敏感项过滤、有效计费配置投影和重载失败保留旧快照；原 Gemini/Claude、插件开关和计费配置回归继续通过。

输出：`/tmp/new-api-options-module-tests.log`、`/tmp/new-api-options-module-full-tests.log`、`/tmp/new-api-options-module-vet.log`、`/tmp/new-api-options-module-startup.log`。

## 第十六批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归及第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./controller ./model \
  -run 'TestModular|TestOAuthBinding|TestProvider|TestExternalIdentity|TestClearTelegram|TestHardDelete|TestUserDeletion|TestTelegram' -count=1
```

SQL 集成测试覆盖绑定响应与所有权隔离、管理员权限、自助解绑、注册回滚、并发绑定只有一个所有者、替换绑定保留创建时间、提供商删除与绑定竞态、外部身份重复声明/冲突/释放。既有 Telegram 和用户硬删除回归继续通过。

输出：`/tmp/new-api-bindings-module-tests.log`、`/tmp/new-api-bindings-module-full-tests.log`、`/tmp/new-api-bindings-module-vet.log`、`/tmp/new-api-bindings-module-startup.log`。

## 第十七批验证（2026-09-05）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归和第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./model ./controller \
  -run 'TestModular|TestTwoFA|TestPendingTwoFA|TestValidateBackupCode|TestIncrementFailedAttempts|TestSecurityFactor|TestHardDelete|TestPasskey' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

SQL 集成覆盖完整启停、备用码散列与重建、初始化失败回滚、状态响应不含密钥、管理员同级保护、认证版本推进与会话撤销；原并发备用码、失败次数和账户删除回归继续通过。真实 DragonflyDB 覆盖预热会话后的启停，确认旧令牌失效、当前会话续签和额度保留。

输出：`/tmp/new-api-twofa-module-tests.log`、`/tmp/new-api-twofa-module-dragonfly.log`、`/tmp/new-api-twofa-module-full-tests.log`、`/tmp/new-api-twofa-module-vet.log`、`/tmp/new-api-twofa-module-startup.log`。

## 第十八批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归及第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./model ./controller \
  -run 'TestModular|TestPasskey|TestParsePasskey|TestAuthFlow|TestClaimExternal|TestSecurityFactor|TestUpdatePasskey|TestHardDelete' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

使用真实 P-256 签名和 CBOR 凭据完成注册、Passkey 登录、升阶验证和删除；验证挑战重放拒绝、安全证明不足不消费挑战、状态响应隐藏凭据、断言计数器更新、凭据替换失败回滚与管理员同级保护。既有认证挑战并发消费和账户清理回归继续通过。DragonflyDB 验证凭据增删后的会话续签、旧令牌失效及额度保留。

输出：`/tmp/new-api-passkey-module-tests.log`、`/tmp/new-api-passkey-module-dragonfly.log`、`/tmp/new-api-passkey-module-full-tests.log`、`/tmp/new-api-passkey-module-vet.log`、`/tmp/new-api-passkey-module-startup.log`。

## 第十九批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归与第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./model ./controller ./service \
  -run 'TestModular|TestSession|TestAccessToken|TestSecurityProof|TestAuth|TestRefresh|TestCreateLogin|TestUserSession|TestRevoke|TestLogout|TestValidateLogin' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

保留 JWT 篡改、用途隔离、安全证明范围及认证身份绑定测试，新增真实认证中间件下的会话列表、刷新 Cookie、安全响应、撤销其他会话和退出流程。DragonflyDB 验证刷新令牌轮换、并发窗口中恢复同一后继令牌及退出后的访问拒绝。

输出：`/tmp/new-api-auth-runtime-tests.log`、`/tmp/new-api-auth-runtime-dragonfly.log`、`/tmp/new-api-auth-runtime-full-tests.log`、`/tmp/new-api-auth-runtime-vet.log`、`/tmp/new-api-auth-runtime-startup.log`。

## 第二十批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归和第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./model ./controller ./service \
  -run 'TestModular|TestUserSession|TestRotateUserSession|TestRevoke|TestCleanup|TestLoginSession|TestSession|TestAuth|TestUserBase|TestUserUpdate|TestPasswordReset' -count=1
```

验证短 TTL、延迟回填不能恢复已撤销会话、数据库失败下的撤销边界、刷新竞争/重用、会话列表限制、分批撤销、到期与审计保留期清理，以及密码变更后的失效。完整回归中的真实 DragonflyDB 场景继续覆盖会话刷新、撤销和安全配置变更。

输出：`/tmp/new-api-session-store-tests.log`、`/tmp/new-api-session-store-full-tests.log`、`/tmp/new-api-session-store-vet.log`、`/tmp/new-api-session-store-startup.log`。

## 第二十一批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归与第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./model ./controller ./service \
  -run 'TestModular|TestUserAuth|TestPendingUserAuth|TestCommittedUserAuth|TestRefreshUserGroup|TestUserUpdate|TestSecurityFactor|TestHardDelete|TestSession|TestAuth' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonfly -count=1
```

真实 DragonflyDB 验证扣减额度后发布元数据仍保留余额，提交禁用与新认证版本后旧快照无法回填；SQL/缓存回归继续覆盖回滚恢复和版本下限单调性。

输出：`/tmp/new-api-usercache-module-tests.log`、`/tmp/new-api-usercache-module-dragonfly.log`、`/tmp/new-api-usercache-module-full-tests.log`、`/tmp/new-api-usercache-module-vet.log`、`/tmp/new-api-usercache-module-startup.log`。

## 第二十二批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归与第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test ./internal/arch ./internal/module/usage/... ./model ./controller ./internal/module/system/... \
  -run 'TestModular|TestLog|TestFormat|TestTaskPluginLog|TestLegacyLog|TestClickHouse|TestRecord|TestProvider|TestRelayed|TestError' -count=1
```

在真实 PostgreSQL/ClickHouse 上验证相同时间戳的游标分页、游标身份绑定与篡改拒绝、字符串转义、用户 ID 统计隔离、请求 ID 补齐/保留、展示 ID、敏感元数据过滤和清理批次。原角色分层及大整数元数据回归随实现保留，系统日志清理任务继续通过。

输出：`/tmp/new-api-usage-module-tests.log`、`/tmp/new-api-usage-module-full-tests.log`、`/tmp/new-api-usage-module-vet.log`、`/tmp/new-api-usage-module-startup.log`。

## 第二十三批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、第四批完整后端回归及第一批三种日志配置的新库/两次重启均通过。

专项命令：

```sh
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test ./internal/arch ./internal/module/usage/... ./model ./controller ./service \
  -run 'TestModular|TestLog|TestRecord|TestAudit|TestRelayError|TestTaskLog|TestQuotaData|TestLegacyLog' -count=1
```

真实 PostgreSQL/ClickHouse 验证消费、错误、任务和审计日志写入，覆盖 IP 偏好、请求 ID 保留、任务发起节点、汇总参数、审计所属用户和角色过滤，以及关闭消费日志后的存储/汇总行为。原计费日志回归继续通过。

输出：`/tmp/new-api-usage-writers-tests.log`、`/tmp/new-api-usage-writers-full-tests.log`、`/tmp/new-api-usage-writers-vet.log`、`/tmp/new-api-usage-writers-startup.log`。

## 第二十四批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令（均在项目根目录运行，RelayKit 命令除外）：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/usage/... ./model ./controller ./service \
  -run 'TestModular|TestUsageAggregate|TestUsageDashboard|TestGetFlowQuotaData' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/usage \
  -run 'TestUsageAggregate|TestUsageDashboard|TestGetFlowQuotaData' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

汇总测试使用 `internal/testdb` 的隔离 schema，连续执行两次正式 SQL 初始化，验证组合唯一/非空约束、8 个维度分别隔离、两个独立实例并发累加、501 行跨 SQL 批次失败的整体回滚、失败期间的新事件、重试不重复累加已成功快照、取消后重试及停止周期任务后最终刷新。查询覆盖用户身份固定、管理员/Root 字段范围、软删除令牌名称、小时序列、排行榜分桶和非法时间范围。竞态检查通过；完整回归继续验证真实 PostgreSQL、ClickHouse 和 DragonflyDB。

输出：`/tmp/new-api-usage-aggregation-build.log`、`/tmp/new-api-usage-aggregation-tests.log`、`/tmp/new-api-usage-aggregation-race.log`、`/tmp/new-api-usage-aggregation-full-tests.log`、`/tmp/new-api-usage-aggregation-vet.log`、`/tmp/new-api-usage-aggregation-startup.log`。

## 第二十五批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/usage/... ./internal/transport/http/middleware \
  -run 'TestModular|TestRankings|TestUsageDashboard|TestHeaderNav' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/usage/internal/rankings -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

排行榜测试在真实 PostgreSQL 的隔离 schema 上使用正式 SQL 初始化，覆盖 24 小时/周/月/年周期边界、前期排名、并列名次稳定性、模型/供应商份额、增长率、未知供应商回退、历史分桶、20/10/5/6 条展示上限及 Others 汇总。固定时钟验证默认周周期、实例间缓存隔离、5 分钟到期刷新、并发访问、请求取消、SQL 失败后恢复和 HTTP 响应；原导航访问控制回归保持通过。专项测试与竞态检查全部通过。

输出：`/tmp/new-api-rankings-module-build.log`、`/tmp/new-api-rankings-module-tests.log`、`/tmp/new-api-rankings-module-race.log`、`/tmp/new-api-rankings-module-full-tests.log`、`/tmp/new-api-rankings-module-vet.log`、`/tmp/new-api-rankings-module-startup.log`。

## 第二十六批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/usage/... ./model ./service ./controller ./internal/transport/http/middleware \
  -run 'TestModular|TestPerformance|TestHeaderNav' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/usage -run TestPerformance -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

真实性能统计测试使用正式 SQL 初始化的隔离 PostgreSQL schema，验证 SQL/内存合并后的加权指标、完整字段回滚、刷新期间采样与查询、两个实例原子累加、已完成桶/当前桶区别、分钟/5 分钟/小时粒度、重复刷新、保留期边界、大整数设置、请求取消及关闭时保存。HTTP 测试保留响应缓存标记、隐藏请求计数、过滤停用分组，并验证缺失模型参数及小时数默认/上限；活跃分组提供方返回空集合时不扩大查询范围。

本批没有新增 DragonflyDB 读写协议；删除了没有读取用途的性能指标写入，完整后端回归继续在真实 DragonflyDB 配置下运行通过。

输出：`/tmp/new-api-performance-module-build.log`、`/tmp/new-api-performance-module-tests.log`、`/tmp/new-api-performance-module-race.log`、`/tmp/new-api-performance-module-full-tests.log`、`/tmp/new-api-performance-module-vet.log`、`/tmp/new-api-performance-module-startup.log`。

## 第二十七批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/subscription/... ./model ./service ./controller \
  -run 'TestModular|TestPlan|TestMembership|TestSubscription|TestAdminReset|TestWallet|TestBalance' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/subscription/... -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

正式 SQL 初始化的隔离 PostgreSQL schema 验证重置的用户/套餐/有效期范围、显式保留与默认推进时间、审计回调、并发购买限额、分配与取消并发、重叠订阅分组保留、删除回退、到期降级事务回滚和提交计数。软/硬删除账户后的残留订阅清理继续成功，取消的订阅仍计入累计购买上限，原余额购买、支付回调和预扣退款回归通过。

真实 DragonflyDB 新增分配/取消订阅场景：分组随 SQL 状态更新，缓存余额保留预先扣减后的值，认证版本和既有登录会话保持有效。竞态检查通过。

输出：`/tmp/new-api-memberships-build.log`、`/tmp/new-api-memberships-tests.log`、`/tmp/new-api-memberships-race.log`、`/tmp/new-api-memberships-dragonfly.log`、`/tmp/new-api-memberships-full-tests.log`、`/tmp/new-api-memberships-vet.log`、`/tmp/new-api-memberships-startup.log`。

## 第二十八批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/subscription/... ./model ./service ./controller \
  -run 'TestModular|TestPlan|TestMembership|TestSubscription|TestBillingSessionRefund' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/subscription/... -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

原 SQL 回归随额度实现迁入模块，覆盖重复请求只扣一次、请求归属隔离、退款失败整体回滚、追加预扣可完整退款、大整数不回绕、选择下一有效订阅及自动重置。新增正式 SQL 隔离 schema 测试验证缓存与事务相互隔离、回滚新套餐不污染缓存、标题失效刷新、重置读取最新套餐、关闭周期不清额度、重置失败不计完成数、过期订阅不再重置、维护清理及取消退出；上层 BillingSession 的退款联动回归保留并通过。

真实 DragonflyDB 验证套餐缓存 TTL、事务读取绕过缓存且不发布未提交值、回滚后展示仍读取提交前值、失效后订阅标题刷新。竞态检查通过。

输出：`/tmp/new-api-subscription-quota-build.log`、`/tmp/new-api-subscription-quota-tests.log`、`/tmp/new-api-subscription-quota-race.log`、`/tmp/new-api-subscription-quota-dragonfly.log`、`/tmp/new-api-subscription-quota-full-tests.log`、`/tmp/new-api-subscription-quota-vet.log`、`/tmp/new-api-subscription-quota-startup.log`。

## 第二十九批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/subscription/... ./internal/module/billing/... ./model ./controller ./service \
  -run 'TestModular|TestPlan|TestMembership|TestSubscription|TestCompleteSubscription|TestExpireSubscription|TestRecharge|TestBillingSession' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/subscription/... ./internal/module/billing/... -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

正式 SQL 隔离 PostgreSQL 测试覆盖并发重复回调只发放/记录一次、购买后停用套餐仍可完成订单、实际支付方式和提供商一致、成功订单抵抗迟到的失败/过期通知、收款失败整体回滚、数据库错误不伪装成不存在、钱包充值流水冲突拒绝、并发余额购买、订单写入失败回滚、价格取整和 NaN/Inf/额度上限拒绝。HTTP 测试保留支付确认门槛和登录用户归属；提交后的缓存故障或请求取消不改变成功购买与日志结果。

真实 DragonflyDB 验证余额购买后的 SQL/缓存余额一致、保留其他请求已有预扣及分组刷新，购买次数限制失败后不再次扣款。原支付提供商隔离、钱包充值和上层退款联动回归继续通过；竞态检查通过。

输出：`/tmp/new-api-subscription-payments-build.log`、`/tmp/new-api-subscription-payments-tests.log`、`/tmp/new-api-subscription-payments-race.log`、`/tmp/new-api-subscription-payments-dragonfly.log`、`/tmp/new-api-subscription-payments-full-tests.log`、`/tmp/new-api-subscription-payments-vet.log`、`/tmp/new-api-subscription-payments-startup.log`。

## 第三十批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/identity/... ./internal/module/subscription/... ./model ./controller ./service \
  -run 'TestModular|TestBillingPreference|TestSelfSubscriptions|TestSelf|TestUserUpdate|TestSubscription|TestAdminReset' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/identity ./internal/module/subscription \
  -run 'TestBillingPreference|TestSelfSubscriptions' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

在正式 SQL 隔离 PostgreSQL schema 上验证偏好与语言并发更新、保留其他设置和账户计数、无效偏好归一化、设置写入失败回滚、删除账户拒绝修改，以及真实认证中间件下忽略伪造 user_id/role/quota。订阅查询测试验证用户范围、有效/过期/取消条目、空数组响应与数据库失败显式返回错误。原账户、订阅和审计回归继续通过，新增并发测试的竞态检查通过。

真实 DragonflyDB 测试启用批量额度落库，先产生未落库预扣，再发布扣费偏好，确认缓存余额不被 SQL 中的旧余额覆盖，语言与认证版本保持，最终落库后的余额正确。

输出：`/tmp/new-api-subscription-self-build.log`、`/tmp/new-api-subscription-self-tests.log`、`/tmp/new-api-subscription-self-race.log`、`/tmp/new-api-subscription-self-dragonfly.log`、`/tmp/new-api-subscription-self-full-tests.log`、`/tmp/new-api-subscription-self-vet.log`、`/tmp/new-api-subscription-self-startup.log`。

## 第三十一批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。支付 SDK 保持 **stripe-go v81.4.0**、**waffo-pancake-sdk-go v0.3.1**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./internal/module/subscription/... ./controller ./service ./model \
  -run 'TestModular|TestStripeSubscription|TestCreemCheckout|TestWaffo|TestEpayCheckout|TestSubscription|TestPayment|TestRecharge' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing ./internal/module/subscription -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

本地 HTTP 服务验证 Stripe SDK 实际发出的表单、两个客户端并发时的密钥隔离、已有/新客户参数及 subscription 模式；Creem 验证元数据、API 密钥头、取消、异常响应和空链接；Waffo SDK 验证两步认证结账、买家身份、商户流水和令牌片段；Epay 验证签名参数、回调签名拒绝和返回 URL。

真实 PostgreSQL 业务测试确认网关调用前订单已保存、四种响应字段和流水前缀保持、用户信息取自身份模块、套餐停用/购买上限/订单写入失败时不调用网关、超时后订单仍能完成，以及回调先完成时请求失败不会撤销订单。Epay HTTP 通知/返回使用真实签名验证和订单事务，重复通知仅发放一次订阅。完整回归继续覆盖真实 DragonflyDB 和 PostgreSQL/ClickHouse 日志；竞态检查通过。

输出：`/tmp/new-api-checkout-build.log`、`/tmp/new-api-checkout-tests.log`、`/tmp/new-api-checkout-race.log`、`/tmp/new-api-checkout-full-tests.log`、`/tmp/new-api-checkout-vet.log`、`/tmp/new-api-checkout-startup.log`。

## 第三十二批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./internal/module/subscription/... ./model ./controller ./service \
  -run 'TestModular|TestTopup|TestRecharge|TestUpdatePendingTopUp|TestRedeem|TestSubscription|TestPayment|TestValidateTopUp|TestWallet' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing/... -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

正式 SQL 隔离 PostgreSQL 测试覆盖五渠道自动/手动入账的单位一致性、重复通知不重复加款、成功状态保护、两个不同订单竞争钱包上限、订单完成失败时回滚额度和支付资料、非法额度/NaN/Inf 拒绝、现有邮箱保护、数据库故障不伪装成不存在。查询验证用户时间范围、管理员全量范围、转义、HTTP 用户隔离，并用 10,001 条确定性匹配记录验证搜索计数真实限制在 10,000。兑换码的单次消费、并发唯一胜者和额度上限回滚回归迁至模块并通过。

真实 DragonflyDB 覆盖五个充值提供商，在预扣尚未批量落库时完成充值，确认缓存增量只应用一次、不会因支付资料发布覆盖余额，最终批量落库后 SQL/缓存一致。竞态检查通过。

输出：`/tmp/new-api-topups-build.log`、`/tmp/new-api-topups-tests.log`、`/tmp/new-api-topups-race.log`、`/tmp/new-api-topups-dragonfly.log`、`/tmp/new-api-topups-full-tests.log`、`/tmp/new-api-topups-vet.log`、`/tmp/new-api-topups-startup.log`。

## 第三十三批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./internal/module/subscription/... ./controller ./model ./service \
  -run 'TestModular|TestStripeWebhook|TestCreemWebhook|TestWebhook|TestStripeTopUp|TestCreemTopUp|TestPayment|TestSubscription|TestTopup|TestRecharge' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing \
  -run 'TestStripeWebhook|TestCreemWebhook|TestWebhook' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

带真实 Stripe SDK 签名和 Creem HMAC 签名的 HTTP 测试使用正式 SQL 初始化的隔离 PostgreSQL schema，覆盖验签拒绝、Stripe 签名过期、无效载荷、未支付与未知事件忽略、订阅/钱包分发、异步成功/失败、过期及迟到事件、重复通知只发放一次。数据库故障和入账回滚返回 500，修复后相同事件可重试成功；订阅提供商不匹配不会绕道给钱包充值。只提供 webhook 密钥的配置可工作，支付确认关闭或密钥缺失则拒绝请求。

完整回归继续覆盖真实 DragonflyDB 入账缓存和 PostgreSQL/ClickHouse 日志，竞态检查通过。

输出：`/tmp/new-api-webhooks-build.log`、`/tmp/new-api-webhooks-tests.log`、`/tmp/new-api-webhooks-race.log`、`/tmp/new-api-webhooks-full-tests.log`、`/tmp/new-api-webhooks-vet.log`、`/tmp/new-api-webhooks-startup.log`。

## 第三十四批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**；支付 SDK 保持 **waffo-go v1.3.2**、**waffo-pancake-sdk-go v0.3.1**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./internal/module/subscription/... ./controller ./model ./service \
  -run 'TestModular|TestWaffo|TestPancake|TestWebhook|TestSubscription|TestPayment|TestTopup|TestRecharge' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing \
  -run 'TestWaffoWebhook|TestPancake|TestStripeWebhook|TestCreemWebhook|TestWebhook' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

使用独立 RSA 密钥生成有效请求签名，并验证商户响应签名。真实 PostgreSQL 测试覆盖 Waffo 支付中/授权/待捕获状态保留 pending、ORDER_CLOSE 关闭待支付订单、入账失败签名 failed、恢复后重试、重复成功和迟到关闭。

Pancake 测试覆盖测试/生产公钥隔离、签名时效、签名载荷环境、Store 和买家身份不匹配不入账、数据库故障返回 retry、重复通知只发放一次、订阅与钱包不能互相替代，以及配置缺少 Store 时拒绝回调。完整回归继续验证真实 DragonflyDB 入账缓存和 PostgreSQL/ClickHouse 日志；竞态检查通过。

输出：`/tmp/new-api-waffo-hooks-build.log`、`/tmp/new-api-waffo-hooks-tests.log`、`/tmp/new-api-waffo-hooks-race.log`、`/tmp/new-api-waffo-hooks-full-tests.log`、`/tmp/new-api-waffo-hooks-vet.log`、`/tmp/new-api-waffo-hooks-startup.log`。

## 第三十五批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./internal/module/subscription/... ./controller ./model ./service \
  -run 'TestModular|TestWallet|TestEpay|TestTopup|TestValidateCredited|TestStripeCredited|TestSubscription|TestRecharge' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing -run 'TestWallet|TestEpay' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

模块测试覆盖货币/Token 展示模式、大整数、持久化单位截断、最大充值提示、非法单位/价格/折扣拒绝、钱包容量检查、支付信息启用条件及集合隔离。真实 PostgreSQL 与 Epay 签名测试将报价、订单和最终入账连通，验证回调地址、重复通知、停用可选支付方式后仍处理有效旧订单，以及支付确认关闭时不创建新订单。原控制器的额度回归迁入模块，其他提供商的校验回归保留。

完整回归继续覆盖真实 DragonflyDB 入账缓存和 PostgreSQL/ClickHouse 日志，竞态检查通过。

输出：`/tmp/new-api-wallet-epay-build.log`、`/tmp/new-api-wallet-epay-tests.log`、`/tmp/new-api-wallet-epay-race.log`、`/tmp/new-api-wallet-epay-full-tests.log`、`/tmp/new-api-wallet-epay-vet.log`、`/tmp/new-api-wallet-epay-startup.log`。

## 第三十六批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/arch ./internal/module/billing/... ./internal/module/subscription/... ./controller ./model ./service \
  -run 'TestModular|TestWallet|TestStripeWallet|TestCreemWallet|TestStripeSubscription|TestCreemCheckout|TestSubscription|TestTopup|TestRecharge' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing \
  -run 'TestStripeWallet|TestCreemWallet|TestWalletCheckout' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

本地 HTTP 服务验证 Stripe payment 模式、数量、商品、促销码、新客户创建、默认/自定义跳转和请求取消。真实 PostgreSQL 测试验证分组额度基数、报价与入账字段的既有语义、数量/倍率/单位拒绝、重定向白名单、先保存订单再调用网关、超时后的有效回调继续完成，以及 Creem 服务端商品选择和价格/额度不可伪造。

完整回归继续验证真实 DragonflyDB 入账缓存与 PostgreSQL/ClickHouse 日志；竞态检查通过。

输出：`/tmp/new-api-wallet-stripe-creem-build.log`、`/tmp/new-api-wallet-stripe-creem-tests.log`、`/tmp/new-api-wallet-stripe-creem-race.log`、`/tmp/new-api-wallet-stripe-creem-full-tests.log`、`/tmp/new-api-wallet-stripe-creem-vet.log`、`/tmp/new-api-wallet-stripe-creem-startup.log`。

## 第三十七批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**；支付 SDK 保持 **waffo-go v1.3.2**、**waffo-pancake-sdk-go v0.3.1**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归、相关竞态测试及三种日志配置的新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/module/billing ./internal/arch -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing \
  -run 'TestWaffoWallet|TestPancakeWallet|TestPancakeConfiguration|TestPancakeManagement' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-modular-startup.py
```

真实 PostgreSQL 测试覆盖两种展示单位的报价与持久化额度、非法倍率/折扣/超大输入拒绝、服务端支付方式选择、创建网关会话前已存在的 pending 订单、超时后继续入账及重复完成。Pancake 配置保存通过真实选项管理器，覆盖空密钥保留、一次事务保存全部字段、注入数据库错误后整体回滚与请求取消。

SDK 协议测试验证 Waffo RSA 请求/响应签名、币种金额格式、支付跳转和空响应拒绝；Pancake 使用本地 HTTP 服务验证买家身份、价格快照、认证 token、产品发布失败时保留店铺信息，以及目录过滤。没有调用真实支付账户。完整回归继续覆盖真实 DragonflyDB 缓存及 PostgreSQL/ClickHouse 日志。

首轮启动验证在输出应用日志前达到 20 秒期限，验证器随后终止并回收子进程；确认版本命令可执行后，使用同一二进制和原始期限重跑，全部九次启动及版本/数据检查通过。未改变应用或放宽验证期限。

输出：`/tmp/new-api-waffo-wallet-build.log`、`/tmp/new-api-waffo-module-tests.log`、`/tmp/new-api-waffo-wallet-race.log`、`/tmp/new-api-waffo-wallet-full-tests.log`、`/tmp/new-api-waffo-wallet-vet.log`、`/tmp/new-api-waffo-wallet-startup.log`。

## 第三十八批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归、账务模块竞态测试以及启用批处理的三种日志配置新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/module/billing/internal/accounting ./internal/arch -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing/internal/accounting -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-accounting-startup.py
```

账务测试使用正式 SQL 初始化的隔离 PostgreSQL schema，验证两个最大单请求扣费可安全累积、批次最后一项写入失败时全部回滚、相同批次 ID 不重复扣费、钱包上界和累加溢出拒绝、模块实例队列隔离、取消与停止后排空。原请求预扣及令牌缓存回归继续验证数据库回退、脚本重新加载和变更 fence。

真实 DragonflyDB 集成覆盖用户并发预扣、精确大整数余额、令牌实时余量、配置变更缓存失效、充值和订阅购买不覆盖预扣。新增数据库错误注入验证：明确写库失败后钱包/令牌缓存不变化，Lua 已成功预扣的部分正确补偿；恢复写入后返回时缓存与数据库余额一致。

启动脚本沿用三种日志配置的新库与版本/保留数据检查，并在全部九次启动中设置 `BATCH_UPDATE_ENABLED=true`，验证工作线程可正常启动和停止。批处理仍是进程内队列；本批没有为进程崩溃前尚未落库的增量增加持久化保证。

输出：`/tmp/new-api-accounting-build.log`、`/tmp/new-api-accounting-module-tests.log`、`/tmp/new-api-accounting-race.log`、`/tmp/new-api-accounting-dragonfly-tests.log`、`/tmp/new-api-accounting-full-tests.log`、`/tmp/new-api-accounting-vet.log`、`/tmp/new-api-accounting-startup.log`。

## 第三十九批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归、计费会话竞态测试，以及启用批处理的三种日志配置新库/两次重启均通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/module/billing/... ./internal/module/subscription/... ./internal/arch ./service \
  -run 'TestBillingSession|TestPrepareTieredBillingForSelectedGroupTopUp|TestModular|TestSubscription' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing -run TestBillingSession -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-accounting-startup.py
```

真实 PostgreSQL 用例覆盖钱包/订阅偏好与回退、令牌预扣回滚、追加预留回滚、同时重复退款、请求取消后的退款、Playground 钱包退款、信任旁路与强制预扣、订阅最低预扣、负数/超大额度拒绝。注入数据库错误验证：消息包含额度不足文本的存储错误不会回退钱包，令牌回滚失败不会触发第二次预扣；资金已结算而令牌更新失败时不会退款或重复结算。

DragonflyDB 全链路通过真实转发适配器执行预扣 30 → 追加到 50 → 取消请求后退款，以及新会话预扣 30 → 实际结算 40。在批量模式下验证即时缓存、最终数据库、令牌已用额度及订阅日志差额。原阶梯计费的更贵分组重试和欠费结算回归继续通过，测试改为真实预扣初始化会话。

资金与令牌仍分两步提交；第二步失败通过错误和日志暴露，已提交资金不会自动回滚。非幂等的钱包/令牌退款仅尝试一次；订阅退款使用请求回执进行有限重试。

输出：`/tmp/new-api-billing-sessions-build.log`、`/tmp/new-api-billing-sessions-tests.log`、`/tmp/new-api-billing-sessions-race.log`、`/tmp/new-api-billing-sessions-dragonfly.log`、`/tmp/new-api-billing-sessions-full-tests.log`、`/tmp/new-api-billing-sessions-vet.log`、`/tmp/new-api-billing-sessions-startup.log`。

## 第四十批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归、定价竞态检查与三种日志配置的新库/两次重启全部通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/module/billing \
  -run 'TestPricing|TestInitChannelCache|TestCacheUpdateChannelSyncs' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing \
  -run 'TestPricing|TestInitChannelCache|TestCacheUpdateChannelSyncs' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-pricing-startup.py
```

模块用正式 SQL 初始化隔离 PostgreSQL schema 两次，覆盖原生/高级自定义端点合并、渠道缓存失效、插件 generation 更新、别名表达式与 usageSchema。新回归验证独立快照及实例、失败刷新保留原快照且可恢复、并发创建默认供应商、重复有效供应商拒绝及软删除名称复用；HTTP 定价结果与用户分组策略同时验证匿名、VIP 倍率覆盖、Auto 顺序和空权限列表。

启动脚本在共享 PostgreSQL 日志、独立 PostgreSQL 日志、ClickHouse 日志三种配置中分别建立新数据库并重启两次；全部启用批处理。在每个主库实际插入重复供应商，要求数据库报 unique_violation，并在重启后验证只有一条有效记录。保留数据和主/日志 schema 版本检查继续通过。完整回归继续覆盖真实 DragonflyDB 与 ClickHouse。

输出：`/tmp/new-api-pricing-build.log`、`/tmp/new-api-pricing-module-tests.log`、`/tmp/new-api-pricing-race.log`、`/tmp/new-api-pricing-full-tests.log`、`/tmp/new-api-pricing-vet.log`、`/tmp/new-api-pricing-startup.log`。

## 第四十一批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归、同步器竞态检查和三种日志配置的新库/两次重启全部通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test ./internal/module/billing/... ./internal/arch -run 'TestPriceSync|TestModular' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
GOWORK=off go test -race ./internal/module/billing -run TestPriceSync -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-pricing-startup.py
```

本地 HTTP 服务覆盖四种价格格式、可选价格显式零值、表达式、models.dev 最便宜非零候选、OpenRouter 非有限/动态价格跳过、差异可信度、无变化来源剔除和输入顺序。无效标量/嵌套价格与超过 10 MiB 的响应被拒绝；八个在途来源加一个排队来源验证取消会结束全部任务且不发出排队请求。

真实 PostgreSQL 的渠道投影、密钥选择和 HTTP handler 测试验证来源目录无密钥、预设 ID、channel_ids 路径、OpenRouter 仅向保存渠道同源地址附加认证，以及跨源重定向被阻止。还验证参数错误 400、空来源业务错误和查询失败 500。测试使用本地服务提供上游价格响应。

完整回归继续覆盖真实 DragonflyDB 与 ClickHouse；新库启动、两次重启、schema 版本、保留数据和供应商唯一约束检查继续通过。

输出：`/tmp/new-api-price-sync-build.log`、`/tmp/new-api-price-sync-tests.log`、`/tmp/new-api-price-sync-race.log`、`/tmp/new-api-price-sync-full-tests.log`、`/tmp/new-api-price-sync-vet.log`、`/tmp/new-api-price-sync-startup.log`。

## 第四十二批验证（2026-09-06）

Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。主模块 build/vet、RelayKit 独立 build/vet、完整后端回归、相关竞态检查和三种日志配置的新库/两次重启全部通过。

本批命令：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test ./internal/module/billing ./internal/arch \
  -run 'TestCheckin|TestAffiliate|TestBillingStatements|TestModular' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
GOWORK=off go test -race ./internal/module/billing \
  -run 'TestCheckin|TestAffiliate|TestBillingStatements|TestPaymentComplianceConfirmation' -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off go test ./e2e -run TestDragonflyCacheContracts -count=1
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-pricing-startup.py
```

真实 PostgreSQL 覆盖并发签到只发一次奖励、闰月/跨月统计、零奖励、无效配置、余额上界回滚、缺失用户、并发邀请转入、字段隔离、账单单位/令牌归属及两个 bigint 极值的总额。确认配置通过真实选项管理器注入写入失败，验证整个事务回滚；成功保存五个字段，API access token 请求返回 403。

DragonflyDB 集成验证缓存预扣 7、签到奖励 5、邀请转入 10 后缓存与最终数据库余额均为 108，认证版本不变且系统日志只有一条。签到审计另行分别写入真实 PostgreSQL 和 ClickHouse 隔离日志库，验证重复请求不重复记录。

输出：`/tmp/new-api-wallet-control-build.log`、`/tmp/new-api-wallet-control-tests.log`、`/tmp/new-api-wallet-control-compliance-tests.log`、`/tmp/new-api-wallet-control-race.log`、`/tmp/new-api-wallet-control-dragonfly.log`、`/tmp/new-api-wallet-control-full-tests.log`、`/tmp/new-api-wallet-control-vet.log`、`/tmp/new-api-wallet-control-startup.log`。

## 第四十三批验证（2026-09-06）

本批按“先移动目录”的要求处理，178 个原文件除导入路径外内容一致。所有生产代码的旧 controller/service/model 导入已清除，根目录三个目录均不存在。已拆模块的依赖检查继续生效，业务函数未在本批重写。

验证环境沿用 Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。以下检查全部通过：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./...)
python3 /tmp/verify-new-api-pricing-startup.py
```

完整回归覆盖迁移后包路径、模块依赖约束及真实数据库/缓存。三种日志配置均使用新库启动并重启两次，schema 版本、保留数据和供应商唯一约束检查通过。

输出：`/tmp/new-api-directory-move-build.log`、`/tmp/new-api-directory-move-tests.log`、`/tmp/new-api-directory-move-vet.log`、`/tmp/new-api-directory-move-startup.log`。

## 第四十四批验证（2026-09-06）

本批只做整包目录迁移、Go 引用替换和架构规则/说明更新。已核对全部 400 个移动文件及其他受影响 Go 文件，共 829 个文件除包路径替换外内容一致；Lua 嵌入资源保持原样。根目录八个旧目录及其旧 Go 导入均已清除。

验证环境沿用 Go **1.27.1**、PostgreSQL **18.6**、ClickHouse **26.9.1.762**、DragonflyDB **v1.40.2**。以下检查全部通过：

```sh
GOWORK=off go build -o /tmp/new-api-modular ./cmd/new-api
GOWORK=off go vet ./...
TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55438/new_api_test?sslmode=disable' \
TEST_CLICKHOUSE_DSN='clickhouse://default@127.0.0.1:59000/default' \
TEST_DRAGONFLY_DSN='redis://127.0.0.1:56379/15' \
GOWORK=off make test
(cd relaykit && GOWORK=off go build ./... && GOWORK=off go vet ./...)
python3 /tmp/verify-new-api-pricing-startup.py
```

三种日志配置均通过新库初始化及两次重启，schema 版本、保留数据和供应商唯一性检查通过。主模块 go.mod/go.sum、RelayKit 目录及其独立模块配置没有改动。

输出：`/tmp/new-api-shared-move-build.log`、`/tmp/new-api-shared-move-tests.log`、`/tmp/new-api-shared-move-vet.log`、`/tmp/new-api-shared-move-startup.log`。
