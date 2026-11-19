---
name: verify
description: 改完 Sumika 的代码、提交之前用：按改了什么挑验证档位，把改动真跑起来（界面用 ui-tour 走一遍演示窗口，后台用 go-verify 挑包测，必要时真模型冒烟），交回哪一步、截图、日志尾这些证据，并在提交说明里写“验证：”一行。
---

# 提交前验证

出处：[验证蒸馏笔记](../../../docs/notes/蒸馏-验证流程-ClaudeCode团队-2026-11-18.md)第三节第 1 条。验证就是把改动真跑起来，单元测试绿不等于做完。

## 按改了什么挑档位

| 改了什么 | 档位 | 跑什么 |
|---|---|---|
| 只改文档、注释、规格 | 不用跑 | 提交说明写“验证：只改文档” |
| `native/` 的后台包（回合、渠道、记忆、工具、配置……） | 后台 | 下面“后台” |
| 根包或 `internal/` 里画界面的包（`winshell`、`chatui`、`loadui`……），或改了界面会显示的数据 | 后台 + 界面 | 下面“后台”和“界面” |
| 提示词、模型调用、记忆写入或召回、守卫 | 后台 + 真模型 | 下面“后台”，再问主会话要不要跑“真模型”（花钱） |
| 检查脚本、钩子、合并队列本身 | 后台 + 改坏检查 | 照 AGENTS 第 7 条，另做改坏检查 |

## 后台

在 `native/`：

```bash
bun ../scripts/go-affected.ts <改过的文件>      # 先看挑到哪些包
bun ../scripts/slot.ts go vet ./...
SUMIKA_TEST_PROC_MEM_GB=4 bun ../scripts/slot.ts go test -short -count=1 <上面挑到的包> ./internal/archcheck
```

不许直接敲 `go test`。红了先看是不是自己改坏的；是偶发红就照 AGENTS 第 8 条处理，不重跑碰运气。

## 界面

真启动一个只用演示数据的窗口，按状态清单走一遍，截图、量帧、跑六类布局规则。不读真实数据、不联网、不发消息。2026-11-18 实跑过：编译约 20 秒，巡视 124 个状态约 8 分钟。

在 `native/` 的 PowerShell 里（从 Git Bash 用 `powershell -NoProfile -Command "..."` 调也行，注意 `$` 要转义）：

```powershell
$env:GODEBUG = ''                       # 本机 GODEBUG 会让 go 往 stderr 打 gc 日志
$S = '<会话草稿目录>\ui-verify-<时间>'    # 必须是新目录
New-Item -ItemType Directory $S | Out-Null
subst U: $S                             # 先确认 U: 没被占用；路径太长时 socket 会失败
try {
  & go build -tags e2e -o U:/sumika-e2e.exe . 2>&1 | Out-Null
  "编译退出码 $LASTEXITCODE"
  & go run ./cmd/ui-tour -app U:/sumika-e2e.exe -socket-dir U:/ -out U:/tour 2>&1 | Select-Object -Last 3
  "巡视退出码 $LASTEXITCODE"
} finally { subst U: /d }               # 成败都要解除映射
```

不要设 `$ErrorActionPreference = 'Stop'`：go 往 stderr 写的普通输出会被当成错误，编译到一半就退出，还会把 `U:` 留着。

- 整次巡视要 8 分钟，**做完一步（准备合并前）跑一次**，不必每个提交都跑。
- 看输出目录里的 `report.json` 和最后一行 `REPORT`，和基线比，不看绝对数。基线（2026-11-18 main，mygo 0.10.0）：124 个状态、规则命中 1072 处（0.9.1 时 1092）、检查器 Issues 0、最慢帧约 640 毫秒；设置第 4 页和侧栏第 2 页一直重绘，10 个状态 idle 超时，巡视因此退出 1（已挂待办）。你的改动让这些数变多、或者让别的页面也 idle 超时，才是你的问题。
- 打开 `index.html` 看你改到的那几页截图（宽窄、深浅四张）。
- 编译和巡视算重活，同时只跑一个。
- 细节见 [native/README.md 的真窗口自动巡视](../../../native/README.md#真窗口自动巡视) 和 [巡视规格](../../../docs/规格-原生界面自动巡视.md)。

## 真模型（花钱，先问）

改了提示词、记忆、守卫时，用编的数据和假 QQ 端跑一遍两轮冒烟：

```powershell
go run ./cmd/e2e-run -scenario smoke -out '<会话草稿目录>\e2e' -models-root <模型根目录> -models-exe ..\native\sumika-models.exe
```

会调用真实模型、产生费用：派活的代理不要自己跑，在交回里写“建议跑 e2e smoke”，由主会话决定。完整剧本（`-scenario full`）留给主人。

## 交回什么

- 每档跑了什么命令、结论行（从完整输出里抄，没看到最后一行和退出码就写“没跑完”）。
- 界面档：改到的页面的截图路径、`report.json` 里和基线不同的数。
- 失败时：哪一步、报错原文前几行、日志尾、截图。
- 提交说明最后一段加一行 `验证：……`，写跑了哪几档和结论；没跑写 `验证：没验` 和原因。

## 停下的条件

同一个失败修三轮还不过，停下交回，写清卡在哪（AGENTS 第 8 条）。
