# Sumika

[English](README.md) | 简体中文

Sumika（栖処）是一个本地常驻的角色 Agent：同一台电脑里住着几个性格不同的 AI 角色，每个人有自己的人设和长期记忆。做成以后，你可以在主窗口里和她们聊天、让她们帮忙做事；她们也能用各自的 QQ 号接收私聊和群消息，并且记得谁是谁。

> 状态：开发中，Bun 后台已退役，Go 后台与原生窗口同进程运行（ADR-203）。现在的样子（issue #12）：
> - **Go 后台**（`native/`）：能连一个 QQ 小号收消息、入库、由 Go 内核回话，发送前过守卫，看得懂聊天里的 X、B 站链接。2026-11-02 的[切换记录](docs/notes/清单-QQ小号切换到Go-2026-10-30.md#切换记录切换当天填)记载已接管小号、开启主人 QQ 私聊发送，两轮各回一条，群里没有发送；群聊开口仍待单独验收与授权。
> - **原生主窗口**（mygo 画的，不是网页）：和 Go 后台装在同一个 exe 里，双击默认只看历史；托盘可开始 / 停止接收 QQ、设置自动接收和开机自启（默认都不勾）、退出。receive 下本地私聊已接文字输入、流式回复、停止和审批；本地群聊也能建群、输入、轮流接话，QQ 会话仍只查看记录，配置页只读。记忆页第一步只读浏览已合 main，编辑及真窗口手试仍待完成。见 [native/README.md](native/README.md)。
> - **旧的 Bun 后台和网页界面**：退役了，不再加功能也不再修；2026-11-01 已停，11 月 2 日 Go 接管前曾没有 Sumika 程序保存新 QQ 聊天记录。2026-11-01 起旧代码在单独的仓库 sumika-bun，2026-11-11 已存档；ADR-359 后仓库里的 `server/`、`desktop/` 冻结，只当参考。
>
> 进度见[待办](docs/待办.md)顶部"现在的顺序"和[路线图](docs/路线图-1.0.md)。

## 各部分怎么配合

- **角色**：住在 Sumika 里的 AI。一个角色有人设、口吻、自己用的模型和专属记忆；这些放在 `data/characters/<id>/` 里（卡在 `card/`，不进 git）。主人自身事实的共用例外见下面“记忆”。
- **渠道**：消息从哪里来、往哪里去。本地私聊、本地群聊、QQ 私聊、QQ 群是四种场合；同一个角色在哪都能想起自己的记忆，每条标来源，说不说由提示词和发送前守卫配合判断。
- **Go 后台**：一个进程装着内核（回合、工具、审批）、渠道中间件（收消息、排队入库、闸门决定要不要开口、发送）、QQ 适配器（连 SnowLuma 的 OneBot 接口）和插件（记忆、链接解析、复读、戳一戳）。两种模式：`archive` 只看历史、不连 QQ；`receive` 连一个 QQ 小号接收。日常入口是桌面程序（`native/` 根目录），默认 `archive`，托盘里可以切到 `receive`；`native/cmd/sumika-go` 是同一套后台的无界面入口，用来诊断和试运行。两个抢同一把数据锁，不能同时开。
- **记忆**：角色库保存各自的关系、约定和共同经历，明确可共用的主人自身事实、偏好在共用池只存一份（ADR-162、ADR-242）。Go 已按配置开放 `remember` 和后台整理两档写入，默认都关；2026-11-02 切换记录只确认铃音的 `remember` 第一档已开并写入两条事实，后台整理和真实模型的完整“写入 → 重启 → 换问法 → 最终答对”验收仍待完成（issue #17）。
- **模型网关**：默认把模型请求交给本机的 [Magpie](https://github.com/yetone/magpie)（`127.0.0.1:3425`），每个角色在配置的 `characters` 一节选一个 `provider/model`；以后会支持不装 Magpie、直连服务商（ADR-044）。
- **原生主窗口**：mygo + CrychicUI 的原生界面，和 Go 后台在同一个进程里，直接调读接口及可选本地写接口、不开端口，挂在右下角托盘里（[规格-界面接Go后台](docs/规格-界面接Go后台.md)）。

## 快速开始（开发用）

还没有给普通用户的安装包。下面是开发时在本机跑 Go 后台的步骤，要准备：Windows、Go 1.27 以上、一个能用的 Magpie 网关；接 QQ 还要自己装好并登录 SnowLuma。记忆向量检索支持 `embed.backend: go-models`，由后台启动独立的 `sumika-models` 服务（gte 嵌入；模型目录、官方 onnxruntime.dll 和构建方式见规格-Go本地模型（私有存档））；默认后端仍是 `legacy`，沿用配置中的 Python 桥，不填嵌入配置时只用全文检索。GM8 切到 Go 模型已有 2026-11-02 切换记录，GM9 稳定观察后清理旧依赖仍待办。想参与开发，先看 [CONTRIBUTING.md](CONTRIBUTING.md)。

1. 装 [Go](https://go.dev/dl/)。作用：Go 后台和原生界面都用它编译。
2. 把 `sumika.example.yml` 复制成 `data/config/sumika.yml`（没有这个文件时读仓库根目录的 `sumika.yml`）。作用：放你自己的配置；每个键的含义写在示例的注释里。
3. 启动 Magpie，确认 `magpie.baseUrl`（默认 `http://127.0.0.1:3425/v1`）能访问。
4. 在 `native/` 文件夹里执行 `go run ./cmd/sumika-go -mode archive`。作用：只读地打开已有的 Go 数据，确认配置和数据目录都对；不连 QQ、不调模型，也不建库，所以还没有 Go 的聊天库时它会报缺库退出，可以直接做下一步。按 Ctrl+C 停。
5. 想看界面：在 `native/` 里执行 `go build -trimpath -ldflags "-s -w -H windowsgui" -o sumika.exe .`，再执行 `sumika.exe -root ..`。作用：默认打开只看历史的主窗口和托盘；启动 receive 后可在本地私聊发文字、看流式回复、停止和处理审批。`-root` 指向有 `sumika.example.yml` 的仓库根目录，不给时用 exe 所在的目录。要有 Go 的聊天库才看得到历史，还没有时窗口只显示"后台没在运行"和原因；只想看样子、不碰任何库，用 `sumika.exe -demo`。exe 自带图标和托盘图标（`native/rsrc_windows_*.syso`、`native/assets/`），源文件和重新导出见 [docs/assets/logo/README.md](docs/assets/logo/README.md)。
6. 想让角色进 QQ：先从托盘菜单退出 `sumika.exe`（关窗口只是隐藏，它还占着数据锁）。在配置 `qq.accounts` 里写 `id`（她的号）、`character`（绑给哪个角色）和 `token`（SnowLuma 的访问令牌，可以写成 `${环境变量名}`，别把明文提交进 git；不写就从加密的密钥里取）；`url` 不写会按本机 SnowLuma 的配置自动找。然后 `go run ./cmd/sumika-go -account <号>`（默认就是 `receive`）。作用：连这个小号收消息、入库；真发消息要满足[规格-QQ发送上线](docs/规格-QQ发送上线.md)第 5.1 节的七个条件，默认发不出去。Sumika 只连接和监控 SnowLuma，不负责安装和登录。
7. 取 X、GitHub、YouTube、pixiv 等境外链接一般要走代理：在配置里写 `linkReader.proxy: http://127.0.0.1:<端口>`；B 站、抖音、网易云仍直连。各平台匿名能取到的字段和受限状态见[链接解析规格第四节](docs/规格-链接解析.md#四联网取内容及字段契约)。

所有命令行工具和参数见 [docs/subsystems/go-tools.md](docs/subsystems/go-tools.md)；原生界面怎么编译见 [native/README.md](native/README.md)。

## 文档在哪

- [使用说明](docs/使用说明.md)：首次启动、日常聊天、托盘控制、设置、故障排查和数据备份。
- [CONTRIBUTING.md](CONTRIBUTING.md)：想改代码的人从这里开始：搭环境、编译和测试、提交的规矩、代码地图。
- [docs/README.md](docs/README.md)：所有文档的索引，以及"每种事实以哪份为准"。
- [AGENTS.md](AGENTS.md)：动手改代码前的项目约定（验收、文档分层、技术约束、安全边界）。
- [LESSONS.md](LESSONS.md)：之前做错过什么，改代码前先读。
- [docs/待办.md](docs/待办.md)：现在先做什么、什么在等主人。
- [docs/路线图-1.0.md](docs/路线图-1.0.md)：1.0 包括什么、各块谁等谁。
- [docs/决策记录.md](docs/决策记录.md)：已经拍板的决定和理由，只增不改。
- [docs/subsystems/](docs/subsystems/README.md)：现在的代码是怎么工作的。
