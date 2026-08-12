# Security Fixes

### 1. Proxy credentials and pending OAuth session identifiers exposed in errors and debug logs

状态：已修复。

深入复核确认：

- 管理员配置的代理 URL 可包含用户名和密码。畸形 URL 进入统一代理解析器后，`url.Parse` 的错误文本包含完整输入，原实现又将该文本返回给调用方，可能把代理凭据带入 API 错误或上层日志。
- HTTP upstream 的 TLS 指纹调试日志记录了完整代理 URL 和包含代理 URL 的缓存键；Gemini OAuth 调试日志记录了完整代理 URL 与待处理 SessionID。这些值包含认证信息或 bearer-like 临时标识。

本轮修复：

- 畸形代理 URL 统一返回稳定的 `invalid proxy URL`，不再包含底层解析错误或原始输入。
- HTTP upstream 日志使用仅保留 `scheme://host` 的代理标签，并移除包含完整代理 URL 的缓存键日志；无法安全解析的已配置代理仅记录为 `configured`。
- 删除 Gemini OAuth 的完整代理 URL 与 SessionID 调试日志。
- 增加回归测试，验证畸形 URL 错误及日志标签不会包含用户名、密码、路径或查询参数。

残余边界：

- 代理认证信息仍保留在内存缓存键中，用于隔离不同凭据对应的客户端连接池；修复仅禁止这些键进入日志。
- 其他非代理 URL 的日志与第三方库内部日志不在本次修复范围内。

### 2. Legacy OAuth complete-registration routes bypassed the configured CAPTCHA

状态：已修复。

深入复核确认：

- LinuxDo、OIDC、WeChat 与 DingTalk 的 legacy `complete-registration` 路由可以直接创建 OAuth 用户。
- 当 Cloudflare Turnstile 启用时，原实现只调用面向动作入口的校验函数；该函数按设计不覆盖 Turnstile，因此持有有效 pending OAuth 浏览器会话的请求可跳过站点配置的注册 CAPTCHA。

本轮修复：

- 四个请求载荷统一接收 Turnstile、腾讯验证码及阿里云复用的证明字段，并在任何用户创建或身份绑定写入前调用完整 `VerifyCaptcha`。
- 四个回调页面统一渲染 CAPTCHA 控件、提交证明，并在每次提交结束后重置一次性证明。
- 增加四 provider 表驱动回归测试：缺失证明时拒绝且用户数保持为零，合法证明通过后才创建用户；前端组件和 API 序列化也有对应测试。

残余边界：

- 攻击者仍必须先完成真实 OAuth 往返并持有匹配浏览器 cookie；本修复只关闭完成注册阶段的 CAPTCHA 缺口。
- CAPTCHA 的服务端有效性、一次性语义与风险判定继续由已配置供应商负责。

### 3. Invalid invitation codes were written to application logs

状态：已修复。

深入复核确认：

- 邮箱注册查询邀请码失败时会把用户提交的完整邀请码写入日志；仍可使用的邀请码属于注册凭据，不应进入集中日志或日志导出。

本轮修复：

- 保留失败原因用于运维诊断，但从日志参数中移除原始邀请码。

残余边界：

- 邀请码仍按业务需要出现在注册请求和数据库中；该修复仅收紧日志边界。
