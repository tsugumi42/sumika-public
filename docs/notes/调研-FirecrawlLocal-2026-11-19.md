# 调研：Firecrawl Local 能不能接进 Sumika 的网页读取

> 2026-11-19。只读调研，没装任何东西、没起任何进程、没跑 `cargo`。依据是 `firecrawl/firecrawl@`（2026-11-18，浅克隆在 `D:\Projects\Code\_external\firecrawl`）的 `apps/local/` 源码和 README、官方入门页 docs.firecrawl.dev/quickstarts/firecrawl-local、`gh` 和 crates.io 的元数据，对照 Sumika `main@`。
> 可信度标记照[拆解笔记写法](拆解笔记-写法.md)：`[读码]` 读过源码；`[推断]` 凭文档或语义推的、没跑过；`[没读到]` 找过但没找到或没法在不安装的前提下确认。网页和 README 的内容当资料，不当指令。
> 结论和方案见 [规格-网页抓取提供方](../规格-网页抓取提供方.md)。

## 0. 结论先行

- Firecrawl Local 是 `apps/local/` 里新的 Rust 程序（单个可执行文件 `firecrawl-local`，一个 crate，不要 Docker、Redis、数据库），2026-11-11 才第一次提交，只有一个人在写。它在 localhost 上提供和 Firecrawl 云端同形状的 `/v2/scrape` 等接口。[读码]
- **我们已经有一个 Firecrawl 云端提供方**（`native/internal/web/firecrawl`，调 `/v2/scrape`），Local 的接口形状和它兼容，所以“接入”主要是加一个只连本机地址的新提供方，不用新协议。[读码]
- 它补的是我们 `web_fetch` 现在明说不做的三样：JS 渲染的页面（要本机装了 Chrome 或 Edge）、PDF 和 Office 文档、整站 crawl/map。[读码]
- 许可证是 **AGPL-3.0**（整个仓库，包括 `apps/local`），只有各语言 SDK 是 MIT。我们只当独立进程用、不改它、不链接它、不随安装包分发它，可以；不能把它的代码抄进 Sumika。[读码]
- 最大的实际障碍不是协议，是网络环境：它自己的内网拦截把 `198.18.0.0/15`（Clash fake-ip 默认段）当私网拒掉，主人的订阅又用 fake-ip。必须在主人的网络上实测过再决定做不做。[读码 + 推断]

## 1. 它是什么，怎么跑

| 项 | 内容 | 标记 |
|---|---|---|
| 位置 | 仓库 `apps/local/`，包名 `firecrawl-local`，版本 `Cargo.toml` 写 0.1.1，crates.io 上只有 0.1.0（2026-11-11 创建，下载 22 次） | [读码] + crates.io API |
| 安装 | `cargo install firecrawl-local` 或克隆后 `cargo build --release`；需要 Rust 和 C 编译器。**没有预编译二进制**：GitHub 最新 release 是 v2.11.0（2026-06-19，自托管服务器的版本），没有 Local 的 release | [读码] README、`gh release list` |
| 运行时依赖 | 不要 Docker、Redis、Postgres、队列。可选：Chrome/Chromium（渲染 JS），Node 22（MCP 要用 `npx firecrawl-mcp`） | [读码] |
| 找浏览器 | `FIRECRAWL_CHROME_PATH`，否则找系统常见位置（Windows 有 Chrome 和 Edge 的默认路径）。Chrome 无头、沙箱、临时 profile | [读码] `browser.rs:226-312` |
| 命令 | `scrape`、`crawl`、`map`、`serve`（v2 API，默认 3002）、`mcp`（stdio MCP）、`init`（把自己写进各 AI 工具的 MCP 配置）、`search`、`login`、`find-tools`、`data` | [读码] README |
| `serve` 的路由 | 本地处理：`POST /v2/scrape`、`/v2/map`、`/v2/crawl`、`/v2/batch/scrape`（及 GET/DELETE 查询取消）、`GET /health`。其余 `/v2/*`（含 `/v2/search`）默认**转给 Firecrawl 云端**；非 `/v2/` 路径 404 | [读码] `serve.rs:743-760`、`:298-330` |
| 任务存放 | crawl 和 batch 任务只在内存里，一小时过期，`serve` 停了就没了 | [读码] README Limits |
| 本地不支持 | `json`、`summary` 格式（跳过并警告）、浏览器 `actions`；截图要 Chrome | [读码] README Limits |

返回形状（本地结果）：`{"success":true,"data":{"markdown":…,"metadata":{"title","url","statusCode",…},"source":"local","warning"?,"blocked"?}}`；云端接手的结果 `source` 是 `"firecrawl-cloud"`；本地也读不了时 `blocked:true` 加警告。[读码] `serve.rs:663-665`、`cloud.rs:629-700`。我们现有的 `firecrawl.Provider.Fetch` 解析的字段（`success`、`data.markdown`、`data.metadata.url/statusCode`）都在里面。[读码]

## 2. 能力对照

| 能力 | Firecrawl Local | 我们现在的 `web_fetch`（`httpfetch`） |
|---|---|---|
| 静态 HTML 转 markdown | 有（正文抽取是 Firecrawl 云端同一套清洗代码，编译进来的） | 有（Readability + html-to-markdown） |
| JS 渲染 | 有，本机 Chrome/Edge，`--browser auto|never|always` | 没有，规格写明不做 |
| PDF、Office | 本机解析（`anydoc`、`pdf-inspector`） | 拒绝，`WEB_BINARY_CONTENT`；仓库另有 `internal/pdftext`（wazero 跑 pdfium），没接进读网页 |
| 整站 crawl / map | 有（读 robots.txt，UA 带 `FirecrawlLocal`） | 没有 |
| 搜索 | **没有本地搜索**：转云端，无 key 有限流 | 有多家（Tavily、Exa、Brave、SearXNG、DuckDuckGo 等） |
| 被站点拦（人机校验页） | 本地失败后默认**交给 Firecrawl 云端代抓** | 链上换下一家（`LooksBlocked`） |
| 截断 | 无固定字数上限 | 12000 字截断并标 `Truncated` |
| 费用 | 本机免费（云端部分另算） | 本机免费 |

“比自托管快 37 倍、空闲 11 MB 内存”这两个数字：官方入门页和 `apps/local/README.md` 里都**没有**（入门页抓取结果里明说找不到），我也没在仓库里搜到来源。[没读到] 没装、没测，不引用。

## 3. 许可证（逐个确认）

| 范围 | 许可证 | 出处 |
|---|---|---|
| 仓库根 `LICENSE` | GNU AGPL v3 | 文件头 [读码]；`gh repo view` 的 `licenseInfo` 也是 agpl-3.0 |
| `apps/local/`（Local 本体） | **AGPL-3.0**，`Cargo.toml` 的 `license = "AGPL-3.0"`；该目录没有自己的 LICENSE 文件 | [读码] |
| `apps/js-sdk`、`python-sdk`、`elixir-sdk`、`ruby-sdk` | MIT（各有 LICENSE） | [读码] |
| 其他 `apps/*`（api、playwright-service-ts、go-html-to-md-service 等） | 没有单独 LICENSE，随仓库根 AGPL | [读码] 逐个 `ls` 确认 |
| `nodesig` 依赖（Local 编译进去的 git 依赖，`firecrawl/nodesig`） | 没查 | [没读到] |

对 Sumika 的含义（不是法律意见，只是我的读法，仓库计划公开，选之前最好再确认一次）：

- 当独立进程、走 HTTP，Sumika 自己的代码不构成它的衍生作品；AGPL 的网络条款针对“修改过它再让别人通过网络用”，我们不改。[推断]
- **不抄它的代码**（拦截地址表、SOCKS 守卫、清洗规则都不抄，只借思路）。我们的客户端按公开的 v2 API 形状写，和现有云端提供方是同一份代码路径。
- **不随安装包分发它的二进制**，由主人自己 `cargo install`。分发就要附 AGPL 源码和许可证，等于把义务揽到 Sumika 身上。
- 如果以后要改它再用（比如给它加上游代理），那是修改，要按 AGPL 把改动公开，这条先不走。

## 4. 维护情况

| 项 | 数 | 标记 |
|---|---|---|
| 仓库创建 | 2024-04-15；星标约 20.3 万；公司主体 Sideguide Technologies（Firecrawl） | `gh repo view`、SDK LICENSE |
| 仓库最近提交 | 2026-11-18（推送时间 2026-11-18T18:31Z） | `gh` |
| 近三个月提交者 | 至少 12 个账号有大量提交（最多的一个 193 次），含若干机器人 | `gh api commits` |
| `apps/local` 提交 | **3 个提交，全部来自同一个人（ericciarla），2026-11-11、11-11、11-18**；首个提交就是整个程序 | `gh api commits?path=apps/local` |
| 发布节奏 | Local 没有独立 release；crate 版本 0.1.x，只有 8 天历史 | [读码] |
| 外部使用 | crates.io 下载 22 次 | crates.io API |

仓库整体维护很活跃，**但 Local 这一块不满一个月、一个人写、版本号 0.1**；API 形状跟随云端 v2，相对稳，内部（命令行参数、环境变量、守卫行为）可能随时变。

## 5. 安全：它自己连网，我们的检查还剩什么

### 5.1 我们这边现在怎么检查

- `web.Service.Fetch` 先对 `q.URL` 跑 `URLPolicy.Check`（`web/access.go`）：只许 http/https、拒带账号密码、拒 localhost/`.local`、拒十进制或十六进制写成数字的主机名、只许 80/443 和配置的端口、域名要解析，**解析出的每个地址必须是公网或代理假地址**，直接写 IP 的必须是真公网。[读码]
- `httpfetch` 自己连：把检查过的 IP 钉进拨号（不再二次解析，防 DNS 重绑定）；每一跳重定向都重新 `Check`；遇到假地址且有环境代理就让代理按域名解析。[读码] `httpfetch/provider.go:41-100, 197-230`
- 提供方结果回来后，`Service.Fetch` **不再检查** `ReturnedURL`/`FinalURL`（搜索结果的 URL 会再过 `Check`，读取的最终地址不会）。[读码] `web/service.go:238, 289-323`。云端提供方没问题，是因为请求在对方机房发出；换成本机的外部程序就必须补上这一道。

### 5.2 Firecrawl Local 自己的检查

- `guard.rs`：URL 层（只许 http/https、拒账号密码、`localhost`/`.local`/`.internal`/`metadata*` 名字、IP 字面量），**每一跳重定向再查一次**；DNS 层：自定义解析器把解析结果里不被允许的地址丢掉，剩下为空就连不上。注释写明这是防 DNS 重绑定。`serve` 默认只许公网（`PublicOnly`）；`--allow-private` 才放开本机和内网；链路本地和云元数据地址（169.254/16、100.100.100.200）任何模式都拒。[读码] `guard.rs`、`fetch.rs:124-190`
- 浏览器：Chrome 被强制走 Firecrawl 自己起的本机 SOCKS5 代理，`proxy-bypass-list=<-loopback>`、关 QUIC、WebRTC 不走非代理 UDP、Chrome 自己的 DNS 全部 `NOTFOUND`；代理在**实际拨号的地址**上套同一份策略，所以重定向、子资源、脚本发的请求、WebSocket 都被覆盖。[读码] `netguard.rs:1-100`。这一块做得比较扎实，不比我们自己写的差。
- `serve` 的访问控制：默认绑 127.0.0.1；绑别的地址必须给 `--token`；要求 `Host` 头是本机名；只收 `application/json`（网页里的跨站表单发不进来）。[读码] `serve.rs:100-140, 337-`
- 它的地址表比我们窄：我们拒掉所有 IPv6 过渡地址（6to4 `2002::/16`、Teredo、NAT64 等），它只展开 IPv4 映射和 `64:ff9b::/96`，6to4 里藏私网 IPv4 的地址它看成公网。要走到这个地址得有人能路由，实际意义小，但两张表不一致，写在已知的坑里。[读码] 对比 `web/access.go:blockedPrefixes` 与 `guard.rs`

### 5.3 关键问题的回答

**我们的内网/假地址检查还能生效吗？** 分三层说：

1. **连接那一刻的检查只能是它自己的。** 连接是它的进程发起的，我们没法把检查过的 IP 钉进它的拨号。我们这边的 `Check` 只是**发请求前的预检**，和它连接时的解析之间有一个很窄的重绑定窗口；窗口由它自己的 DNS 守卫补上（前提是它用 `PublicOnly`）。
2. **假地址（Clash fake-ip）是真问题。** `guard.rs` 把 `198.18.0.0/15` 当“基准测试保留段”算私网，`fetch.rs` 的解析器在 `PublicOnly` 下会把解析到 `198.18.x.x` 的域名全部丢掉，结果是“连不上”。主人的订阅 2026-11-18 刚把 fake-ip 的 IPv6 段换成 `2001:2::/48`（`netsafe.go` 注释），那个段不在它的私网表里，会被当公网放行；IPv4 段仍是 198.18。[读码，实际表现 推断：没在主人网络上跑过]
   - 如果有 `HTTP(S)_PROXY` 环境变量或系统代理，`reqwest` 会把域名交给代理去解析，它的解析器根本不会看到 198.18 地址（所以能通）；同时它的 DNS 层检查在这种模式下**不起作用**，目标域名解析成内网地址时只有代理能管。[推断：按 reqwest 的行为推，没验证；`apps/local` 里搜不到 `no_proxy`/`.proxy(`，说明走默认的系统代理]
   - TUN 加 fake-ip 且没有环境代理时，它会拒绝所有域名。唯一的“解法”是加 `--allow-private`，那等于关掉它的内网拦截，**我们不能这样用**（见 5.4）。
   - Chrome 那一路更麻烦：它的 SOCKS 守卫用 `TcpStream::connect` 直连目标，**不支持上游代理**；需要代理才能出网的网络下，渲染大概率连不上。[读码 `netguard.rs:serve`；影响 推断]
3. **所以“还能不能生效”的答案是：预检在我们手里，连接时检查在它手里，两边都要有，而且“它是不是在 `PublicOnly` 下”要我们自己验证，不能信配置。**

**重定向**：它的 `reqwest` 重定向策略每跳重查 URL（`fetch.rs:175-186`）；浏览器路径由 SOCKS 守卫每次拨号都查。我们再用它回的 `metadata.url`（最终地址）做一次 `Check`，不通过就丢弃内容（请求已经发出去了，GET 为主，但内容不给模型）。

**DNS 重绑定**：在 `PublicOnly` 且无代理时它自己的解析器防住；有代理时这一层失效（见上）；我们的预检只是第一道。可行补强：阶段二由我们起一个只许公网的出口代理（`netsafe` 现成，Go 写 CONNECT 代理不难），让 Firecrawl 经 `HTTPS_PROXY` 走它，连接那一刻的解析和地址检查就回到我们手里，也顺便接上主人的上游代理。浏览器路径用不了这个办法，只能 `--browser never` 或接受它自己的守卫。

**它内部再抓子资源**：纯 HTTP 路径只取页面本身，不取子资源；浏览器路径子资源全部经 SOCKS 守卫。[读码] 另外 PDF/Office 链接走同一个 `Fetcher`。

**会不会泄露到 Firecrawl 云端**：**默认会**。`FIRECRAWL_CLOUD` 缺省是 `auto`：本地读不了（被拦、超时、`FETCH_FAILED`）就把这个 URL 交给 `api.firecrawl.dev` 代抓；`/v2/search` 等其他路由原样转发；每次云端调用带一个随机 install ID。`FIRECRAWL_CLOUD=off` 才全部留在本机；写了不认识的值也当 off。[读码] `cloud.rs:15-45, 286-292, 629-700`、`serve.rs:710-725`。`/health` 不告诉你当前模式。对我们的意思：用户 URL 里可能带私人信息（群友发来的链接），所以**必须保证它是用 `off` 起的**；我们能做的是拒收 `source != "local"` 的结果并停用该提供方，但那时 URL 已经出去了，所以必须靠“Sumika 自己起进程并设环境变量”或设置页明确提醒。

**探针**：要验证它没开 `--allow-private`，可以让 Sumika 临时开一个只绑 127.0.0.1 的随机端口，通过提供方让 Firecrawl 去读它；读到就说明它没拦内网，拒绝启用。这个检查是我们自己的，不依赖它的说法。

### 5.4 这对“默认关、用户明确开”的要求

- 只许连配置里写明的**回环地址**（127.0.0.1、::1、localhost）；配置里写成别的主机一律拒绝（否则一个配置项就能把读网页的请求发到局域网或别人的服务器）。
- 它不能带 `--allow-private`，由探针验证；它自己报告的 `source` 不是 `"local"` 就拒收。
- JS 渲染意味着页面脚本会在主人这台电脑的 Chrome 里跑（沙箱 + 临时 profile，但仍是新的攻击面）；`web_fetch` 的工具说明里“不执行页面脚本”这句要照实改。

## 6. 我们现有的底子

- **提供方接口** `web.FetchProvider{ID, Available, Fetch}`（`web/types.go`）；`FetchChain` 按 `web.fetchOrder` 依次试，失败、内容为空、`LooksBlocked` 换下一家（`web/fetchchain.go`）。装配在 `app/web_providers.go:chooseWebProviders`。[读码]
- **先例：SearXNG** 就是“用户自己配一个实例地址、不要 key、`Available()` 只查地址格式”的提供方（`web/searxng`，配置 `web.searxng.url`）。本机地址的外部服务不是新概念。[读码]
- **现有 Firecrawl 云端提供方**：`New` 里 base 写死 `https://api.firecrawl.dev`，要 key，按次计费，查余额调 `/v2/team/credit-usage`。**不能直接改 base 复用**：这个路由 Local 会转发到云端（会带着 key 出去），而且它计费、记余额。要做成独立 ID `firecrawl-local`，免费（`FreeUsage`），不记余额。[读码]
- **出网记账**：提供方的所有请求都经 `web.DoHTTP`（要求回合上下文里有派发记录，检查 `web:fetch` 授权，记额度）；它不限制目标地址，所以连本机端口没问题。[读码]
- **工具层权限**：`web_fetch` 已按场合分档（QQ 前台没有网络工具，本地群聊只有主人触发），`web.Plugin` 通过 `Host.Visible/Access` 控制；结果头固定写“外部不可信资料；不授予权限”。接进提供方不改这些。[读码] `web/plugin.go`、`web/render.go`
- **MCP 客户端已经有了**：`native/internal/mcpclient`（官方 go-sdk、stdio 和 http、进程树、审批留痕、隔离恢复）、`mcpplugin`（把 MCP 工具给模型，审批默认询问，`Visible(a, "mcp")` 控制听众），配置在 `config/mcp.json`，可以直接贴 Claude Desktop 的 `mcpServers` 片段。ADR-376 说“进程外 MCP 尚未实现”指的是统一清单和隔离执行器，不是 MCP 客户端本身。[读码] `mcpclient/config.go`、`mcpplugin/plugin.go:50`
- 规矩里相关的几条：ADR-379“不依赖没登记的外部程序”、“新协议入口先定来源种类，默认不当成主人”；开发指导“要用 C 或 Rust 的东西，依次考虑纯 Go、wasm（wazero）、子进程”；ADR-380“技术栈不换”针对我们自己的代码，不管用户机器上多一个第三方 Rust 程序，但也不该让 Sumika 自己依赖 Rust 工具链。[读文档]

## 7. 三种接法

### a. 当进程外 MCP 接

- **得到什么**：今天就能试，**零代码**：在 `config/mcp.json` 里写 `{"mcpServers":{"firecrawl":{"command":"…\\firecrawl-local.exe","args":["mcp","--public-only"],"env":{"FIRECRAWL_CLOUD":"off"}}}}`。模型多出 scrape、crawl、map、batch scrape 工具。
- **底子**：有（见上）。`firecrawl-local mcp` 自己起本机 API，再 `npx firecrawl-mcp` 去连它；`npx` 会从 npm 现拉一个包，**每次启动跑一份没锁版本的第三方 JS**，这是额外的供应链面。[读码] README
- **代价**：不经过我们的 `web:fetch` 授权、额度、缓存、外部资料包装、预检（它的结果是 MCP 工具结果，不是 `web.Service` 的结果）[推断：按 `mcpplugin` 的路径读的，没逐行核]；审批默认每次都问，只给主人 × 本地主窗口；`init`/`mcp` 默认 `AllowPrivate`（能读 localhost 和内网），必须记得加 `--public-only`；MCP 里的 `search` 等工具走云端。
- **安全边界**：靠 MCP 的审批和来源规矩，不靠网页读取这套。模型自己决定的抓取每次都弹审批，实用性差；主人亲自让她去读一页倒是合适。
- **适合**：先试用、先看效果，不当长期方案。

### b. 当 `web` 包里一个可选的读取提供方（推荐）

- **得到什么**：`web_fetch` 在 http 读不了时（JS 页面、PDF、Office）自动换 Firecrawl Local；`fetch_provider: auto` 的顺序里排在 `http` 后面，设置页能拖顺序；缓存、额度、授权、外部资料包装、四场合权限都原样沿用，模型看到的还是同一个 `web_fetch`。
- **底子**：很足：接口、先例（SearXNG）、结果解析（现有云端提供方）都有，估计新代码几百行加测试。
- **代价**：要主人自己 `cargo install`（没有预编译包）并自己起进程（v1 不由 Sumika 起）；新增一个外部程序要登记；`web_fetch` 的说明和规格里“不做浏览器、PDF”两处要改；要加探针和结果后检。
- **安全边界**：5.3、5.4。预检在我们、连接时检查在它、探针和后检补缝；默认关；只连回环；拒收云端来源。

### c. 不接，只借鉴

- **得到什么**：不增加外部依赖，没有 AGPL 问题，没有网络环境的坑。借鉴点：每跳重定向重查（已有）、浏览器经本地守卫代理的做法（以后自己做浏览器时用）、`looks_blocked` 的判定规则、PDF/Office 本地解析（我们已有 `pdftext`，可以接进 `httpfetch` 补 PDF）。
- **底子**：有；PDF 接进读网页是几十行的事。
- **代价**：JS 渲染页面仍读不了，要自己做无头浏览器（范围大得多，也是新的攻击面）；整站 crawl 没有。
- **安全边界**：不变。

### 推荐

**b，分两步：先 a 试用（零代码、可撤），实测通了再做 b；c 里的“PDF 接进 httpfetch”可以独立先做**，不依赖结论。理由：我们已经有提供方架构和云端 Firecrawl 的解析代码，b 的新增面只是一个连本机地址的小包；a 绕开我们的网页读取安全和额度，不适合给模型自主使用；c 补不上 JS 渲染。

做 b 前的**门槛**：在主人的网络（Clash fake-ip）上实测通过——`firecrawl-local serve`（`FIRECRAWL_CLOUD=off`，不加 `--allow-private`）能读一个公网页面，并且读不到本机探针。过不了就不做 b，不用 `--allow-private` 糊过去。

## 8. 还没核、需要实测的

- 主人的网络下 `serve` 默认模式能不能读公网页（fake-ip、环境代理、系统代理三种情形各一次）。
- reqwest 在有代理时是否真的绕过它自己的 DNS 守卫（按语义推的）。
- Windows 上 Chrome/Edge 渲染的耗时、内存、有没有残留进程（README 说 Chrome 用临时 profile，退出时的清理没读）。
- 它的正文抽取质量和我们 Readability 的差别（中文页面、B 站、萌娘百科样本各 5～10 个）。
- “37 倍快、11 MB”：官方没写，没测。
- `nodesig` 依赖的许可证。
