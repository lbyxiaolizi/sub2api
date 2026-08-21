# 审计 issue 记录

> 审计日期:2026-08-09
> 范围:main 分支近期变动(075f38179 同步 upstream/main 至 0.1.172 带入的 80 个上游提交、296 文件 ~1.6 万行)+ proxy-pool 提交线。
> 方法:分块并行深审 + 人工核查合并冲突解决与迁移机制;发现均对照当前代码验证。
> 适用分支标注:[feature] = feat/proxy-pool-upstream 与 main 共有(同一份 proxy-pool 代码),两边都要带; [main] = 仅 main(上游修复尚未合入 feat 分支)。

---

## 高危

### H1. 账号转换器漏映射 pool_id —— 任何账号编辑都会静默清掉池绑定
- 位置:`backend/internal/repository/account_repo.go:3328` `accountEntityToService` 复制了 `ProxyID` 等字段但漏了 `PoolID`(全文件仅写入处 `:524-527` 引用 PoolID)。
- 适用:[feature] / main
- 后果链:`UpdateAccount`(`backend/internal/service/admin_account.go:672`)先 `GetByID` 加载 → `PoolID` 恒为 nil → 除非请求显式重发 `pool_id>0`,更新时走 `ClearPoolID()` 分支,数据库绑定被抹掉。改名、刷新凭据、OAuth 重登、批量操作等任何无关编辑都会解绑。
- 副作用:`enrichPoolNames`(`admin_account.go:26-73`)因 PoolID 恒 nil 成为永久 no-op,管理接口永远不返回账号的 `pool_id`/`pool_name`,前端池标识显示不出来。
- 注:提交 6c6210a74 声称修了"entity→service 遗漏池字段",只修了 `proxyEntityToService`(`proxy_repo.go:606-609`,已验证存在),没修 account 转换器。

### H2. 创建账号时 pool_id 从不落库
- 位置:`backend/internal/repository/account_repo.go:92-172` `createAccountRecord` 的 builder 设了 `ProxyID` 但没有 `SetPoolID`;`CreateAccount`(`backend/internal/service/admin_account.go:617-633`)只在 `assignPoolHealthyProxy` 找到健康代理时才调 `accountRepo.Update` 顺带落库,且该 Update 错误被 `_ =` 吞掉(`:631`)。
- 适用:[feature] / main
- 后果:新建池(健康状态 unknown)或池内全不健康时,绑定静默丢失;代码注释承诺"无健康代理时留空,由池服务后续补齐"永远无法兑现——`ListPoolUnassignedAccountIDs` 按 `a.pool_id` 匹配,而 `pool_id` 从未写入。

### H3. 前端编辑弹窗"清除池绑定"永远不生效
- 位置:`frontend/src/components/account/EditAccountModal.vue`(main 行号 `:4433-4436`):清除池选择产生 `pool_id === null` → 提交时 `delete updatePayload.pool_id` → 后端把缺失视为"未变更"(`admin_account.go:840`)。
- 适用:[feature] / main
- 后果:管理员对池绑定账号点 X 清除、保存,成功提示后 UI 显示无绑定,实际绑定还在。目前不存在任何能单独解绑池的操作路径(只能通过改选具体代理间接置 0)。

---

## 中危

### M1. /v1/responses 图片请求的 400 放大未堵住
- 位置:`backend/internal/service/ratelimit_service.go:2083-2094`(main):守卫对非 `/v1/images/*` 入口的 gpt-image 请求跳过 plan-gated 冷却。
- 适用:[main](02fbcbe3a 引入的守卫)#5391
- 后果:`/v1/responses`/WS 显式生成图片(`gpt-image-1`,`openAIResponsesRequiredCapability`)请求在 plan-gated 账号池上每次确定性 400 并遍历整个池,跨请求无刹车——正是该修复为 `/v1/images/*` 解决的同型问题在另一入口的残留。异步图片任务安全(重入 Images handler,`openai_images.go:147` 已置标志)。

### M2. Grok 405 无任何负缓存
- 位置:`backend/internal/service/openai_gateway_upstream_errors.go:214`(main):405 对 openai+grok 全网关可 failover,但无任何账号/端点状态记录。
- 适用:[main](#5392 e79429b03)
- 后果:若某端点对所有账号都 405(如全 Grok 账号打 `/v1/responses`),每个请求遍历全池 405,跨请求持续。粘滞会话锁死已修复(failover 时 rebind);端点级负缓存被提交说明显式推迟。

### M3. proxy-pool 批量分配负载均衡失效
- 位置:`backend/internal/service/proxy_pool_service.go:198-210`:healthy 列表循环前排序一次,内层循环首个成功即 break(恒取 `healthy[0]`),`counts[p.ID]++` 从未被重新用于排序。
- 适用:[feature] / main
- 后果:100 个未分配账号全部压到同一个代理,与文档承诺的"负载均衡到健康代理"相反。

### M4. CreateProxyPool 无法 honor auto_rebind=false
- 位置:`backend/internal/service/admin_proxy_pool.go:106,117-119`:仅 `if input.AutoRebind { pool.AutoRebind = true }`,显式 false 被忽略。handler(`proxy_pool_handler.go:126-128`)正确转发 false 但 service 不采用。
- 适用:[feature] / main

### M5. 手动 rebind 绕过 leader 锁 + 请求上下文内跑探测
- 位置:`backend/internal/service/admin_proxy_pool.go:244` 直接调 `poolService.RunPool`,后台巡检用 `tryAcquireSingletonLeaderLock`(`proxy_pool_service.go:87`)串行化。
- 适用:[feature] / main
- 后果:手动触发可与巡检/另一管理员并发跑同一池:探测写交错、双重 `RebindAccountsOffProxy` 可能把账号移到对方刚标记不健康的代理。探测 15s×并发 4(`proxy_pool_service.go:28,244`),40 代理的池可挂住 HTTP 请求约 150s;ctx 取消中途留下部分健康更新。

### M6. 池绑定账号的任何保存都会重跑代理分配
- 位置:`frontend/src/components/account/EditAccountModal.vue:3360` + `backend/internal/service/admin_account.go:847-852`:只要提交带非空 pool_id 就走 `assignPoolHealthyProxy` 覆盖 `account.ProxyID`。
- 适用:[feature] / main
- 后果:编辑无关字段(名称、备注、并发)也可能把账号换到另一个代理。当前被 H1 掩盖(后端不返回 pool_id);修 H1 时必须连带评估。

---

## 低危

### L1. create-account 端点先查邮箱存在性、后验证码
- 位置:`backend/internal/handler/auth_oauth_pending_flow.go:1737-1758` vs `:1763`(main):邮箱存在性查询与 choice 状态切换发生在 `VerifyCaptcha` 之前;同流程的 `send-verify-code` 是先验证码(:572)。持有 pending 会话者可低速枚举已注册邮箱(需真实 OAuth 往返,吞吐受限)。

### L2. legacy complete-registration 端点在 Turnstile 模式下无验证码
- 位置:`auth_linuxdo_oauth.go:514`(dingtalk:703 / oidc:617 / wechat:497)(main):`VerifyActionCaptchaIfEnabled`(`auth_service.go:471-474`)在非腾讯/阿里云 provider 下返回 nil,handler 也不调 `VerifyCaptcha`。有邀请码兜底。

### L3. 前端账号导入流程丢失池选择
- 位置:`CreateAccountModal.vue:5472`(Grok SSO)/ `:5678`(Codex session)/ `:5756`(Codex PAT):池选择器可见,但载荷只带 `proxy_id` 不带 `pool_id`,且选池会置空 `proxy_id`——账号创建后既无池绑定也无代理,无任何提示。
- 适用:[feature] / main

### L4. CreateAccountModal resetForm 不重置 pool_id
- 位置:`CreateAccountModal.vue:4690-4703`:重置了 proxy_id 等 30+ 字段但漏 pool_id。创建账号 A 绑池 3 后,再开弹窗建 B 时 B 仍带池 3 绑定。

### L5. ProxyPoolsView 状态筛选无"全部"选项
- 位置:`ProxyPoolsView.vue:22-28,404-407`:statusOptions 仅 active/disabled 且 Select 不可清除;兄弟页 ProxiesView 含 `{ value:'', label: allStatus }`。筛选一次后不刷新页面看不到全量列表。

### L6. "选择无代理"也顺带解绑池
- 位置:`EditAccountModal.vue:2765-2766` / `CreateAccountModal.vue:3695-3697`:任何 `update:model-value` 都设 `pool_id=0`,含选"直连/无代理"选项;与提示文案("仅选具体代理才清绑定")不符。

### L7. proxy-pool 各处小问题
- `admin_account.go:619,843`:`s.poolRepo.GetPoolByID` 无 `nil` 守卫(同文件其它池方法都有);生产 wiring 恒提供,风险低。
- `admin_account.go:845-848`:分配出错时账号保留旧手动代理却获得 pool_id,错误静默;依赖巡检自愈,建议加日志。
- `proxy_pool_service.go:340-345`:死分支(`target.ID == pp.ID` 不可达)。
- `admin_proxy_pool.go:189-196`:AssignProxiesToPool 不校验池存在,坏 id 表现为 FK 500 而非 404;DeleteProxyPool 对不存在 id 返回成功。
- `proxy_pool_repo.go:104-117`:BoundAccountSum 仅按 proxy_id 关联,已绑池未分代理的账号不计数(展示口径)。

### L8. 前端其它
- `ProxyPoolsView.vue:441-443`:搜索 debounce 空函数(`setTimeout(() => {}, 200)`)。
- `ProxyPoolsView.vue:596-599`:assign 弹窗可静默把其它池的代理改走,列表无池归属标识。
- `ProxyPoolsView.vue:417`:"Updated" 列渲染原始 ISO 时间串,未用 formatDateTime。

---

## 已验证可靠的部分(无 action)

- **#5345 OAuth pending 账号接管修复**:完整。修复位于 5 个 provider 共用的 exchange 端点;密码绑定、TOTP 续期、4 个 complete-registration 端点均无法从 choice 状态滥用;会话单次(ConsumedAt)、10min 过期、state/PKCE/nonce 校验齐全。
- **#5406 OpenAI OAuth 路由提示**:无注入面。header 先无条件剥离再由服务端账号状态合成(`openai_routing_hint.go:19-61`),不在两个 header 白名单,在 ApplyHeaderOverrides 之后执行;legacy beta header 移除仅 OAuth 分支,API-key 账号有回归测试覆盖。
- **#5398 流内降载 pre-output failover**:可靠。零客户端字节后才重试,`c.Writer` 尺寸变化会阻止 retry,无 SSE 重复/交错路径;重试有界(~(retry+1)×(switch+1),无冷却但每请求有界)。
- **#5399 Responses→Anthropic content block 修复**:逻辑健全,不产生空 content,不破坏 tool_use/tool_result 配对;低危残留:空白文本 user 块仍 400、全过滤后空消息仍 400(均非回归)。
- **#5383 工具 schema null-type 修复**:字节拼接方案核实过 gjson 偏移语义,最坏退化为 no-op 不会写错位置;嵌套 schema 未覆盖是显式范围。
- **#5396 上游响应模型审计**:两个新字段纯观测性,不影响计费口径(定价输入仍用 `UsageLog.Model`/`UpstreamModel`);无增长问题;dashboard 三态过滤与缓存键正确。
- **nanoid 3.3.16→3.3.17**:仅前端 lockfile,postcss 构建期依赖,后端 Go 无关。
- **迁移编号重复**(192/193/194/195 各两个文件):无害。执行器按文件名追踪+排序、不按编号;相关迁移均幂等(IF NOT EXISTS)。
- **合并质量**:64 个手工冲突解决;独立 worktree `go build ./...` 通过,service/repository/handler/apicompat 测试全过;前端合并相关 5 个 spec 60 用例全过。

---

## 附带提醒(非缺陷)

- 上游近期修复(#5398/#5399/#5396/#5406/…)**尚未合入 feat/proxy-pool-upstream**:分支上 `responses_to_anthropic_request.go:187-195` 仍是修复前代码。下次合并 upstream/main 时,apicompat 目录有 4 个未提交本地文件改动,合并会冲突需小心处理。
- H1/H2/H3 与 M3-M6 在 feat 分支与 main 是同一份代码,修复时两边都要带(或先合并统一再修)。

## 建议补的测试

- 集成往返:带 `pool_id` 在"无健康代理"的池上创建账号 → `GetByID` → 无关 `UpdateAccount`,可同时暴露 H1/H2。
- `CreateProxyPool` 传 `auto_rebind:false` 断言落库值(M4)。
- `assignUnassigned` 多健康代理 + 多账号,断言分布(M3)。
