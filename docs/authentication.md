# 用户鉴权与登录会话

面板鉴权采用短期 Access Token、HttpOnly Refresh Cookie 与服务端登录会话控制面的组合。面板请求不再依赖 Gin session，也不再要求 `New-Api-User` 请求头。

## 鉴权模型

- Access Token 是有效期 15 分钟的 JWT，只保存在浏览器内存中，通过 `Authorization: Bearer <token>` 发送。
- Refresh Token 是随机不透明值，有效期最长 30 天。浏览器只通过 `HttpOnly`、`SameSite=Strict` Cookie 持有它；服务端仅保存 HMAC 摘要，并在每次刷新时轮换。
- `new_api_has_session` 是 Refresh Cookie 的会话提示，值恒为 `1`，`Path=/`、非 `HttpOnly`，与 Refresh Cookie 同时写入、同时清除、同一过期时间。它只声明"曾签发过 Refresh Cookie"，不含任何凭据，也不参与任何鉴权判定；伪造它唯一的效果是自费一次注定失败的 refresh。它存在的原因是 Refresh Cookie 被 `HttpOnly` 和 `Path=/api/user/auth` 双重限制，`/` 上的页面无法判断自己是否匿名，否则每次冷启动都要发一次注定 401 的 refresh，而该请求还会占用按 IP 计数的 `CriticalRateLimit` 配额。
- `user_sessions` 是登录会话控制面，记录设备、IP、登录方式、最后活跃时间、到期时间和撤销状态。数据库中的 Session 状态是最终权威；撤销传播速度取决于下文所述的 Redis 拓扑。
- 用户的密码、状态、角色或安全因子发生安全相关变化时，`auth_version` 会递增并使旧登录会话失效。订阅带来的分组升降级只刷新授权缓存，不会退出任何登录设备。
- Redis 缓存保存用户鉴权快照和登录会话快照。版本栅栏和撤销 tombstone 防止旧缓存重新授权；Session 快照使用跟随 `SYNC_FREQUENCY` 的短 TTL，缓存未命中或未启用 Redis 时回退到数据库校验。

`SESSION_SECRET` 用于派生 Access Token、Security Proof、Refresh Token 摘要和 AuthFlow 摘要的不同用途密钥。生产环境及多节点部署必须在所有节点配置相同的高强度随机值；更换该值会使现有登录、临时鉴权流程和 Security Proof 全部失效。

## 多节点 Redis 拓扑

多节点部署必须共用同一主数据库。登录 Session、账户级活跃 Session 上限和签发窗口计数都以数据库为权威，因此这些限制在应用节点间全局生效。Redis 中的 Session Hash（包含 `revoking`/`revoked` tombstone）只是缓存，其 TTL 为 Session 剩余寿命与有效 `SYNC_FREQUENCY` 中的较小值；`SYNC_FREQUENCY` 默认及非法值回退均为 `60` 秒。读取缓存不会续期，过期后会按 SID 回源数据库。延迟完成的 active 缓存回写只能使用其数据库观察窗口尚未消耗的 TTL，不能在撤销 tombstone 到期后重新启动一个完整缓存周期。

| Redis 部署方式 | Session 状态传播 | 限流语义 |
| --- | --- | --- |
| 所有节点共享 Redis | 正常撤销和版本发布通过同一缓存即时传播 | Redis 限流额度在所有节点间共享 |
| 每个节点使用独立 Redis | 最迟在该节点 Session 缓存 TTL 到期后回源收敛，即不超过有效 `SYNC_FREQUENCY`；版本轮换期间，新 Token 在持有旧缓存的节点上可能短暂返回 401 | 每个节点独立计数，集群总额度最坏约为单节点阈值乘以节点数 |
| 不使用 Redis | 每次 Session 校验直接读取数据库 | 使用各节点的内存限流器，额度同样按节点独立 |

`SYNC_FREQUENCY` 越大，独立 Redis 部署的陈旧窗口越长；值越小，每个活跃 SID 在每个节点上回源数据库的频率越高。默认配置下，持续活跃的 Session 每个节点最多约每 60 秒增加一次数据库主键点查。共享 Redis 时，撤销 tombstone 和版本发布仍保持即时传播。

所有节点必须使用相同的 `SESSION_SECRET`。当多个节点连接同一个 Redis 时，还必须使用相同的 `CRYPTO_SECRET`，否则节点生成的缓存键摘要不一致，无法正确共享缓存。上述保证只覆盖登录 Session 鉴权的有界陈旧语义；限流额度及其他 Redis 缓存仍会受到 Redis 拓扑影响，不能据此认为整个控制面与拓扑无关。

## 浏览器接口

登录成功后，密码登录、2FA、Passkey、OAuth、WeChat 和 Telegram 登录均返回统一数据：

```json
{
  "success": true,
  "data": {
    "access_token": "...",
    "token_type": "Bearer",
    "access_expires_at": 1730000000,
    "user": {},
    "session": {
      "sid": "...",
      "current": true,
      "login_method": "password",
      "ip": "...",
      "user_agent": "...",
      "created_at": 1730000000,
      "last_active_at": 1730000000,
      "expires_at": 1732592000
    }
  }
}
```

会话相关接口：

| 接口 | 鉴权 | 用途 |
| --- | --- | --- |
| `POST /api/user/auth/refresh` | Refresh Cookie；Secure 模式附加 Origin 校验 | 轮换 Refresh Token 并签发新的 Access Token |
| `POST /api/user/auth/logout` | Refresh Cookie；Secure 模式附加 Origin 校验，可同时携带 Bearer | 撤销当前登录会话并清除 Cookie |
| `GET /api/user/sessions` | Bearer | 查看当前鉴权版本的有效登录会话，当前会话优先，最多 100 条 |
| `DELETE /api/user/sessions/:sid` | Bearer | 撤销指定登录会话，包括当前会话 |
| `POST /api/user/sessions/revoke-others` | Bearer | 保留当前会话并撤销其他会话 |

客户端内存中已有会话时，应在 refresh/logout 请求中发送 `X-Auth-Session: <sid>`。Refresh Cookie 与该 SID 不一致时，两个端点都返回 `409 AUTH_SESSION_MISMATCH`，且不会轮换、撤销或清除任何会话；客户端先通过 refresh 清除本标签页的旧 SID、恢复 Cookie 当前对应的会话，再重试 logout。冷启动尚无内存会话时可以省略该请求头。

并发使用同一个 Refresh Token 时，服务端通过确定性轮换恢复同一个后继 Token，多个浏览器标签页不会因丢失“胜者”响应而被迫退出。最近一代 Refresh Token 在短暂容错窗口结束后再次出现会撤销对应会话；无法识别的更早代或随机 Token 只会被拒绝，不会允许攻击者凭猜测踢掉会话。

前端使用 Web Locks 串行化同一浏览器配置文件中的刷新，并通过 BroadcastChannel（不支持时回退到 `storage` 事件）仅同步会话标识和登录/退出事件；Access Token 与 Refresh Token 都不会通过跨标签页消息传递或持久化到 Web Storage。

前端将冷启动状态与登录状态分开管理。网络或服务端临时故障允许后续导航重试 refresh；服务端确认 Refresh Cookie 无效时才进入已完成的匿名状态。内存 SID 与 Cookie SID 不一致时，客户端清除旧内存身份并在不携带旧 SID 的情况下重试一次。

公开页面的冷启动会先读 `new_api_has_session`：提示不存在且内存中没有任何身份时跳过 refresh，直接按匿名渲染，且**不**把这次跳过记为已完成的匿名判定——跳过只是延后，不是服务端结论。会依据鉴权结果做跳转的位置（受保护路由与登录页）不看提示，内存为空时一律回源。因此提示缺失但 Refresh Cookie 有效的用户（该 Cookie 上线前建立的会话，或只清理了 `/` 站点数据的浏览器）会在公开页显示为匿名，并在进入上述任一位置时自动恢复登录态，不需要重新输入密码。提示因服务端撤销而过期时，那次 refresh 返回 401 并在同一响应里清除提示，浪费的请求只发生一次。

## Session 签发限额与保留策略

服务端在所有登录方式的统一 Session 签发出口执行两级账户限制：

- `USER_SESSION_ACTIVE_LIMIT`（默认 `50`）：单用户未过期且状态为 active 的 Session 上限。达到上限时新登录返回 `409 AUTH_SESSION_LIMIT`。
- `USER_SESSION_ISSUANCE_LIMIT`（默认 `100`）和 `USER_SESSION_ISSUANCE_WINDOW_SECONDS`（默认 `86400`）：统计窗口内该用户创建的所有 Session，包含已撤销和旧鉴权版本的记录。达到上限时返回 `429 AUTH_SESSION_ISSUANCE_LIMIT`。
- 这两次计数与插入不加跨数据库锁；极端并发登录可能出现少量超额，但计数失败会拒绝签发，不会降级放行。

升级时已经超过活跃上限的账户不会被自动下线或挤掉旧会话；限制只作用于后续的新 Session 签发。

`USER_SESSION_REVOKED_RETENTION_DAYS`（默认 `7`）控制 revoked 行的审计保留期。签发计数依赖窗口内的行仍存在，因此签发窗口不得超过 revoked 保留期。如果配置超出，启动时会记录告警并将实际窗口钳制到保留期，避免提前删除 revoked 行导致限流计数被低估。

定时清理即使发现 `expires_at` 已过期，也不会删除 `created_at` 仍落在实际签发窗口内的行；尚未达到 revoked 保留期的撤销记录同样会继续保留。这样在扩大配置窗口时，过期清理不会静默削弱签发计数或审计保留。

活跃数量会计入状态仍为 active 但 `user_auth_version` 已过期的异常残留行，而设备列表只展示当前鉴权版本。因此遇到 `AUTH_SESSION_LIMIT` 时，应优先在仍已登录的设备上执行“撤销其他会话”，该操作会同时清理不可见的旧版本 active 行；没有可用设备时可使用密码重置撤销所有会话。密码重置不会清空签发窗口计数。

仅 master 节点每小时分批删除过期 Session 和超过保留期的 revoked Session。`USER_SESSION_HOURLY_ALERT_THRESHOLD`（默认 `5000`）只在最近一小时全局签发量异常时记录告警，不会形成可被滥用的全站登录拒绝开关。

## Refresh/Logout 的 Origin 校验

refresh/logout 的 Origin 防护与 Refresh Cookie 的 Secure 模式绑定：

- 未配置 `SESSION_COOKIE_SECURE` 或显式设为 `false` 时，Refresh Cookie 可用于本地 HTTP，refresh/logout 的 OriginGuard 关闭，并且不得配置 `SESSION_COOKIE_TRUSTED_URL`。这使 `http://localhost` 上不同端口的 Rsbuild/Vite 开发代理可以正常转发请求。该模式仅用于可信的本地开发环境，不应暴露到公网。
- `SESSION_COOKIE_SECURE=true` 时，Refresh Cookie 仅通过 HTTPS 发送，同时启用严格 OriginGuard。`POST /api/user/auth/refresh` 和 `POST /api/user/auth/logout` 会校验浏览器的 `Origin`；缺少 `Origin` 时只接受合法的单一 `Referer` 作为回退。允许来源包括请求自身的精确 Origin，以及 `SESSION_COOKIE_TRUSTED_URL` 中配置的精确 Origin。

Secure 模式的 Origin 校验不信任客户端直接发送的 `X-Forwarded-Proto`。TLS 在反向代理终止时，应将面板的公开 HTTPS Origin 明确写入 `SESSION_COOKIE_TRUSTED_URL`。

`SESSION_COOKIE_TRUSTED_URL` 现在具有明确的新语义：它是 refresh/logout Cookie 端点的可信 Origin 列表，不是 CORS 白名单。配置规则如下：

- 仅在 `SESSION_COOKIE_SECURE=true` 时配置；多个值用英文逗号分隔。
- 每项必须是精确的 HTTPS Origin，例如 `https://panel.example.com` 或 `https://panel.example.com:8443`。
- 不接受通配符、路径、查询参数、用户信息或域名后缀匹配。
- 不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。浏览器使用 `sk-` key 直连 relay 的场景保持不变。

本地 HTTP 开发示例（OriginGuard 关闭）：

```env
SESSION_SECRET=<local-random-value>
SESSION_COOKIE_SECURE=false
# SESSION_COOKIE_TRUSTED_URL 不得设置
```

生产 HTTPS 示例（OriginGuard 开启）：

```env
SESSION_SECRET=<high-entropy-random-value>
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://panel.example.com,https://admin.example.com
```

该开关只控制面板 Refresh Cookie 和 refresh/logout 的 OriginGuard，不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。

## 可信代理与 IP 限流

Gin 默认会信任所有代理提供的客户端 IP 请求头。本项目改为兼顾常见反代拓扑和公网直连安全的三态配置：

- 未配置、空字符串或纯空白的 `TRUSTED_PROXIES` 默认信任 `127.0.0.0/8`、`::1`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16` 和 `fc00::/7`，并输出启动告警。该默认值覆盖同机 Nginx、Docker Compose 和常见内网反代；公网直连地址不在列表中，其伪造的 `X-Forwarded-For` 会被忽略。
- `TRUSTED_PROXIES=none`（大小写不敏感且必须单独使用）启用严格直连模式，不信任任何代理，`ClientIP()` 只使用 TCP 直连地址。
- 其他非空值按英文逗号解析为代理 IP/CIDR，并完全替代默认列表。应填写反向代理自身的地址而不是客户端网段；非法 CIDR、空列表或将 `none` 与其他值混用都会阻止服务启动。

Gin 只在请求的直连来源属于可信代理时解析客户端 IP 请求头，并从转发链右侧向左寻找首个非可信地址。因此常见 Nginx `$proxy_add_x_forwarded_for` 链中的公网客户端地址会阻止更左侧的伪造前缀生效。默认信任私网的残余风险是：能够从同一私网直接访问应用的其他机器或容器仍可伪造这些请求头；需要消除此风险时应使用 `none` 或配置精确代理地址。

Redis 限流使用原子 Lua 固定窗口，替代旧的近似滑动窗口 List 实现。这是有意的语义变化：窗口边界两侧可分别打满一次，极短时间内通过量最高约为配置值的两倍。例如 `20 次/20 分钟` 在边界可通过约 40 次。帐户级 Session 上限和签发窗口继续控制数据库增长；如未来需要严格抑制边界突发，需单独迁移为 ZSET 滑动窗口。

用户级模型成功请求限流仍使用原有 Redis List 近似滑动窗口，但列表时间戳统一写为 UTC。滚动升级期间，旧节点写入的本地时间字符串和新节点写入的 UTC 字符串无法从格式上区分，可能在一个模型限流窗口内临时误放行或误拒绝。所有节点升级完成并经过一个完整窗口后会自然收敛；本次升级不会切换 Key 或主动删除现有列表。

开放注册仍会受 Critical IP 限流保护，但分布式 IP 多账号攻击不能仅靠 IP 限流阻止。公网开放注册的部署应同时启用 Turnstile 和邮箱验证；更强的设备或多维风控需作为独立安全项目设计。

## PAT 调用契约

`User.AccessToken`（面板 PAT）继续支持 `Authorization: Bearer <pat>`，也兼容原有的单值 `Authorization: <pat>`。`New-Api-User` 不再参与鉴权，外部脚本不需要再发送 Bearer 与用户 ID 双请求头。这是有意的调用契约简化；旧 PAT 本身无需重新生成。

PAT 不是浏览器登录会话，不能调用登录会话管理接口，也不能签发绑定具体登录会话的 Security Proof。

## 临时鉴权流程与二次验证

OAuth state、2FA pending、Passkey ceremony、Telegram bind 等临时状态存放在 `auth_flows`。客户端只持有随机 `flow_token`，数据库仅保存 HMAC 摘要；流程具有用途、provider、intent、用户和登录会话绑定，并且只能原子消费一次。OAuth 注册的 affiliate code 也随登录 AuthFlow 保存。

标准 OAuth 绑定回调由 popup 通过同源 `postMessage` 交给 opener；只有 opener 使用自身内存中的 Bearer 调用后端绑定接口。Telegram 绑定先由已登录前端创建绑定 AuthFlow，再让 widget 回调携带路径中的 `flow_token`，回调时会重新确认原登录会话仍有效。Telegram 的已签名 widget assertion 也会登记为一次性凭据，重复回放会被拒绝。

敏感操作使用有效期 5 分钟的 `X-Security-Proof`：

- `channel.key.read`：查看渠道密钥；
- `passkey.register`：注册 Passkey；
- `passkey.delete`：删除 Passkey。

Proof 同时绑定用户、登录会话、用户鉴权版本、会话版本和 scope，不能跨用户、跨会话或跨用途复用。

启用了 2FA 的用户注册 Passkey 时，register begin 与 finish 都必须携带有效的 `passkey.register` Proof；finish 会在消费一次性 AuthFlow 之前重新验证 Proof。未启用 2FA 的首次 Passkey 注册不要求该请求头。

## 升级注意事项

- 旧 `session` Cookie 不再使用；升级后现有面板登录会失效，用户需要重新登录。
- 数据库迁移会新增 `user_sessions`、`auth_flows`、`external_identity_claims` 和 `users.auth_version`，并为已有用户初始化鉴权版本、回填 Telegram 账号唯一归属；若历史数据中同一 Telegram ID 已绑定多个用户，迁移会拒绝继续启动，需先消除歧义。
- 数据库迁移会为 Session 签发计数和分批清理新增索引；已有 `user_sessions` 很大时应为首次启动预留维护窗口。
- `user_sessions.previous_refresh_hash` 会从定长 `char(64)` 迁移为 `varchar(64)`。应用会兼容读取历史定长字段留下的空格填充；迁移后的目标结构必须保持幂等，连续启动不应反复执行列类型变更。
- 仅 master 节点定时清理过期登录会话、超过配置保留期的 revoked 会话和已过保留期的 AuthFlow。
- 未配置 `TRUSTED_PROXIES` 时会兼容信任回环和常见私网代理；使用公网负载均衡器、`100.64.0.0/10`、链路本地地址或自定义 CNI 网段的部署仍需显式配置。需要严格忽略所有转发头时设置为 `none`。
- Redis 限流从近似滑动窗口改为原子固定窗口，存在明确的边界双倍突发语义。
- 用户级模型成功请求限流的 UTC 时间戳在滚动升级期间存在一个窗口的混合格式过渡，期间可能临时误放行或误拒绝。
- 自建客户端应按新的 AuthBundle、`flow_token` 和 Security Proof 契约升级；PAT 客户端可直接移除 `New-Api-User`。

## 自定义模型调用密钥派生与分享

这是非官方扩展，数据库列和业务元数据使用 `custom_` 前缀。原 `POST /api/token/` 保持原有行为和 `{ "success": true, "message": "" }` 响应，不写入订单和手机号、不生成分享。

新增 `POST /api/token/derive`：通过 `Authorization: Bearer <PAT>` 或控制台会话，复制当前用户已禁用的模型调用密钥，生成新的可用密钥。无需 `New-Api-User`；模型调用密钥本身不能认证管理接口。

| 请求字段 | 规则 |
| --- | --- |
| `source_token_id` | 必填，当前用户拥有且状态为禁用的密钥 ID；其他用户、已删除、不存在或其他状态均拒绝 |
| `custom_order_no` | 必填且非空，最多 128 UTF-8 字节；去除首尾空白、区分大小写、保留前导零 |
| `custom_phone` | 必填且非空，最多 32 字节；7–15 位数字，允许开头的 `+`、空格、括号、连字符；未验证的联系方式，不要求唯一 |
| `name` | 可选，传入则覆盖名称，最多 50 字节；空字符串也会覆盖 |
| `valid_days` | 可选，非负整数，从派生时计算有效天数；0 为永久。不传则继承源密钥原到期时间，不重新计算 |
| `amount` | 可选，0–1,000,000,000 的整数，按 `amount × QuotaPerUnit` 转为额度，沿用平台严格额度转换和舍入，溢出拒绝；传入时关闭无限额度，0 表示零额度 |

复制源密钥的额度、无限额度标记、模型限制、IP 限制、分组、自动分组和跨组重试配置；上述可选参数仅覆盖对应值。新密钥 ID 和 key 重新生成，状态设为可用，创建/访问时间设为当前时间，已用额度归零，独立生成分享码；源密钥不变。金额只设置密钥限额，不充值用户账户。不传有效期或金额会继承源值，因此已到期或零额度的源配置需要调用方按需覆盖，状态启用并不绕过模型调用时的额度和到期校验。

订单号在平台内全局唯一，包含软删除密钥；用户硬删除清理密钥后释放订单占用。数据库直接在 `custom_order_no` 上建立唯一索引（MySQL 使用 `utf8mb4_bin`，SQLite/PostgreSQL 区分大小写）。并发重复订单返回 HTTP 409、`success: false`、`Order number already has an API key`，不返回已有密钥。订单及手机号仅在派生时写入，普通密钥更新不能更改订单绑定。仅保留 `custom_order_no`、`custom_phone`、`custom_share_code` 三个存储字段，不增加产品表或未上线中间方案的兼容迁移。

```http
POST /api/token/derive
Authorization: Bearer <PAT>
Content-Type: application/json

{
  "source_token_id": 123,
  "custom_order_no": "001Order",
  "custom_phone": "+86 13800138000",
  "name": "customer-order",
  "valid_days": 30,
  "amount": 10
}
```

成功响应为 `{ "success": true, "message": "", "data": ... }`，保留此前扩展创建接口的响应字段：

| 字段 | 含义 |
| --- | --- |
| `id`, `name`, `key` | 新密钥 ID、名称和完整密钥；`key` 不附加 `sk-` |
| `status`, `expired_time` | 当前有效状态及 Unix 秒级到期时间；`-1` 为永久 |
| `remain_quota`, `used_quota`, `unlimited_quota` | 额度计数与无限额度标记；单位为平台 quota |
| `api_addresses` | 配置的模型 API 地址列表；未配置时前端使用当前站点地址 |
| `models` | 与 `/v1/models` 共用逻辑，按分组、模型限制和计费配置筛选的模型列表 |
| `custom_phone`, `custom_order_no` | 完整联系手机号和规范化后的订单号，仅认证后的派生响应返回 |
| `custom_share_code`, `custom_share_url` | 自动生成的 8 位大小写敏感分享码和 `/ck/{share_code}` 链接 |

派生响应为创建时快照；分享查询读取当前状态、额度和模型列表。额度沿用转发缓存，异步批量落库时数据库快照不保证最新。

`.env` 的 `CUSTOM_TOKEN_SHARE_BASE_URL=https://keys.example.com` 配置分享页域名；为空时使用系统 `ServerAddress`，均无有效地址则返回相对路径。该配置只改变分享链接，不改变模型 API 地址。独立分享域名须将 `/ck/` 和 `/api/custom/token-share` 路由到本应用。修改环境配置后重启服务。

分享页为 `/ck/{share_code}`，免登录并适配手机。页面会展示完整密钥（复制时带 `sk-`）、脱敏手机号（`****` 加后四位数字）、接口地址、密钥额度、有效期、状态和可用模型；不展示订单号、用户资料或账户总余额。持有链接即可取得密钥并使用其权限，分享链接应当作为凭据保管。

通过派生接口创建时自动开启分享；原创建接口不生成分享。所有者可以在密钥行菜单的“API 密钥分享”中复制、关闭或重新生成已有分享：

- `POST /api/token/:id/custom-share`：读取当前分享链接。
- `PUT /api/token/:id/custom-share`：重新生成，旧分享码立即失效。
- `DELETE /api/token/:id/custom-share`：关闭分享，不删除模型调用密钥。

这些管理操作均验证当前用户归属，不能为原本没有分享资格的密钥开启分享。分享页内部通过 `POST /api/custom/token-share` 查询，只接收 JSON 中的 `custom_share_code`，不接受 URL 查询参数作为凭据。该数据入口限制匿名请求体和访问频率，返回 `no-store`。分享仅存储一个 `custom_share_code` 字段：使用密码学安全随机数生成 8 位大小写字母与数字，数据库按区分大小写的规则建立唯一索引。关闭分享时在已存码前加 `-` 标记撤销；此标记不能用于访问，保留其再次生成资格，无需另加状态列。普通列表不披露分享码。应用日志隐藏 `/ck/` 后的凭据，页面发送 `Referrer-Policy: no-referrer`；反向代理与 APM 也应脱敏该路径。管理操作的审计记录仅包含动作和密钥 ID，不记录手机号、完整密钥或分享凭据。


禁用/删除/过期的密钥或被禁用/删除的所有者不再允许分享访问。额度耗尽仍可查看额度。关闭或重置分享不能撤回已经复制的模型调用密钥；需要阻断模型调用时应禁用或删除密钥。页面刷新失败时隐藏旧详情；已经送达客户端的信息无法远程收回。

安全设计参考 [OWASP ASVS 5.0.0](https://github.com/OWASP/ASVS/tree/v5.0.0)、[Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html) 和 [Authorization Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html)。本扩展的验证范围为服务端归属校验、PAT/模型密钥隔离、不可预测凭据、撤销/轮换/到期失效、无缓存披露和审计去敏；不构成整站 ASVS 合规声明。生产部署必须使用 HTTPS，并让网关/APM 排除凭据请求体、响应体及 Authorization 头。

### 本扩展验证记录（2026-09-24）

实际数据库：SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 15.19。三种数据库均验证全新建库，以及从发布版 `v1.0.0-rc.40` 的 Token 表结构升级；连续迁移及后续迁移无 DDL、原数据与索引保留、并发订单唯一性、软删除占用和硬删除释放均通过。审计单独日志库回归也通过。

使用隔离的临时数据库运行（以下密码仅用于已销毁的本地测试容器）：

```sh
AUDIT_MYSQL_DSN='root:custom-key-test-only@tcp(127.0.0.1:23306)/mysql?parseTime=true' \
AUDIT_POSTGRES_DSN='postgres://postgres:custom-key-test-only@127.0.0.1:25432/postgres?sslmode=disable' \
go test ./controller -run 'TestCustomTokenDatabaseMatrix|TestCustomShareAccessLogRedactsCode|TestCustomShareURLConfiguration|TestAPITokenAuditDatabaseMatrix|TestAddToken|TestListModels|TestGetModelListGroups' -count=1 -v
go build ./...

cd web
bun run test src/features/keys/__tests__/custom-share.test.tsx src/features/keys/components/__tests__/api-key-listing.test.tsx src/features/keys/components/__tests__/api-keys-mutate-drawer.test.tsx src/features/keys/components/__tests__/api-addresses.test.tsx
bun run typecheck
bun run build
```

2026-09-24 的后端数据库回归、后端构建及前端 39 项测试均通过。前端 typecheck、生产构建及以下浏览器检查沿用 2026-09-23 的验证结果（本次仅更新手机号测试数据）：另用模拟接口在真实浏览器的 375px 宽度检查匿名分享页：长密钥、手机号和长模型名正常换行，无横向溢出；浏览器检查未使用真实用户凭据。
