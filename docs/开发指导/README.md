# 开发指导

在某个领域开发新东西之前，先读下面对应的一页。每页是该领域的短规则，带正例和反例；来源是 2026-11-14 的独立架构质检（`data/dev/fable-review/块-A.md` 到 `块-J.md`，不进 git），已按 main 逐条核对过代码位置，并对照 AGENTS、LESSONS 和 ADR-373 至 ADR-378。这些页是“怎么做才对”，现状见 [子系统参考](../subsystems/README.md)，决定见 [决策记录](../决策记录.md)。

记号：

- 〔目标〕：质检建议的做法，对应的包、函数、检查现在还没有；不能当现成接口调。
- 权限、可见性、审批类的 18 条，主人 2026-11-14 晚整批点头（ADR-379 的补记），已经是规则；以后新加这类条目先标〔待主人确认〕，主人看过再去掉。
- 每页末尾“和现行规矩冲突、待定”是和 AGENTS、LESSONS、ADR 对不上、要主人定的条目，不当规则用。
- 页里的路径：Go 的相对 `native/`（`internal/...`），脚本和文档相对仓库根，各页首行写明。

| 读这一页 | 改哪些包（相对 `native/`），做哪类事时读 |
|---|---|
| [回合内核](回合内核.md) | `internal/turn`、`internal/kernel`、`internal/turnbudget`、`internal/subagent`、`internal/goal`、`internal/taskrun`、`internal/watchdog`：开始一轮、排队插话停止、工具调度、运行状态和恢复、预算 |
| [渠道与平台](渠道与平台.md) | `internal/channel`（`types`、`snowluma`、`qq`、`repeat`、`fake`、`sendlock`）、`internal/app/channel*.go`：接新平台、加能发的东西、改准入和发送规矩 |
| [模型与服务商](模型与服务商.md) | `internal/llm`、`internal/modelcall`、`internal/modelmeta`、`internal/localmodels`、`cmd/sumika-models`：接新服务商或协议、改请求体、能力和限额 |
| [插件与扩展](插件与扩展.md) | `internal/turn/plugin*.go`、`internal/mcpclient`、`internal/mcpplugin`、`internal/skills`、`internal/userhooks`、`internal/plugintrace`、各 `*plugin` 包：加插件、工具、钩子、MCP、脚本宿主 |
| [记忆与上下文](记忆与上下文.md) | `internal/memory`、`internal/memwrite`、`internal/memplugin`、`internal/history`、`internal/turn` 的压缩和重建（`compact.go`、`refblock.go`、`workhistory.go`）：往上下文加内容、记忆读写、压缩、提示词缓存 |
| [权限安全隐私](权限安全隐私.md) | `internal/approval`、`internal/approvalrelay`、`internal/policy`、`internal/audience`、`internal/secrets`、`internal/checkpoint`：审批、放行规则、谁能看谁能做、密钥 |
| [存储与数据](存储与数据.md) | `internal/store`（含迁移）、`internal/paths`、`internal/backup`、`internal/sqlitesnap`、`internal/fileatomic`、`internal/storagestat`、`internal/mediastore`：新增落盘数据、开库、写迁移、备份、保留期 |
| [界面与交互](界面与交互.md) | 根包 `*.go`、`internal/winshell`、`internal/chatui`、`internal/design`、`internal/desksettings`、`internal/desktopread`、`internal/settingsreg`、`internal/api`：加页面、控件、设置项、文案 |
| [开发流程测试便携](开发流程测试便携.md) | 仓库根 `scripts/`、`lefthook.yml`、`testdata/shared/`、`native/internal/archcheck`、`native/internal/sharedtest`、测试写法：加检查、一致性场景、外部工具、新测试 |
| [技术栈](技术栈.md) | `native/go.mod`、`protocol/`、`scripts/gen-rpc.ts`、`internal/pdftext`：换技术、加依赖、跨语言的接口和格式、脚本插件的宿主能力 |

一件事跨几个领域时，几页都读；规则冲突以 AGENTS、LESSONS 为准，并告诉主人。
