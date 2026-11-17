# 测试与验收

使用时机：实现前写验收矩阵、交付前实跑。出处代号和固定版本见 sources.md；本页命令从仓库根或注明的目录执行。

## Tester：验真实行为与几何

把真实 view 接编造数据/隔离的假后端，不再维护一份测试专用画面。状态断言检查动作 ID、版本、次数、结果和焦点；几何断言检查 bounds、锚点、遮挡和留白。依据：M docs/ui/testing.md:7–22、28–79；L desktop/render_test.go:47–59（GPL，仅结构）；[源码→项目验收]。

NewTester 与 ui.View 都接收 func(*ui.Context)，没有返回值。返回 Element 的 helper 需包一层 `func(c *ui.Context) { a.View(c) }`；不要直接传 a.View。M ui/api_gen.go:1437–1439、2676–2678；[源码/本次编译修正]。

以下是第3个配方的独立 Tester 写法（MIT API，需 testing 与 recipes.md 的 Approval）；fake 只记录投递，不执行工具，故 busy 保持到显式结果到达。

```go
func TestApprovalBusy(t *testing.T) {
    calls := 0
    a := &Approval{ID: "approval-demo", Revision: 7, Command: "go version", Status: "待审批"}
    a.Submit = func(id string, rev int, allow bool) {
        if id != a.ID || rev != 7 || !allow { t.Fatal(id, rev, allow) }
        calls++
    }
    tt := ui.NewTester(func(c *ui.Context) {
        ui.Column(c).Fill().Children(func() { a.View(c) })
    }, 480, 320)
    if err := tt.Click("允许一次"); err != nil { t.Fatal(err) }
    if calls != 1 || a.Status != "提交中" || !tt.HasText("提交中") { t.Fatal(calls, a.Status) }
    if err := tt.Click("允许一次"); err == nil { t.Fatal("busy 仍有审批动作") }
}
```

出处：M docs/ui/testing.md:7–22、ui/headless.go:490–516；MIT 独立组合；[源码]。Click 返回 nil 只说明目标存在且中心在窗口内，不能证明没被遮挡或触发了业务，所以必须检查 calls/model。[源码]，M ui/headless.go:490–516。

时间用 Advance；从外部改 model 先 Frame，再推进。输入和 Frame 最多 settle 20 帧；FramePending 仍真需要定位持续动画或未收敛布局，不能无限调用 Frame 把错误磨过去。测 composer 清空后的自动回填，不能额外 Frame 隐藏失效遗漏。M ui/headless.go:256–318；L desktop/composer_test.go:32–76；[源码→验收]。

## 尺寸与截图口径

```go
tt.SetSize(2048, 1030)
tt.SetScale(1.25)
tt.SetDark(true)
img := tt.Image()
```

M ui/headless.go:371–409、456–468；MIT API；[源码]。Find 是 DIP，Image 是物理像素；记录 img.Bounds 实际值，高度有半像素取整。目标为逻辑2048×1030、像素比1.25，不是把图片缩成2048宽；全窗约2560×1288像素，最终以实际输出为准。主会话查看图时裁组件区域，裁切坐标从 DIP×scale 算并记录；原图与浅深色/状态名对应，禁止整页缩小图代替组件图。依据：任务截图规格及 M ui/headless.go:377–388、459–468；[项目约定/源码]。

覆盖目标/最小/窄/宽窗、浅深、TextScale、IME、长中文、空/加载/失败/恢复、禁用、键盘、浮层叠层；聊天再加10个角色、万条消息、很长历史，报告构建行数与位置变化。矩阵按本次改动选择必要邻居，不为了文字改动跑全套。依据：S 项目验收约定、M docs/ui/testing.md:28–79；[项目约定]。

## 真窗口：v0.7.4 automation

测试构建额外文件（包名与应用一致），M docs/plugins/automation.md:19–40，MIT：

```go
//go:build e2e

package main

import _ "github.com/egoist/mygo/plugins/automation"
```

从隔离的测试入口构建 `go build -tags e2e -o <任务目录>/app.exe .`，启动前设 MYGO_AUTOMATION 为任务目录里的 socket 路径；Windows 也用 unix socket。长目录可把工作目录设为任务目录、变量设为相对路径 `s`，客户端从同一目录连接；不改到共享临时根。必须保留 inspector，mygo_noinspector / 普通 mygo release build 会把 plugin 去掉。M docs/plugins/automation.md:19–44；[源码→运行配方]。

没有把 main 新增 automation CLI 当依赖；按 NDJSON 协议调用，以下每行是一次请求，window 使用 windows 实际返回 ID，key 是产品稳定 key：

```json
{"id":1,"method":"windows"}
{"id":2,"method":"tree","params":{"window":1}}
{"id":3,"method":"act","params":{"window":1,"key":"allow","action":"press"}}
{"id":4,"method":"idle","params":{"window":1,"timeout":5000}}
{"id":5,"method":"capture","params":{"window":1}}
```

出处：M docs/plugins/automation.md:46–90、158–177、247–259；[源码]。先找到窗口/目标再行动，每条检查 error.code/message；依赖上一步布局的操作之间 idle。tree 只有无障碍节点，不枚举所有装饰容器；IDs 是64位数字字符串，勿转 JS number。capture 返回 base64 PNG、width、height；保存后检查实际尺寸/内容。M docs/plugins/automation.md:104–156、207–212、231–234；[源码]。

Go 客户端核心可直接采用 M docs/plugins/automation.md:261–288 的 MIT socket 模式，但必须处理网络/JSON 错误，设置 deadline，不吞失败。完整实现由当前任务测试脚本负责；协议已给定，不新建生产客户端框架。[源码→项目选择]。

只给被测应用设置 MYGO_AUTOMATION；复用同一可执行文件作 Go 客户端时，客户端进程必须清掉该变量，否则其 plugin init 也会抢占 socket、等待尚未启动的 App。M plugins/automation/automation.go:16–33、66–70；[源码/本次启动竞态修正]。

每个状态：tree/业务投影确认动作 → idle → capture；持续 spinner/光标可能让 idle timeout，按有限 deadline 保存当前状态并解释，不能忽略 timeout 冒充稳定。capture 是运行中窗口内容的渲染图，验证真实窗口输入/布局路径；它不等同系统桌面合成抓屏，不能单独证明 D3D11 最终呈现、系统候选框、托盘和跨屏 DPI。M docs/plugins/automation.md:231–255；[源码→证据边界]。

不按进程名关闭程序；记录本次启动 PID，正常关闭或仅回收该进程树。真实 Sumika 的自动化必须用隔离数据和不联网的测试入口；本任务不改真实数据配置。依据：任务进程/数据约束；[项目约定]。

## 检查器 Issues 与 mygo vet

开发构建按 F12 / Ctrl+Shift+I，打开 Issues 并检查新增问题；Elements 看布局、Label/Role、focus/disabled，Performance 看 build/layout/paint。面板占内容宽，验目标布局时关闭面板再截图；另存 Issues 检查证据。v0.7.4 没有把 Issues 列表做成 automation 方法，不能杜撰 issues 请求。M docs/ui/inspector.md:9–30、59–85；M docs/plugins/automation.md:81–90；[源码]。

当前 native/go.mod 已声明 mygo tool，与应用共同固定 v0.7.4。从 native/ 指定本次应用目录，避免检查无关包：

```text
go tool mygo vet ./cmd/sumika-proto
```

未声明 tool 的独立模块可使用 `go run github.com/egoist/mygo/cmd/mygo@v0.7.4 vet <应用目录>`。两种入口必须固定为模块实际版本；不升级依赖来取得新命令。依据：S native/go.mod；M cmd/mygo/main.go:25–26、69–70；[源码]。vet 在指定目录先跑 go vet ./...，再分析构建值存储/捕获；它不是视觉检查。M cmd/mygo/vet_ui.go:27–45、108–110；[源码]。

## 帧统计与交回

启动前 MYGO_FRAME_STATS=all；同场景记录数据量、视口、scale、主题、GPU/内存路径、帧样本、build/pass/layout/paint/present 和进程分配/GC。检查动画结束/离屏后停止请求；万条历史测构建行数，前插测同一ID的Y位移。具体开关见 async-performance.md。M docs/ui/rendering.md:23–43、ui/framestats.go:13–41；[源码→项目验收]。

交回区分五项结果：Tester、真窗口插件、Issues、mygo vet、帧统计。附命令、退出码/失败、数据和图的位置；没做的写清范围。独立配方实跑只证明片段在固定版本的指定路径，不代表 Sumika 产品已接入或验收。[项目约定]，依据：S docs/决策记录.md:3362–3364。

## 本版配方实跑记录

2026-11-06，独立临时模块直接 require mygo v0.7.4，Go 1.27.3 windows/amd64、CGO_ENABLED=0。从 recipes.md 原样提取7个 Go 块一起编译，3个 Tester 场景实跑；命令均由本次任务的 heavy-job.lock 与 scripts/run-low.ps1 串行调起，单轮持锁设置25分钟上限，结束释放。[实跑，本次输出]

```text
go mod tidy                         exit 0
go test -count=1 -v .                exit 0; 3 tests PASS; package 6.434s
go tool mygo vet .                  exit 0
go build -o app.exe .                exit 0
app.exe client                      exit 0; native-app exit 0
```

| 配方 | 实际断言与输出 |
| --- | --- |
| 会话列表 | 重排仍选原ID；键盘切换；删除清选择；空态；10000项、末帧报告14行 |
| 气泡+浮动聊天 | 10004条；锚点 history:4999 前插Y位移0.000 DIP；上翻追加不拉回；composer 184.969→54.621 DIP，无额外输入/手动Frame自动回填；IME Enter不发送、Shift+Enter换行；idle FramePending=false |
| 审批卡 | ID/Revision正确；busy挡重复；失败可重试；服务结果回填；nil回调禁用；拒绝独立；真实工具执行0次 |

Tester 目标逻辑2048×1030、scale1.25，实际PNG为2560×1288；另测360×340窄窗。真窗口用独立编造数据演示，900×640 DIP、实际PNG1125×800；automation 验过 windows/tree/act/input/idle/capture，发送与审批投影正确。F12打开检查器并点Issues，树和截图显示 No issues so far.；应用正常退出，未按名字杀进程。[实跑]

MYGO_FRAME_STATS=all 共记录17帧，均GPU路径；总耗时p50=1.6ms、p95=281.4ms、最大281.4ms；阶段最大值 build0.7、layout6.6、paint4.4、present273.3ms。样本包含启动和检查器，数量少，不是稳定态帧率基准；不能外推为 Sumika 性能。[实跑，frame-summary.json]

本次临时模块、脚本、逐次失败/成功日志和截图均在任务的 $N/mygo-ui-skill/；未启动真实Sumika，未连接模型或平台服务，未操作真实审批。七个配方均编译；仅前三个跑了行为场景，工具卡/设置行/抽屉/全局待办未独立交互验收。真窗口没有验系统IME候选框、跨屏DPI、托盘、macOS/Linux或系统桌面合成抓屏。[实跑范围]
