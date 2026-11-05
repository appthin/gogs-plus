# 登录限流、验证码与文件持久化封禁修改方案

本文是设计提案，不是最终实现。上一版方案已按下面的决定修订。

## 修订说明

| 项 | 上一版 | 本版 |
| --- | --- | --- |
| IP 限流默认值 | 开启 | 关闭 |
| 配置段 | `[security]` | 保持 `[security]` |
| 登录验证码 | 未设计 | 新增开关，默认开启 |
| 封禁状态存储 | 内存缓存，可选数据库迁移 | 独立文件持久化，可外部编辑，改动即时生效 |
| 跨重启保留 | 仅 Redis 缓存或数据库方案 | 文件天然保留 |
| 管理员解锁 | 可选 P2 | 后台管理界面一键解封，属于第一阶段 |
| 阈值与时长默认值 | 10 次 / 5 分钟 / 15 分钟 | 不变，不放宽 |
| 数据库迁移 | 可选 v23 | 不需要 |
| 覆盖入口 | Web 登录 | Web 登录、Git HTTP Basic、LFS Basic、Contexter Basic |
| 失败计数存储 | Flamego 缓存 | 进程内共享计数，跨入口合并计算 |

## 1. 目标与范围

需求：

- 可配置的登录限流，IP 维度默认关闭。
- 登录页面验证码，默认开启，可关闭。
- 封禁（IP 与账号）跨重启保留，用文件记录。
- 管理员在后台查看封禁记录文件位置与当前封禁列表，IP 记录要带最后尝试登录的账号，并能一键解封。
- 手工删除或修改封禁文件，封禁状态立刻变化。
- 阈值和时长沿用默认值，不放宽。

本方案覆盖：

- 按客户端 IP 的失败限流与写入文件封禁。
- 按账号的失败锁定与写入文件封禁。
- 登录验证码。
- 封禁文件的读写、外部编辑感知、过期清理。
- 后台封禁管理页与配置页展示。
- 前端登录页的验证码与 429 倒计时。
- Git HTTP Basic 认证、LFS Basic 认证，以及 Contexter 链路里的 Basic 认证。

不覆盖：SSH 登录（无密码认证）、访问令牌滥用防护、注册反垃圾的完整方案、修改密码和删除账号时的当前密码校验。MFA、找回密码、注册等入口的限流放在后续阶段。

## 2. 现状（代码事实）

登录链路：

- 前端登录页 `web/src/pages/user/SignIn.tsx` 向 `POST /api/web/user/sign-in` 提交 `{username, password, loginSource}`，见 `SignIn.tsx:62`。
- 路由注册在 `cmd/gogs/internal/web/webapi.go:124-126`，处理器为 `postUserSignIn`，见 `cmd/gogs/internal/web/webapi_user.go:267`。
- 认证由 `internal/database/users.go:64` 的 `UsersStore.Authenticate` 完成。凭证错误返回 `auth.ErrBadCredentials`，登录来源不匹配返回 `ErrLoginSourceMismatch`。
- 当前没有登录限流、失败计数或封禁逻辑。

验证码：

- Flamego 已挂载验证码中间件 `captcha.Captchaer`（`cmd/gogs/internal/web/web.go:691`），图片路由 `/captcha/image.jpeg`（`web.go:722`），Macaron 桥接 `/captcha/*`（`web.go:549`）。
- 目前只有注册接口使用验证码：`postUserSignUp`（`webapi_user.go:94`），受 `conf.Auth.EnableRegistrationCaptcha` 控制，前端实现在 `SignUp.tsx:260-305`。
- 登录接口和登录页都没有验证码。

封禁现状：

- `ProhibitLogin` 只在 `internal/context/auth.go:31` 的 `Toggle` 里对挂载了 `reqSignIn`/`ignSignIn` 的旧页面生效。`/api/web/*`（`web.go:547`）没有 `Toggle`，`Authenticate` 与 `postUserSignIn` 也都不检查它。被封禁账号仍能登录并调用 `/api/web/*`。
- 不存在任何 IP 封禁或账号锁定机制。

Git HTTP 与 LFS 认证：

- Git HTTP 在 `internal/route/repo/http.go:128-178` 自行解析 `Authorization: Basic`，调用 `store.AuthenticateUser` 校验用户名密码（`http.go:146`）。密码失败后会再用用户名或密码字段尝试访问令牌（`http.go:155-173`），都失败才返回 401 并要求重新输入（`askCredentials`，`http.go:38-41`）。
- LFS 在 `internal/route/lfs/route.go:46-100` 做同样的事：先用户名密码（`route.go:61`），再令牌回退（`route.go:75-94`），最后 `askCredentials`。
- 这两条路径都注册在 `cmd/gogs/internal/web/web.go:569-588`，不经过 `session.Sessioner` 和 `context.Contexter`，所以 Web 登录入口的任何中间件都覆盖不到它们。
- `internal/context/auth.go:222-240` 的 Contexter Basic 分支也只走 `AuthenticateUser`，没有令牌回退。它覆盖的是经过 `Contexter` 的其它 Macaron 路由。
- 关键细节：Git 与 LFS 的令牌回退意味着“密码校验失败”不等于“登录失败”。用访问令牌当密码的客户端每次都会先失败一次再成功，如果把这第一次失败计入限流，正常的令牌用户会被逐步封禁。

缓存与配置：

- Flamego 缓存由 `cache.Cacher` 挂载（`web.go:697`），Macaron 缓存由 `macaroncache.Cacher` 挂载（`web.go:836-840`），两者是不同实例，Redis 布局互不兼容（`cmd/gogs/internal/web/cache.go:81-83`）。
- `github.com/flamego/cache v1.5.1` 只提供 `Get`、`Set`、`Delete`、`Flush`、`GC`，没有原子自增。
- 配置启动时静态读取，`Security` 结构在 `internal/conf/static.go:39-46`，默认值在 `conf/app.ini:160-174`。`checkInvalidOptions`（`static.go:440-501`）对不在默认 `app.ini` 里的键发出警告，新增键必须写进默认文件。
- 路径类配置沿用 `ensureAbs`（例如 `[attachment] PATH`），相对路径基于工作目录解析。

后台界面：

- 后台是服务端模板，不是 SPA。页面模板在 `templates/admin/*.tmpl`，导航在 `templates/admin/navbar.tmpl`，通过 `PageIsAdmin*` 标记选中项；模板用 `//go:embed **/*` 自动打包（`templates/embed.go:18`），新增模板文件不需要改注册表。
- 路由在 `cmd/gogs/internal/web/web.go:141-176` 的 `/admin` 组里注册，处理器在 `internal/route/admin/`。
- 配置页处理器 `admin.Config`（`internal/route/admin/admin.go:184`）把 `conf.Security` 传给模板；展示段在 `templates/admin/config.tmpl:197-214`。

## 3. 方案总览

| 层 | 内容 | 默认 | 状态存放 |
| --- | --- | --- | --- |
| L1 | 按客户端 IP 的失败限流，超限写入 IP 封禁 | 关闭 | 计数在进程内，封禁在文件 |
| L2 | 按账号的失败锁定，超限写入账号封禁 | 关闭 | 计数在进程内，封禁在文件 |
| L3 | 登录验证码（仅 Web 登录页） | 开启 | 会话 |
| L4 | 封禁文件、后台查看与一键解封 | 始终可用 | 文件 |

覆盖的密码入口共用同一个限流器：

| 入口 | 位置 | 触发条件 |
| --- | --- | --- |
| Web 登录 | `cmd/gogs/internal/web/webapi_user.go:267` | 凭证失败 |
| Git HTTP | `internal/route/repo/http.go:146` | 密码与令牌回退都失败 |
| LFS | `internal/route/lfs/route.go:61` | 密码与令牌回退都失败 |
| Contexter Basic | `internal/context/auth.go:229` | 凭证失败 |

核心变化是“计数器在进程内共享、封禁记录在文件”：

- 计数器只保存短窗口失败次数，重启丢失可以接受。放在进程内而不是 Flamego 或 Macaron 缓存里，是因为这两套缓存是不同实例、Redis 布局也互不兼容（`cmd/gogs/internal/web/cache.go:81-83`）。如果各入口各写一份，攻击者在 Web 与 Git 之间交替尝试就能拿到双倍次数。
- 封禁记录写进文件，因此跨重启保留、可被管理员直接查看和编辑、可以一键解封。
- 判定封禁时按文件的最新内容为准，外部删除或修改文件后，下一次请求就能看到变化。

## 4. 配置项设计

全部放在 `[security]`。

| 键 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `ENABLE_LOGIN_RATE_LIMIT` | bool | `false` | 是否按客户端 IP 限制登录失败 |
| `LOGIN_RATE_LIMIT_ATTEMPTS` | int | `10` | 窗口内允许的 IP 失败次数，`<= 0` 关闭该层 |
| `LOGIN_FAILURE_WINDOW` | duration | `5m` | 失败计数窗口，IP 与账号共用 |
| `LOGIN_RATE_LIMIT_BLOCK_DURATION` | duration | `15m` | IP 封禁时长 |
| `ENABLE_LOGIN_LOCKOUT` | bool | `false` | 是否按账号临时锁定 |
| `LOGIN_LOCKOUT_ATTEMPTS` | int | `10` | 账号失败阈值，`<= 0` 关闭该层 |
| `LOGIN_LOCKOUT_DURATION` | duration | `15m` | 账号封禁时长 |
| `ENABLE_LOGIN_CAPTCHA` | bool | `true` | 登录页是否要求验证码 |
| `LOGIN_BAN_FILE` | string | `data/login_bans.json` | 封禁记录文件路径，相对工作目录 |

说明：

- 阈值与时长沿用上一版，不放宽。
- `time.Duration` 由 `ini` 的 `MapTo` 直接解析，已有先例：`[cron.repo_health_check] TIMEOUT = 60s` 映射到 `conf.Cron.RepoHealthCheck.Timeout`（`static.go:163`，`conf.go:416`）。
- `LOGIN_BAN_FILE` 用 `ensureAbs` 转绝对路径，启动时创建父目录。
- `checkInvalidOptions` 依赖默认 `app.ini` 的键集合，9 个键都要写进 `conf/app.ini`。
- 非法值处理：时长为 0 或负数回落到默认并 `log.Warn`；`LOGIN_BAN_FILE` 为空时回落到默认值；`attempts <= 0` 视为关闭对应层。

`conf/app.ini` 默认内容：

```ini
; Whether to limit sign-in failures from a single client IP address.
ENABLE_LOGIN_RATE_LIMIT = false
; Number of failed sign-in attempts allowed within the window before the IP is banned.
LOGIN_RATE_LIMIT_ATTEMPTS = 10
; The time window used to count failed sign-in attempts for both IP addresses and accounts.
LOGIN_FAILURE_WINDOW = 5m
; How long a client IP address stays banned after exceeding the limit.
LOGIN_RATE_LIMIT_BLOCK_DURATION = 15m
; Whether to lock an account after repeated failed sign-in attempts.
ENABLE_LOGIN_LOCKOUT = false
; Number of failed sign-in attempts allowed for one account within the window before it is locked.
LOGIN_LOCKOUT_ATTEMPTS = 10
; How long an account stays locked after exceeding the threshold.
LOGIN_LOCKOUT_DURATION = 15m
; Whether to require a captcha on the sign-in page.
ENABLE_LOGIN_CAPTCHA = true
; The file that stores banned IP addresses and accounts. Edits take effect without a restart.
LOGIN_BAN_FILE = data/login_bans.json
```

## 5. 封禁文件设计

### 5.1 格式

一个 JSON 文档（`.json`），UTF-8，顶层是 `bans` 数组。程序写入时按 `(kind, value)` 排序并用两空格缩进，便于查看和对比。手工编辑时删除数组里的一个对象就是解封，改 `until` 就是改封禁时长。

```json
{
  "bans": [
    {
      "kind": "ip",
      "value": "203.0.113.5",
      "until": 1730003600,
      "created": 1730000000,
      "lastLogin": "alice",
      "reason": "too many failed sign-in attempts"
    },
    {
      "kind": "account",
      "value": "alice",
      "until": 1730003600,
      "created": 1730000000,
      "reason": "too many failed sign-in attempts"
    }
  ]
}
```

字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `kind` | 是 | `ip` 或 `account`，其它值跳过并计入警告 |
| `value` | 是 | IP 层存规范化后的地址（IPv6 存 `/64` 网段），账号层存小写用户名 |
| `until` | 是 | 解封时间，Unix 秒。小于当前时间视为已过期 |
| `created` | 否 | 首次封禁时间，Unix 秒 |
| `lastLogin` | IP 记录使用 | 最后尝试登录的账号，用于后台展示 |
| `reason` | 否 | 例如 `too many failed sign-in attempts` |

规则：

- 同一 `(kind, value)` 出现多次时，最后一条有效记录生效。
- 未知字段忽略，便于以后扩展。
- 缺少 `value`、`until` 非法或 `kind` 未知的对象跳过并计数，不影响同文件里其它有效记录。
- 整个文件 JSON 语法错误时保留上一份有效快照，错误显示在后台页面，不覆盖文件。

### 5.2 加载与外部编辑感知

封禁判定不能依赖启动时的一次性加载，否则手工编辑文件不会生效。文件存储的行为：

1. 每次判定封禁或后台读取列表前，先 `os.Stat` 文件，比较修改时间与文件大小。任一变化就重新解析整个文件并替换内存快照。
2. 文件不存在时快照置空，并在下一次写入时按当前状态重建文件。启动时若文件不存在，会创建一个内容为 `{"bans": []}` 的空文件，便于管理员找到它。
3. 解析失败时保留上一份有效快照，记录错误，不覆盖文件。这样管理员写坏一处不会导致全部封禁失效。
4. 读取路径加进程内互斥，避免并发重载与写入互相覆盖。

由此得到：

- 管理员删除一行或改 `until`，最迟下一次登录请求（或打开后台页面）就生效。
- 管理员可以直接往文件里追加记录来手工封禁一个 IP 或账号，不需要额外界面。
- 进程重启后从文件恢复全部未过期封禁。

### 5.3 写入与并发

- 服务端自己的写入（新增封禁、更新 `lastLogin`、解封）走“加锁 → 先重载一次最新文件 → 修改内存快照 → 原子写回”。
- 原子写回：在同一目录创建临时文件，写入后 `Sync`、`Close`、`Rename` 覆盖目标文件，文件权限 `0600`。Windows 上 `Rename` 在目标被编辑器占用时可能失败，需要短重试，失败则返回错误并在后台页面提示。
- 排序：按 `(kind, value)` 排序输出，保证文件内容稳定，便于 diff。
- 服务端重写文件时保留固定的头部注释与有效记录，手工添加的其它注释会被丢弃。文件是状态文件，不是审计日志，这一点要写进文档。
- 管理员与服务的并发编辑存在很短的竞争窗口。约定文件是唯一事实来源，服务端每次写入前重载一次，把丢失编辑的概率降到最低。后台页面提供“重新加载”按钮，便于管理员手工确认。

### 5.4 过期清理

- 判定与列表都过滤已过期记录。
- 每次写入时顺带清理过期记录。
- 不额外加定时任务。文件里长期无人登录时可能留有已过期行，不影响功能，后台页面只展示有效记录并显示被跳过的过期条数。

## 6. 限流、验证码与封禁流程

### 6.1 计数键

失败计数由 `internal/loginlimit` 自己维护，键与语义如下：

| 键 | 值 | TTL |
| --- | --- | --- |
| `ip::fail::<ip>` | int，窗口内失败次数 | `LOGIN_FAILURE_WINDOW` |
| `account::fail::<sha256>` | int，窗口内失败次数 | `LOGIN_FAILURE_WINDOW` |

账号键对 `sha256(strings.ToLower(strings.TrimSpace(login)))` 取十六进制，避免任意输入造成的键长度和分隔符问题。

计数器实现：`MemoryCounters`，内部是一个带互斥锁与过期时间的 map，`Incr` 读改写在一把锁内完成，天然避免并发丢计数。`Counters` 保留为接口，以后要支持多实例共享时可换成 Redis 实现，不影响调用方。

IP 规范化：`net.SplitHostPort` 取主机部分；IPv4-mapped IPv6 转 IPv4；IPv6 按 `/64` 归并并记录为网段字符串。解析失败时跳过 IP 层。

### 6.2 判定顺序

所有入口遵循同一套顺序：先查封禁，再校验凭证，失败才计数。

```text
Web 登录 POST /api/web/user/sign-in
 1. ip := netx.ClientIP(req, conf.Auth.TrustedProxyCIDRs)
 2. if limiter.Banned(ip, login):        返回 429
 3. if ENABLE_LOGIN_CAPTCHA 且验证码错误: 返回 401，fields.captcha
 4. u, err := Authenticate(...)
 5. if IsErrBadCredentials:
      ban, blocked := limiter.RecordFailure(ip, login)
      if blocked:                              // 内部已写文件封禁，带 lastLogin
          返回 429
      否则返回现有 401
 6. 成功: limiter.ResetAccount(login); completeSignIn(...)
```

Git HTTP 与 LFS 多了令牌回退，因此第 6 步的失败判定要放在回退之后：

```text
Git HTTP / LFS
 1. 解析 Authorization: Basic 得到 login 与 password
 2. if limiter.Banned(ip, login): 返回 429
 3. user, err := AuthenticateUser(login, password)
 4. if err == nil: limiter.ResetAccount(login); 继续原有流程
 5. if IsErrBadCredentials:
      用 login 或 password 再试访问令牌
      if 令牌成功: 继续原有流程          // 不计失败，也不重置
      if 令牌也失败:
          ban, blocked := limiter.RecordFailure(ip, login)
          if blocked: 返回 429
          否则返回原有 401
```

细节：

- 被 IP 或账号封禁时，在验证码校验之前返回 429，避免无谓的验证码与会话操作。
- 只有 `auth.IsErrBadCredentials` 计入失败。`ErrLoginSourceMismatch` 保持现有 422，外部登录源的基础设施错误保持现有 500，都不计数。
- Git HTTP 与 LFS 的“密码失败但令牌成功”不计入失败。用访问令牌当密码是合法用法，计数会把令牌用户逐步封禁。
- Git HTTP 与 LFS 返回 429 时不带 `WWW-Authenticate` 或 `Lfs-Authenticate`，否则客户端会反复弹凭据或在同一请求里重试。
- Contexter Basic 分支（`internal/context/auth.go:229`）同样返回 429。它用 `ctx.Error(http.StatusTooManyRequests, ...)` 写响应，Macaron 的 run 循环在处理器返回后检查 `Written()`，因此后续路由不再执行。
- 验证码错误不计入凭证失败次数，避免用无效验证码把某个 IP 推入封禁。是否需要给验证码失败单独限流留到后续阶段。
- 登录成功只清账号失败计数，不清 IP 失败计数，避免持有有效账号的攻击者用一次成功登录清空 IP 计数。
- 达到阈值的那次请求直接返回 429，前端立刻进入倒计时。触发封禁时清空对应的失败计数，避免管理员解封后下一次失败立刻又触发封禁。
- `ResetAccount` 同时删除账号的失败计数，不动文件里的封禁记录（解封只能过期或管理员操作）。

### 6.3 IP 记录里的最后登录账号

- 阈值触发写入 IP 封禁时，`lastLogin` 写入触发封禁的那个登录名。
- 封禁生效期间收到同一 IP 的登录请求时，内存里更新 `lastLogin`，写入文件做节流：距上次写入超过 60 秒才落盘一次。这样后台能看到较新的尝试记录，也不会被攻击者的高频请求变成磁盘写放大。
- 解封、重新封禁时重新从当次登录名开始记录。

### 6.4 可信代理与客户端 IP

新增 `netx.ClientIP(req *http.Request, trusted []*net.IPNet) string`：

1. 解析 `RemoteAddr` 主机部分，失败返回空字符串并跳过 IP 层。
2. 远端不在 `conf.Auth.TrustedProxyCIDRs` 内时直接返回远端地址，忽略所有转发头。
3. 远端可信时，从右向左扫描 `X-Forwarded-For`，取第一个不在可信 CIDR 内的地址；没有则退回 `X-Real-IP`；再没有退回远端地址。
4. `internal/context/auth.go:255` 的 `isRequestFromTrustedProxy` 可以改为调用同一份 CIDR 判断，避免两处逻辑漂移。

只有可信代理会覆盖而不是透传 `X-Forwarded-For` 时才应配置 `TRUSTED_PROXY_IPS`。

### 6.5 各入口的接入方式

限流器在启动时初始化一次（`loginlimit.Init`），创建文件存储、内存计数器和全局限流器，四个入口共用。`netx.ClientIP` 统一接收底层 `*http.Request`，Macaron 侧传 `c.Req.Request`。

| 入口 | 文件 | 接入方式 |
| --- | --- | --- |
| Web 登录 | `cmd/gogs/internal/web/webapi_user.go:267` | 在 `Authenticate` 前后调用 `Banned`、`RecordFailure`、`ResetAccount`；不再需要注入 Flamego 缓存 |
| Git HTTP | `internal/route/repo/http.go:128-178` | 解码 Basic 之后先查 IP 与账号封禁；令牌回退都失败才 `RecordFailure`；密码校验成功时 `ResetAccount` |
| LFS | `internal/route/lfs/route.go:54-99` | 与 Git HTTP 相同 |
| Contexter Basic | `internal/context/auth.go:222-240` | Basic 分支在 `AuthenticateUser` 前查封禁，失败计数，成功重置 |

Git HTTP 的具体位置：

- 在 `HTTPContexter` 解出 `authUsername`、`authPassword`（`http.go:140`）之后、调用 `AuthenticateUser`（`http.go:146`）之前，插入 IP 与账号封禁检查。命中就设置 `Retry-After` 并 `c.Error(http.StatusTooManyRequests, ...)` 返回，不设置 `WWW-Authenticate`。
- `AuthenticateUser` 成功（`err == nil`）时调用 `ResetAccount(authUsername)`，然后保持现有流程，包括 2FA 提示与权限检查（这两步不是凭证失败，不计数）。
- `auth.IsErrBadCredentials(err)` 时保持现有令牌回退（`http.go:155-173`）。只有回退也失败、即将调用 `askCredentials` 的两个分支才调用 `RecordFailure`。命中封禁阈值时返回 429，否则保持现有 401。
- 公开仓库的匿名拉取（`http.go:111-117`）根本不进入 Basic 分支，不受影响。

LFS 的具体位置：在 `authenticate`（`route.go:46`）解码 Basic 后、`AuthenticateUser`（`route.go:61`）前查封禁；令牌回退失败的两个 `askCredentials` 分支改为先 `RecordFailure`，命中封禁时返回 429 JSON（`responseJSON(w, http.StatusTooManyRequests, ...)`）并设置 `Retry-After`，不设置 `Lfs-Authenticate`。

Contexter 的具体位置：`authenticatedUser` 的 Basic 分支（`auth.go:223-240`）在 `AuthenticateUser` 前查封禁，命中就写 429 并返回 `nil, false, false`；`auth.IsErrBadCredentials` 时 `RecordFailure`，成功时 `ResetAccount`。写响应后 Macaron 不会继续执行路由处理器。

## 7. 验证码

- 开关 `[security] ENABLE_LOGIN_CAPTCHA`，默认开启。现有的 `[auth] ENABLE_REGISTRATION_CAPTCHA` 保持原位不动，登录验证码是独立开关，不改动现有配置语义。
- `GET /api/web/user/sign-in` 响应增加 `captchaEnabled`。
- `POST /api/web/user/sign-in` 请求增加 `captcha` 字段。开启时用注入的 `cpt captcha.Captcha` 调 `ValidText` 校验，失败返回 401，`fields.captcha` 使用现有 `form.captcha_incorrect` 文案，语义与注册接口一致（`webapi_user.go:94-99`）。
- 验证码中间件已在 Flamego 链上，会话适配器也已存在（`webapi.go:56-66`），无需新增中间件。
- 前端把 `SignUp.tsx:260-305` 的验证码区域抽成共享组件（例如 `web/src/components/Captcha.tsx`），登录页与注册页共用。按 `web/DESIGN.md` 的约定，同一模式出现两处时更新该文档。
- 交互：加载后显示验证码图片；任何失败响应后刷新验证码；刷新通过 URL 参数破缓存。
- 无障碍：图片有 `alt`，刷新按钮有可读名称，输入框有 `Label`，错误提示用 `aria-describedby` 关联，与注册页保持一致。
- 说明：验证码默认开启会改变所有 Web 登录用户的体验，也会阻止脚本化的 `/api/web/user/sign-in` 调用。API v1 使用 Basic/Token，不受影响。
- 验证码不是限流的替代品，无法阻止人工或模型识别验证码的攻击者，因此 IP 限流仍然保留为可选开关。

## 8. 接口契约

`GET /api/web/user/sign-in`：

```json
{ "loginSources": [ { "id": 0, "name": "Local", "isDefault": true } ], "captchaEnabled": true }
```

`POST /api/web/user/sign-in` 请求新增可选字段 `captcha`。

响应：

| 情况 | 状态码 | 响应 |
| --- | --- | --- |
| 成功 | 200 | `{}` 或 `{"mfa":true}` |
| 凭证错误 | 401 | `{"error":"...","fields":{"username":null,"password":null}}` |
| 验证码错误 | 401 | `{"fields":{"captcha":"..."}}` |
| 来源不匹配 | 422 | `{"error":"..."}` |
| 被限流或封禁 | 429 | `{"error":"...","retryAfter":900}` 加 `Retry-After: 900` |

- 429 的 `error` 文案区分“IP 尝试过于频繁”和“账号被临时封禁”时仍不暴露账号是否存在：账号计数按输入字符串计算，存在与不存在的账号行为一致。
- 429 响应头通过注入的 `flamego.Context` 设置 `Retry-After`，处理器写响应前的设置会被 Flamego 的 `ReturnHandler`（`web.go:699-716`）保留。
- 命中限流或封禁时用 `log.Warn` 记录 `ip`、规范化登录名、`retry_after`，不记录密码。

Git HTTP 与 LFS：

| 情况 | 状态码 | 响应 |
| --- | --- | --- |
| 未提供凭据 | 401 | 保持现有行为，Git 带 `WWW-Authenticate: Basic`，LFS 带 `Lfs-Authenticate` |
| 密码与令牌都失败，未达阈值 | 401 | 保持现有行为 |
| 密码与令牌都失败，达到阈值或已被封禁 | 429 | 设置 `Retry-After`，不带 `WWW-Authenticate` 或 `Lfs-Authenticate`，LFS 用 JSON 错误体 |
| 权限不足 | 403 或 404 | 保持现有行为，不计数 |
| 2FA 用户使用密码 | 401 | 保持现有提示，不计数 |

## 9. 前端改动

`SignIn.tsx`：

- 从 loader 读取 `captchaEnabled`，开启时渲染共享验证码组件；失败后刷新验证码。
- 响应类型增加 `retryAfter?: number`。
- 非 2xx 分支读取 `body.retryAfter`，缺失时回退到 `Retry-After` 响应头。
- 新增 `lockedUntil` 状态与每秒倒计时，倒计时期间禁用提交按钮，按钮文案显示剩余时间。提示放在现有 `role="alert"` 区域，不依赖颜色传达状态。
- 倒计时结束后恢复可提交，不清空已输入的账号密码。
- 保持现有 tab 顺序与 `autoFocus` 行为，避免移动端键盘体验回退。

新增文案（只改 `conf/locale/locale_en-US.ini`）：

```ini
sign_in_rate_limited = Too many failed sign-in attempts. Please try again in %s.
sign_in_locked = Too many failed sign-in attempts for this account. Please try again in %s or contact the site administrator.
sign_in_retry_after = Try again in %s
```

`web/scripts/extract-locales.mjs` 的 `KEYS` 增加上述键；`web/src/locales/*.json` 由脚本生成，不手工编辑非英文文件。

## 10. 后台封禁管理

### 10.1 新页面

新增 `GET /admin/login-bans`，归属现有 `reqAdmin` 组（`web.go:138`）。处理器放在 `internal/route/admin/login_bans.go`，模板 `templates/admin/login_bans.tmpl`，导航在 `templates/admin/navbar.tmpl` 增加一项，用 `PageIsAdminLoginBans` 标记选中。

页面内容：

1. 文件信息：绝对路径、是否存在、上次加载时间、加载错误（如果有）、被跳过的非法行数、已过期条数。
2. IP 封禁表：IP、解封时间、剩余时长、创建时间、最后尝试登录的账号、原因、操作。
3. 账号封禁表：账号、解封时间、剩余时长、创建时间、原因、操作。
4. 时间列复用现有模板函数 `DateFmtLong`、`TimeSince`（`templates/admin/monitor.tmpl` 已在用）。
5. 操作按钮：每行一个“解封”按钮，POST 到 `/admin/login-bans/unban`；页面另有“重新加载”按钮，POST 到 `/admin/login-bans/reload`。
6. 页面顶部说明：可以删除或修改该文件，改动会被自动感知，无需重启。

### 10.2 解封动作

- `POST /admin/login-bans/unban`：表单字段 `kind` 与 `value`，校验 `kind` 属于 `ip`/`account`、`value` 非空且长度受限；调用存储的 `Unban` 原子改写文件并更新快照；`c.Flash.Success` 后重定向回列表页。
- `POST /admin/login-bans/reload`：强制重载文件，用于管理员手工确认。
- 这些是写操作，使用 POST。仓库当前已移除 CSRF 保护，沿用现有的 POST 加 `reqAdmin` 做法。
- 解封在文件写入后立刻生效：本进程快照已更新，其它进程在下一次判定时通过 stat 感知。

### 10.3 配置页展示

`templates/admin/config.tmpl` 的 security 段（`197-214` 行）增加展示行：三个开关、两个计数阈值、三个时长、封禁文件路径。布尔值沿用 `fa-check-square-o` 模式，时长直接输出，路径用 `<code>`。

## 11. 改动文件清单

第一阶段（P0）：

| 文件 | 动作 |
| --- | --- |
| `internal/loginlimit/loginlimit.go` | 新增。失败计数与阈值判定、`Counters` 接口与 `MemoryCounters`、全局 `Init`/`Default` |
| `internal/loginlimit/banfile.go` | 新增。封禁记录结构、文件存储、加载/重载、原子写入、过期过滤、`lastLogin` 节流 |
| `internal/loginlimit/loginlimit_test.go`、`banfile_test.go` | 新增。假时钟、假计数器、临时文件用例 |
| `internal/netx/netx.go` | 增加 `ClientIP` |
| `internal/netx/netx_test.go` | 增加直连、可信代理、伪造转发头、IPv6 `/64` 等用例 |
| `internal/conf/static.go` | `Security` 结构增加 9 个字段 |
| `internal/conf/conf.go` | `[security]` 映射后做路径与时长回退、`log.Warn` |
| `conf/app.ini` | `[security]` 段增加默认键与注释 |
| `cmd/gogs/internal/web/webapi_user.go` | `postUserSignIn` 接入限流、封禁判定、验证码校验、429 响应、成功重置；`getUserSignIn` 返回 `captchaEnabled`；请求体增加 `captcha` |
| `internal/context/auth.go` | Contexter Basic 分支接入封禁检查、失败计数、成功重置 |
| `internal/route/repo/http.go` | Git HTTP Basic 接入封禁检查；令牌回退都失败才计数；命中封禁返回 429 |
| `internal/route/lfs/route.go` | LFS Basic 接入封禁检查；令牌回退都失败才计数；命中封禁返回 429 |
| `cmd/gogs/internal/web/web.go` | 启动时初始化限流器与封禁文件存储；注册 `/admin/login-bans` 路由 |
| `internal/route/admin/login_bans.go` | 新增。列表、解封、重新加载处理器 |
| `templates/admin/login_bans.tmpl` | 新增。封禁列表页 |
| `templates/admin/navbar.tmpl` | 增加导航项 |
| `templates/admin/config.tmpl` | security 段增加展示行 |
| `conf/locale/locale_en-US.ini` | 新增 429 文案、后台页面与配置页文案 |
| `web/src/components/Captcha.tsx` | 新增。从 `SignUp.tsx` 抽出的共享验证码组件 |
| `web/src/pages/user/SignUp.tsx` | 改用共享组件 |
| `web/src/pages/user/SignIn.tsx` | 验证码、429 倒计时、请求体增加 `captcha` |
| `web/scripts/extract-locales.mjs` | `KEYS` 增加 SPA 新文案 |
| `web/DESIGN.md` | 记录验证码组件这一共用模式 |
| `CHANGELOG.md` | 在 `0.14.5-plus` 下增加 `### Added` 条目，只描述用户可见效果 |

关键接口草案：

```go
package loginlimit

// 失败计数，默认实现是进程内共享的 MemoryCounters。
type Counters interface {
    Incr(ctx context.Context, key string, window time.Duration) (int, error)
    Delete(ctx context.Context, key string) error
}

type Kind string
const (
    KindIP      Kind = "ip"
    KindAccount Kind = "account"
)

type Ban struct {
    Kind      Kind
    Value     string
    Until     time.Time
    Created   time.Time
    LastLogin string
    Reason    string
}

// 封禁记录存储，默认实现是 FileStore。
type Store interface {
    Banned(ctx context.Context, kind Kind, value string) (Ban, bool, error)
    Ban(ctx context.Context, b Ban) error
    Unban(ctx context.Context, kind Kind, value string) error
    List(ctx context.Context) ([]Ban, error)
    Reload(ctx context.Context) error
    Status() Status   // 路径、是否存在、上次加载时间、加载错误、跳过行数、过期条数
}

type Limiter struct { /* counters, store, config, now */ }

// Init 在启动时调用一次，构造文件存储、内存计数器与全局实例。
func Init(opts Options) error
func Default() *Limiter

// Banned 同时检查 IP 与账号，返回命中的记录。
func (l *Limiter) Banned(ctx context.Context, ip, login string) (Ban, bool, error)
// RecordFailure 累加 IP 与账号失败次数，达到阈值时写文件封禁记录。
func (l *Limiter) RecordFailure(ctx context.Context, ip, login string) (ban *Ban, blocked bool)
func (l *Limiter) ResetAccount(ctx context.Context, login string)
```

- `FileStore` 实现 `Store`，构造参数为文件路径与时钟函数，便于测试。
- `MemoryCounters` 实现 `Counters`，用一把互斥锁保护 map 与过期时间，保证并发自增不丢计数。接口保留给以后的多实例实现。
- `Init` 在启动时调用一次，从 `conf.Security` 读取配置；四个入口通过 `limiter.Default()` 使用同一实例，测试用 `New(...)` 注入临时文件与假时钟。

## 12. 边界与安全分析

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 分布式爆破 | IP 限流默认关闭，账号锁定也默认关闭 | 验证码默认开启作为第一道防线；需要时开启两层限流 |
| 验证码可被绕过 | 人工或自动化识别可绕过验证码 | 保留可选的 IP 与账号限流，二者组合使用 |
| 恶意锁定他人账号 | 攻击者故意失败以锁定受害者 | 账号锁定默认关闭；阈值与时长保守；文件可被管理员立即解封 |
| NAT 与共享出口 | 共享 IP 被一起封禁 | 阈值可调；后续可选白名单 |
| IPv6 地址轮换 | 隐私地址绕过单地址限制 | 按 `/64` 归并并记录为网段 |
| 伪造转发头 | 直连时伪造 `X-Forwarded-For` | 只在远端命中 `TRUSTED_PROXY_IPS` 时采信，并从右向左取第一个非可信地址 |
| 文件被误编辑 | 手工编辑出现语法错误 | 解析失败保留上一份有效快照，不覆盖文件，后台显示错误与跳过行数 |
| 文件泄权 | 文件含 IP 与用户名 | 权限 `0600`，只写用户名与 IP，不写密码或其它凭证 |
| 文件并发编辑 | 管理员编辑与程序写入竞争 | 每次写入前重载最新文件；文件是唯一事实来源；后台提供重新加载按钮 |
| 磁盘写放大 | 被封禁 IP 的每次尝试都更新 `lastLogin` | 内存即时更新，落盘最多每 60 秒一次 |
| 计数重启丢失 | 失败计数在进程内存储 | 封禁记录在文件，计数丢失只影响阈值累计 |
| 计数不跨实例 | 进程内计数在多实例部署下各自独立 | Gogs 默认单实例；需要共享时替换 `Counters` 实现，封禁记录本身已经跨进程共享 |
| 令牌回退误计数 | Git HTTP 与 LFS 用访问令牌当密码时会先失败一次 | 只在令牌回退也失败后才计数，见 6.2 |
| Git 客户端收到 429 | git 会把 429 当错误上报，可能提示认证失败 | 429 不带 `WWW-Authenticate`，避免反复弹凭据；若实际客户端兼容性差，可退化为 403 |
| 多实例 | 各进程独立快照 | 外部改动在各自下一次请求感知；程序写入以最后写入者为准。Gogs 默认单实例，文档说明该限制 |
| 账号枚举 | 响应可能暴露账号状态 | 账号计数按输入字符串计算，存在与不存在行为一致；`ErrLoginSourceMismatch` 的现有差异不属本次范围，记录为已知问题 |
| 服务端重写丢注释 | 程序写回只保留有效记录 | 文档明确该文件是状态文件，不是审计日志 |
| `ProhibitLogin` 缺口 | 被封禁账号仍能登录 `/api/web/*` | 与本功能独立，建议单独提交补上登录接口检查 |

## 13. 测试计划

单元测试：

- `internal/loginlimit`：未达阈值放行；达到阈值返回写入指令；成功登录只清账号计数；计数器出错时失败开放；并发计数不丢失；IP 与账号共用同一个窗口。
- 各入口的调用收敛到 `Default()` 上的三个方法，HTTP 层难以直接单测，改为手工验证令牌回退路径与跨入口封禁共享。
- `internal/loginlimit` 文件存储：首次创建与加载；外部修改后重载；外部删除文件后视为无封禁；非法行跳过且保留旧快照；`until` 过期过滤；原子写入不产生半截文件；`lastLogin` 节流；`Unban` 立即生效；文件权限为 `0600`；同一 `(kind,value)` 后写覆盖前写。
- `internal/netx`：`ClientIP` 的直连、可信代理、伪造多段 XFF、`X-Real-IP`、IPv4-mapped IPv6、IPv6 `/64`、`RemoteAddr` 不可解析。
- `internal/conf`：新键默认值与非法值回退。`Security` 新增字段会改变 golden 文件，重新生成 `internal/conf/testdata/TestInit.golden.ini`，命令为 `go test ./internal/conf -run TestInit -update TestInit`。

手工验证：

- 默认配置：登录页出现验证码，验证码错误被拒绝；登录成功与失败行为正常。
- `ENABLE_LOGIN_CAPTCHA = false` 后验证码消失。
- 打开 `ENABLE_LOGIN_RATE_LIMIT`，连续错误登录触发 429，文件里出现该 IP 记录且带 `lastLogin`。
- 打开 `ENABLE_LOGIN_LOCKOUT`，账号维度触发 429，文件里出现账号记录。
- 服务运行中手工删除文件里的某一行，不重启，下一次登录立刻放行。
- 服务运行中手工把某行 `until` 改小，不重启，解封立即生效。
- 重启服务，未过期封禁仍然存在。
- Git HTTP：用错误密码反复 `git ls-remote` 触发 429，且该 IP 打开 Web 登录页同样被拒；用访问令牌（放在用户名或密码字段）反复操作不应累计失败次数。
- LFS：用错误密码反复请求返回 429 JSON；令牌路径不计数。
- 后台 `/admin/login-bans` 显示路径、状态与两类列表，IP 行显示最后尝试登录的账号，一键解封后 Web 与 Git 都立刻放行。
- 反向代理下取到真实 IP，直连伪造头无效。
- `moon run gogs:lint`、`go test ./internal/loginlimit/... ./internal/netx/...`、`moon run web:lint`、`moon run web:build`。

## 14. 分阶段落地

P0，本方案主体：

- 配置项、默认 `app.ini`、后台配置页展示。
- 失败计数限流器、内存计数器与文件封禁存储。
- 登录验证码（默认开启）与共享组件。
- Web 登录、Git HTTP Basic、LFS Basic、Contexter Basic 四个入口接入判定、封禁、失败计数与 429。
- 后台封禁管理页与一键解封。
- 前端倒计时与文案。
- CHANGELOG 与反向代理文档补充。

P1，扩展到其它入口：

- `POST /api/web/user/mfa` 与 `/mfa/recovery` 按会话中的待验证用户限流。
- `POST /api/web/user/reset-password` 与 `/sign-up` 按 IP 限制请求频率。
- 修改密码、删除账号与删除组织时的当前密码校验（`internal/route/user/setting.go:209`、`:711`、`internal/route/org/setting.go:103`）。

P2，可选增强：

- 失败到一定次数后强制验证码，或验证码失败单独限流。
- 封禁文件定期压缩清理的定时任务。
- 账号被锁定的邮件通知。
- 封禁文件白名单（`LOGIN_RATE_LIMIT_EXEMPT_IPS`）。

## 15. 已确认决定

1. IP 限流默认关闭，账号锁定默认关闭。
2. 配置项全部放在 `[security]`。
3. 登录页验证码默认开启，保持全局开关，不做渐进模式。
4. 封禁记录使用文件持久化，默认 `data/login_bans.json`，路径可配置；外部编辑在下一次判定时生效。
5. 阈值与时长沿用默认值，不放宽：10 次 / 5 分钟窗口 / 15 分钟封禁。
6. 被封禁 IP 的 `lastLogin` 内存即时更新，落盘最多每 60 秒一次。
7. Git HTTP 与 LFS 被限流时返回 429，不带 `WWW-Authenticate` 或 `Lfs-Authenticate`。
8. Contexter Basic 分支同样返回 429。
9. Git HTTP、LFS 与 Web 登录共用同一套阈值。
10. 后台提供封禁列表、文件路径、一键解封与“手工新增封禁”按钮。

## 16. 实现状态

本方案已实现，包括后台手工新增封禁。

| 模块 | 文件 |
| --- | --- |
| 限流器、内存计数器、`Ban` 辅助方法、`Ban`/`ListBans`/`Unban`/`ReloadBans`/`BanStatus` | `internal/loginlimit/loginlimit.go` |
| 文件封禁存储（JSON、原子写入、外部编辑感知、`lastLogin` 节流） | `internal/loginlimit/banfile.go` |
| 单元测试 | `internal/loginlimit/loginlimit_test.go`、`banfile_test.go` |
| 客户端 IP | `internal/netx/netx.go` 与测试 |
| 配置项与默认值 | `internal/conf/static.go`、`internal/conf/conf.go`、`conf/app.ini` |
| Web 登录、验证码、429 | `cmd/gogs/internal/web/webapi_user.go` |
| 启动初始化与后台路由 | `cmd/gogs/internal/web/web.go` |
| Git HTTP、LFS、Contexter Basic | `internal/route/repo/http.go`、`internal/route/lfs/route.go`、`internal/context/auth.go` |
| 后台封禁页（列表、解封、重新加载、手工新增） | `internal/route/admin/login_bans.go`、`templates/admin/login_bans.tmpl`、`templates/admin/navbar.tmpl`、`templates/admin/config.tmpl` |
| 前端验证码与倒计时 | `web/src/components/Captcha.tsx`、`web/src/pages/user/SignIn.tsx`、`web/src/pages/user/SignUp.tsx`、`web/scripts/extract-locales.mjs` |
| 文案 | `conf/locale/locale_en-US.ini` |
| 变更日志与文档 | `CHANGELOG.md`、`docs/fine-tuning/reverse-proxy.mdx` |

自动化验证：

- `go test ./internal/...` 全部通过，包含 `loginlimit`、`netx`、`conf`、`context`、`route/repo`、`route/lfs`。
- `golangci-lint run --timeout=30m` 返回 `0 issues.`，`goimports -l` 和 `gofmt -l` 对所有改动文件无输出。
- `moon run web:lint` 与 `moon run web:build` 通过。

关于 `moon run gogs:lint`：它依赖的 `gogs:install` 会执行 `go generate ./...`，而 `go-mockgen` 在当前树上的输出与已提交内容存在既有漂移（`internal/database/mocks_test.go` 与 `internal/route/lfs/mocks_test.go` 会因导入限定名不同而变动，并触发 import cycle）。该漂移在未改动的 HEAD 上同样能复现，与本次改动无关。因此本地执行顺序是：先 `go generate ./...`、还原这两个 mock 文件、再跑 `golangci-lint run`。CI 的 lint job 不执行 `go generate`，不受影响。

端到端验证（本地 SQLite 实例，阈值临时调低）：

| 场景 | 结果 |
| --- | --- |
| 错误密码达到阈值 | 前两次 401，第三次起 429，带 `Retry-After` 与 `{"error":...,"retryAfter":...}` |
| 封禁文件内容 | 出现该 IP 记录并带 `lastLogin` |
| 删除或清空文件 | 不重启，下一次请求立即 401 而非 429 |
| 手工追加账号封禁 | 不重启立即 429，且只影响该账号 |
| 账号锁定层（IP 层关闭） | 达到阈值返回 429，其它账号不受影响 |
| 管理员页面 | 文件路径、存在状态、加载时间、跳过与过期计数、两类列表、IP 行的最后尝试账号均正常渲染 |
| 手工新增封禁 | 合法输入写入文件；非法时长与非法 IP 被拒绝 |
| 一键解封 | 302 重定向后文件移除该记录，登录立即恢复 |
| Git HTTP | 错误密码第三次起 429，`Retry-After` 存在，无 `WWW-Authenticate` |
| Git HTTP 令牌回退 | 用访问令牌当密码连续 6 次全部 200，封禁文件保持为空 |
| LFS | 错误密码第三次起 429 JSON，无 `Lfs-Authenticate` |
| Contexter Basic | 被封禁 IP 时 `/explore/repos` 返回 429，清空文件后恢复 200 |
| 验证码 | `captchaEnabled` 为 true，缺验证码返回 401 与 `fields.captcha`，图片端点正常 |

验证后用 `-c` 指向的临时工作目录、临时数据库与生成的二进制均已删除，仓库只保留代码与文档改动。

P1 与 P2 的扩展项保留在上一节。
