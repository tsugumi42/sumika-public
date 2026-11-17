# Sumika

[English](README.md) | 简体中文

[![CI](https://github.com/tsugumi42/sumika-public/actions/workflows/ci.yml/badge.svg)](https://github.com/tsugumi42/sumika-public/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Sumika（栖処）是一个常驻本机的 AI 角色之家。一台电脑里住着几个角色，各有自己的人设、模型和长期记忆。你可以在桌面窗口里和她们聊天，让她们帮你在电脑上做事（每一步都要你批准）；她们也能进 QQ 私聊和群聊，并且记得谁是谁。

> [!WARNING]
> **还在早期开发。** 没有发布版和安装包；配置项和数据格式还会变，不保证能平滑迁移；文档里写的部分功能只做了一半。现在还不适合日常使用。

## 现在能做什么（Windows）

- **原生桌面程序**：后台和界面在同一个 exe 里，界面用 [mygo](https://github.com/egoist/mygo) 和 [CrychicUI](https://github.com/ELY-Open-Source/CrychicUI) 绘制；常驻托盘，不开网页、不开端口。
- **本地私聊和群聊**：流式回复、随时停止、引用；群里几个角色轮流接话。
- **工具要你批准**：命令、写文件等操作先过策略检查，再弹审批卡，你点了才执行；改过的文件可以用检查点撤回。
- **长期记忆**：每个角色有自己的记忆，关于你本人的事实放在共用池；支持全文检索，可选本地向量检索。后台整理默认关闭。
- **QQ**：连接 OneBot 兼容的协议端，所有消息存进本地，由闸门决定角色要不要开口。发送默认关闭，发出前还要过守卫。
- **看懂链接**：X、B 站、YouTube、pixiv、Steam、BOOTH 等。

还在做的：能热重载的 TypeScript 插件、MCP、在界面里改设置、安装包。见[路线图](docs/路线图-1.0.md)。

## 各部分

| 部分 | 做什么 |
|---|---|
| 角色 | 人设卡、口吻、所用模型、自己的记忆，放在 `data/characters/<id>/`，不进 git |
| 渠道 | 消息的来处：本地私聊、本地群聊、QQ 私聊、QQ 群；每条记忆都标来源 |
| 内核 | 跑回合、工具和审批；记忆、链接解析等插件通过登记表接进来 |
| 模型网关 | 默认走本机的 [Magpie](https://github.com/yetone/magpie)（`127.0.0.1:3425`），每个角色选一个 `provider/model` |
| 桌面界面 | 和后台同进程，直接调用 |

总体架构见 [docs/架构.md](docs/架构.md)。

## 开发环境

需要：Windows 10 或 11、[Go](https://go.dev/dl/) 1.27+、[Bun](https://bun.sh/) 1.4+（仓库脚本和 git 钩子用）、一个能用的 Magpie，或者别的 OpenAI 兼容地址（填在 `magpie.baseUrl`）。

1. 克隆仓库，在根目录执行 `bun install`：装脚本依赖和 git 钩子。
2. 把 `sumika.example.yml` 复制成 `data/config/sumika.yml`：每一项的含义都写在注释里。
3. 在 `native/` 里编译并启动：
   ```bash
   go build -trimpath -ldflags "-s -w -H windowsgui" -o sumika.exe .
   ./sumika.exe -root ..
   ```
   默认是只看历史的模式，从托盘开始接收后才能本地聊天。只想看看界面、不碰任何数据，用 `./sumika.exe -demo`。
4. 跑测试：在 `native/` 里执行 `go vet ./...` 和 `go test -short ./...`。CI 每次推送都在 Windows 和 Linux 上各跑一遍。

字体不随仓库分发，没有时界面用系统字体。所有命令行工具见 [docs/subsystems/go-tools.md](docs/subsystems/go-tools.md)。

## 文档

- [CONTRIBUTING.md](CONTRIBUTING.md)：搭环境、测试、提交规矩、代码地图
- [docs/README.md](docs/README.md)：全部文档的索引
- [docs/决策记录.md](docs/决策记录.md)：做过的决定和理由
- [AGENTS.md](AGENTS.md)：改代码前的项目约定
- [docs/使用说明.md](docs/使用说明.md)：使用说明

## 参与

欢迎报 bug 和小的 PR；大的改动请先开 issue。提交代码前先读 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

[MIT](LICENSE)。第三方许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
