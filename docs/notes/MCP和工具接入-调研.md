# MCP、更多工具、网络搜索与 skill 接入调研

调研日期：2026-10-15（按主人指定；本机系统日期显示为 2026-10-15）。只读调研，未安装任何依赖，未改项目文件。

## 一句话结论

pi-coding-agent 1.1.0 已经内置 MCP 和 skill，但它的 MCP 绑在整套 coding-agent 会话上，Sumika 目前直接用 pi-agent-core 建对话实例，不能原样套用。最省事的路线是：直接用 `@earendil-works/pi-mcp`（它已经在 `node_modules` 里，不依赖官方 SDK）把 MCP 工具包成 `AgentTool`，只挂在主人 × 控制台上，所有 MCP 工具默认要审批；skill 不需要新工具，只在启动时把"名字 + 描述"目录固定进前缀，正文用现有的 `read` 读取并追加到末尾；网络搜索个人项目用 Tavily 免费档或自建 SearXNG 都可以。

## 一、pi 现状

**本地已安装的包**（`仓库根\node_modules\@earendil-works\`，版本均为 1.1.0）：

- `pi-agent-core`：Sumika 直接用的对话内核。
- `pi-coding-agent`：内置 MCP 与 skill，见下。
- `pi-mcp`：独立的 MCP 客户端，README 写明"不依赖官方 MCP SDK 或其他 pi 包"，提供 `McpClient`、`StdioTransport`、`StreamableHttpTransport`、`toLlmContent`，附带把 MCP 工具包成 `AgentTool` 的示例代码。sumika 的 `package.json` 没有直接依赖它，只是通过 `pi-coding-agent` 间接装进来的，要用就应该显式加依赖（ADR-019 要求依赖选型留痕）。
- `pi-codemode`：让模型写脚本来调用多个工具，MCP 工具可以不占用模型的工具列表。

**pi-coding-agent 内置的 MCP**（`docs/mcp.md`）：

- 配置文件 `~/.pi/agent/mcp.json`（用户级）和 `.pi/mcp.json`（项目级，需要项目信任）。
- 每个工具注册为 `mcp__<服务名>__<工具名>`。
- 每个服务有 `exposure` 参数：`codemode`（默认，只能经脚本调用，模型看不到定义）、`deferred`（模型用 `tool_search` 找到后才声明）、`direct`（直接声明给模型）、`hidden`（不可用）；还能用 `toolExposure` 对单个工具单独设置。
- 支持 OAuth（`pi mcp login`），令牌存在 `~/.pi/agent/mcp-auth.json`。
- 有 `--no-mcp` 参数。

**pi-coding-agent 内置的 skill**（`docs/skills.md`）：

- 实现 Agent Skills 规范：目录里放 `SKILL.md`，前面是 `name` 和 `description` 的 frontmatter。
- 启动时只把名字、描述、路径放进系统提示；模型判断相关时再读完整的 `SKILL.md`；`/skill:name` 可以强制加载。
- 搜索位置包括 `~/.agents/skills/` 和项目的 `.agents/skills/`。

**pi 自己的安全文档**（`docs/security.md`）写得很直白：工具调用不会逐个审批，扩展和 skill 以进程用户的全部权限运行，文件、注释、命令输出都可能用提示注入来操控模型；项目信任只控制启动时加载哪些资源，不是安全边界。这和 Sumika 的审批模式是互补的，Sumika 要在自己这一层做审批。

**与 Sumika 现状的关系**（`docs/subsystems/tools.md`、`daemon/plugins/tools.ts`）：

- `Tools.forAudience(characterId, audience)` 是唯一的渠道过滤点，现在只有"主人 × 控制台"会拿到读写和 PowerShell 工具。
- `gate()` 只拦截 `NEEDS_APPROVAL = {write, edit, powershell}` 这三个名字；新增的 MCP 工具如果不加进这个集合，就会直接执行、不经审批。
- 工具列表在建对话实例时固定（`turn/tools` 事件），整个实例期间不变，这是为了前缀缓存。MCP 服务是异步连接的，要么在实例创建前等它连上，要么在启动时一次性固定工具列表，连上之后的变化只能等下一个实例。
- `memory-tools.ts` 的 `defineTool` 包装是现成的模式，MCP 工具可以照这个方式包。

## 二、MCP 接法

**两条路线**：

**路线 A：直接用 pi-coding-agent 的 MCP 扩展**。`createMcpExtension`（`pi-coding-agent` 的导出）是一个 ExtensionFactory，要挂在 coding-agent 的 `AgentSession` 上。Sumika 现在用的是 pi-agent-core 的 agent，要改成 AgentSession 才能用，改动面大，还会把 coding-agent 的很多行为（codemode、tool_search、系统提示里的 `mcp_servers` 段落）一起带进来。不推荐现在做。

**路线 B：用 pi-mcp 自己包一层**（推荐）。pi-mcp README 给出的模式：

1. `new StdioTransport({ command, args, env, cwd })` 或 `new StreamableHttpTransport({ url, headers })`。
2. `const client = new McpClient({ name, version, roots })`，`await client.connect(transport)`。
3. `await client.listTools()` 取到工具列表，每个工具包成 `AgentTool`：
   - 名字用 `mcp__<服务名>__<工具名>`，过滤成 `[A-Za-z0-9_-]`，不超过 64 字符（README 注明各家模型接口的上限）。
   - `parameters` 用 `Type.Unsafe({ ...inputSchema, type: 'object', properties: inputSchema.properties ?? {} })`，因为部分提供方要求一定要有 `properties`。
   - `execute` 里调用 `client.callTool(name, params, { signal })`，结果用 `toLlmContent(result)` 转成文本和图片，`isError: result.isError === true`。MCP 的工具错误是放在结果里的，不是协议错误。
4. 把这些工具放进 `forAudience`，并加进 `gate` 的审批集合。

**与 Sumika 的约束对照**：

- stdio 服务会在本机启动子进程，这属于"本地执行能力"，AGENTS.md 明确"QQ 渠道里不出现任何本地执行能力"，所以 stdio 类的 MCP 只能给主人 × 控制台。
- 远程 streamable HTTP 的 MCP（比如搜索服务）理论上可以在以后给其他渠道，但要先过记忆可见性和提示注入这两关，第一阶段不做。
- 服务连接失败或断开时，工具要标记为不可用，不能让模型拿到一个调用一定失败的工具（pi 的做法是"断开后下次调用时重连"）。

**SDK 选择**（见第四节出处）：

- 官方 TypeScript SDK 当前稳定线是 v2（2026-07-27 发布，对应 MCP 规范 2026-07-28），包名 `@modelcontextprotocol/client` 和 `@modelcontextprotocol/server`；v1 是 `@modelcontextprotocol/sdk`，还会维护至少六个月。
- 官方 README 称 SDK 支持 Node.js、Bun、Deno，但没有给出最低版本，也没有 Bun 的实测记录。
- 我没查到官方文档里 `StdioClientTransport` 和 `StreamableHTTPClientTransport` 在 v2 的确切导入路径（官方 README 只展示了服务端）。第三方资料里 v1 的路径是 `@modelcontextprotocol/sdk/client/stdio.js` 和 `@modelcontextprotocol/sdk/client/streamableHttp.js`，这是第三方文章的说法，未核对。
- 第三方资料提醒：stdio 服务端不能用 `console.log`，因为标准输出是协议通道；要用 `console.error`。这是服务端的坑，客户端不用管。
- 结论：如果走路线 B，用 `pi-mcp` 更省事（已在依赖树里、零额外依赖、接口已经和 AgentTool 对齐）。官方 SDK 作为备选，需要在 Bun 上实测后再定。pi-mcp 在 Bun 上是否跑得通，我没有实测，这一点要在第一步验证。

## 三、skill 做法

**官方规范**（Anthropic 工程博客，2025-10-16 发布，2025-12-18 更新为开放标准）：

- 三层渐进式加载：启动时只放 name 和 description；相关时读完整 `SKILL.md`；附带的参考文件只在需要时读。
- 脚本可以作为工具运行，不必进入上下文，结果也更稳定。
- 安全提醒：只装可信来源的 skill；不太可信的要先审计脚本、依赖和图片；注意检查有没有让模型去连接不可信外部网络的指令。

**在 Sumika 里实现的最小方案**（不需要新的工具定义）：

1. 启动时扫描一个 skill 目录（建议放在 `data/` 之外的项目目录，或者 `characters/` 旁边，具体位置要定），读每个 `SKILL.md` 的 frontmatter，生成一段固定的"可用 skill 目录"文字，放进系统提示（前缀）。这段文字在对话实例的生命周期内不变。
2. 模型需要某个 skill 时，用现有的 `read` 工具读 `SKILL.md`（`read` 已经在控制台工具里）。读到的内容作为工具结果追加到对话末尾，不改前面的历史。
3. skill 里的脚本，通过 `powershell` 工具执行，自然走审批。
4. 目录有变化（新装 skill）后，要等下一个对话实例（或 `/reload` 类操作）才生效，这和前缀缓存的要求一致。

**与缓存和 LESSONS.md 的对照**：

- LESSONS.md 要求"每轮变化的内容只能追加在末尾"。skill 目录放在前缀里，只要启动时定一次、之后不动，就不会打断缓存；skill 正文通过工具结果追加，也符合规则。
- pi 的做法可以借鉴：`mcp_servers` 段落"有变化时追加一段新的，而不是改掉前面的声明"，保持之前消息的缓存。

**skill 与 pi 的 `/skill:name`**：`/skill:name` 是 pi 的交互命令，Sumika 的控制台不一定需要，可以先不做。

## 四、网络搜索

**候选**（价格以官方页面为准，下表是搜索摘要里的数字，未逐一核对官方页面，标注如下）：

| 方案 | 免费额度 | 付费价格 | 个人本地项目的评价 |
|---|---|---|---|
| Tavily | Researcher 档每月 1000 credits，不需要信用卡 | Project 每月 30 美元（4000 credits）；按量 0.008 美元/credit；基础搜索 1 credit，深度搜索 2 credits | 免费额度够个人用，接口为 AI 设计，结果直接可用；需要 API key |
| Brave Search API | 旧的 5000 次免费档已对新用户取消；搜索摘要里说法不一（约 5 美元月度额度，约 1000 次，来源说法冲突） | Search 每 1000 次 5 美元；限速 50 次/秒 | 价格最简单，但免费额度不确定，要绑卡 |
| Exa | 新账号 20 美元额度（说法有冲突，一个表格称每月 2 万次，未证实） | Search 带内容每 1000 次 7 美元 | 偏语义搜索，适合研究型任务，个人用不算便宜 |
| SearXNG（本地自建） | 无 API 费用（自己的机器和网络） | 无 | 最符合"不把数据交给第三方"的偏好；但 JSON 输出要在服务器的 `settings.yml` 里开启 `json` 格式，否则返回 403；公共实例常常关闭这个格式，所以要自己部署；我没有查到限速和反爬的说明，也没有查到官方 Docker 部署步骤，要看安装文档 |
| 通过 MCP 的搜索服务 | 取决于服务 | 取决于服务 | 接入方式和路线 B 一样；但它是远程服务，要先过渠道和记忆可见性的检查 |

**建议**：

- 第一选择：Tavily 免费档，接口简单，免费额度够个人项目用。
- 备选：SearXNG 自建，如果主人希望搜索请求不经过第三方。
- 注意：AGENTS.md 规定"不在代码里写任何供应商的 key"，搜索 key 要放在配置或环境变量里，不能写进代码。
- 注意：搜索结果是外部内容，要按提示注入的规则处理（见第五节）。

## 五、权限

**MCP 规范的要求**（`modelcontextprotocol.io/docs/concepts/tools`，规范版本 2026-07-28）：

- 应该有人在回路中，可以拒绝工具调用；展示哪些工具暴露给模型；工具调用时有明显标记；对操作给出确认提示。
- 工具的 `annotations`（比如 `readOnlyHint`、`destructiveHint`）是提示，客户端必须把它们当作不可信的，除非来自可信的服务。
- 客户端应该在调用前展示工具输入，以防数据外泄；对工具结果要先验证再交给模型；要有超时；要记录调用日志。

**对 Sumika 的具体建议**：

1. **按渠道**：MCP 工具只挂在 `forAudience` 的主人 × 控制台分支里。QQ 渠道不给任何 stdio MCP 工具。
2. **按服务**：每个 MCP 服务在配置里声明"只读 / 写 / 执行"，但这个声明只作为展示和默认值，不能让它绕过审批。
3. **审批**：默认所有 MCP 工具都进 `NEEDS_APPROVAL`（因为 annotations 不可信）；只读服务可以在配置里声明"不需要审批"，并且只对只读服务开放（比如 `auto` 模式下只读白名单）。这一点要在 `risk.ts` 里加规则，并配测试。
4. **注入防护**：
   - 工具结果一律当作数据，不能让它改变系统指令。
   - 搜索和网页内容进入上下文前可以用 pi-codemode 的思路，让模型先写脚本过滤，只把需要的字段交给模型（这个思路 pi 文档里有提到，但我没有查到它的防注入效果的实测数据）。
   - skill 的 `SKILL.md` 和脚本如果来自网络，要先审计，不能自动装。
5. **审批超时**：沿用现有的 `approvalMinutes`，超时按拒绝处理。

**承认的局限**：pi 自己的 security.md 明确说明，它没有内置沙箱，扩展和 skill 与进程同权限运行。Sumika 的审批只是一道人工门槛，不能替代沙箱。如果将来要接入来源不明的 MCP 服务，应该先在虚拟机或容器里跑。

## 六、推荐的第一步

按 AGENTS.md 的要求，新模块要先写规格，给主人看过再实现。建议顺序：

1. **写一份 MCP 接入规格**（放 `docs/` 下，不改代码）：写清楚路线 B、工具命名、渠道和审批规则、连接失败处理、工具列表在实例启动时固定的做法。主人确认后再动手。
2. **Spike（需要主人批准安装）**：在临时目录里用 `pi-mcp` 的 `StdioTransport` 连一个只读的 MCP 服务（比如官方的文件系统服务，只给临时目录），确认在 Bun 上 `listTools` 和 `callTool` 能跑通，把结果包成 `AgentTool`，只挂在控制台。测试用临时 `SUMIKA_CONFIG`，不碰真实的 `data/`。
3. **skill 最小实现**：启动时扫描目录生成固定的目录文字，用现有 `read` 读正文。可以不依赖任何新包，改动小，先做。
4. **搜索**：先决定用 Tavily 还是 SearXNG。Tavily 只需要一个 key 和一个工具定义；SearXNG 需要主人先在某台机器上部署。
5. 每一步都要写验收：按 AGENTS.md 的规则，包括"把旧代码放回去就失败"的测试（如果是修 bug），以及提交说明写清楚跑了什么、没试什么。

## 七、查不到或未验证的部分

- `@earendil-works/pi-mcp` 在 Bun 上的实际运行：没有实测。
- 官方 TypeScript SDK 在 Bun 上的 stdio 和 HTTP 客户端：没有找到官方示例，第三方示例未核对。
- Brave、Exa 的当前价格和免费额度：搜索摘要说法不一致，没有逐一打开官方价格页核对。
- SearXNG 的限速、反爬和官方 Docker 部署步骤：官方页面没有写，未查到。
- pi-coding-agent 的 MCP 扩展在 pi-agent-core 上的复用方式：只读了类型导出，没有实际接过。
- 没有查到 Anthropic 对"工具结果提示注入"的专门指南，防注入部分的建议是基于 MCP 规范和 pi 安全文档归纳的，不是官方防注入方案。

## 八、出处

本地文件（读取于调研当天）：

- `仓库根\docs\subsystems\tools.md`：Sumika 工具与审批现状。
- `仓库根\daemon\plugins\tools.ts`：`forAudience`、`gate`、`NEEDS_APPROVAL`。
- `仓库根\daemon\tools\memory-tools.ts`：`defineTool` 包装模式。
- `仓库根\node_modules\@earendil-works\pi-coding-agent\docs\mcp.md`：pi 内置 MCP 的配置、exposure、OAuth。
- `仓库根\node_modules\@earendil-works\pi-coding-agent\docs\skills.md`：pi 的 skill 加载方式。
- `仓库根\node_modules\@earendil-works\pi-coding-agent\docs\security.md`：pi 的安全边界说明。
- `仓库根\node_modules\@earendil-works\pi-coding-agent\CHANGELOG.md`：1.1.0（2026-10-07）的 MCP 相关改动。
- `仓库根\node_modules\@earendil-works\pi-coding-agent\dist\index.d.ts`：`createMcpExtension`、`loadSkills` 等导出。
- `仓库根\node_modules\@earendil-works\pi-mcp\README.md` 与 `package.json`：`McpClient`、`StdioTransport`、`toLlmContent`、AgentTool 包装示例。

网络资料（访问日期 2026-10-15）：

- [Equipping agents for the real world with Agent Skills（Anthropic 工程博客）](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills)：2025-10-16 发布，2025-12-18 更新。
- [MCP SDKs 页面](https://modelcontextprotocol.io/docs/sdk)
- [modelcontextprotocol/typescript-sdk（GitHub）](https://github.com/modelcontextprotocol/typescript-sdk)：v2 发布于 2026-07-27，v1 维护至少六个月。
- [MCP Tools 规范页](https://modelcontextprotocol.io/docs/concepts/tools)：2026-07-28 规范版本的安全要求和 annotations 的不可信说明。
- [SearXNG Search API 文档](https://docs.searxng.org/dev/search_api.html)：`format=json` 需要服务器开启，否则 403。
- [Tavily 价格说明](https://help.tavily.com/articles/8816424538-pricing)：免费档和按量价格（搜索摘要来源，未逐项核对）。
- [Brave Search API 价格（CostBench）](https://costbench.com/software/ai-search-apis/brave-search-api/)：第三方汇总，免费额度说法不一。
- [Exa 价格变更记录](https://exa.ai/docs/changelog/pricing-update)：搜索摘要列出，未打开核对。
- [pi-mcp-extension（pi.dev）](https://pi.dev/packages/pi-mcp-extension)：第三方的 pi MCP 扩展，未核对，不建议直接安装。
