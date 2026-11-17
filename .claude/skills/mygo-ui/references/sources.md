# 来源、版本与复制边界

所有路径相对下表仓库；引用形如「M ui/list.go:42–65」，同时给出仓库、文件、行号和固定版本。同一句中 L 后续省略目录的文件名和 model/ 路径仍相对 desktop/，S 后续 images.go 等相对已写出的 native/。本页代号适用于整个 skill；不要把 A/B/Lorca 的笔记行号当成 v0.7.4 源码行号。核对日期：2026-11-06。

## 一手出处

| 代号 | 仓库与固定版本 | 许可 | 本 skill 用途 |
| --- | --- | --- | --- |
| M | [egoist/mygo v0.7.4](https://github.com/egoist/mygo/tree/9bd34e1eb9525d9a1fe698aadc964c49c8ff8e7e)，9bd34e1eb9525d9a1fe698aadc964c49c8ff8e7e | MIT | API、文档、模板、示例、Tester、automation、vet；最终依据 |
| K | [egoist/kero](https://github.com/egoist/kero/tree/6a7e239ec7f1c685f6a2ac2d10810208379235ed)，6a7e239ec7f1c685f6a2ac2d10810208379235ed | MIT | 设置行和 styled helper 的布局；其 mygo 是 v0.7.5 伪版本，片段需改写 |
| L | [egoist/lorca](https://github.com/egoist/lorca/tree/bf1ab709bc8df470016522578d7ae2df7740cd22)，bf1ab709bc8df470016522578d7ae2df7740cd22 | GPL-3.0 | 仅结构、机制、取值；desktop/go.mod:3–7 为 v0.6.4-0.20261024094029-23ac61d856d7 |
| O | [oneo-me/onevra](https://github.com/oneo-me/onevra/tree/aaa46dc209d49dcf176a02e42d41bc472ab11670)，aaa46dc209d49dcf176a02e42d41bc472ab11670 | MIT | source/components/chat/floating.go 的高度回填和内容避让；不搬其 Scroll 长历史容器 |
| S | tsugumi42/sumika，本工作树基准 f20cbe67a5fd44a9212d6f463c141409a019ff33 | 项目自身 | 只作反例、项目要求或必须保留的现状，不作外部设计背书 |

固定链接格式：https://github.com/仓库/blob/完整提交/文件#L起行。上表版本不随滚动 main 变化。升级后重新执行 `go list -m -f '{{.Dir}}' github.com/egoist/mygo`，核对实际 replace，再更新本 skill。依据：M docs/ui/views.md:191–196；[源码]。

## 研究输入与取舍

本次读完 A 路 10 份主题笔记、SKILL 草稿和来源清单；B 路 10 份主题笔记、SKILL、项目对照表、Sumika 对照、官方 examples 对照和资料来源；Lorca 总览、短入口和 10 份 references。它们位于本次任务给定的 scratchpad/mygo-study-a、mygo-study-b、mygo-study-lorca；这些临时路径不是交付后的依赖。

- A 路提供官方文档/应用分层和版本漂移线索；本稿逐项回查 M，删除 main 专属 API，修正输入和动画语义。可信度：[材料→源码核对]，具体差异见 pitfalls.md。
- B 路提供「库有组件不等于满足生产集合」「浮动 composer 留白」「显式 ID 和任务代次」的对照。只保留经 M/O 证实的机制；未引入这些项目的依赖。可信度：[材料→源码核对]；O source/components/chat/floating.go:26–32、68–81、119–130，M ui/list.go:35–97。
- Lorca 提供 palette → Theme → 控件、三栏退让、真实测高回填、审批版本和全局 Attention 的机制；仅独立描述，GPL 源码不复制。可信度：[源码]；L desktop/theme.go:263–295、mainwindow.go:534–562、chat.go:203–212、sheet_review.go:175–234、attention.go:378–439。
- nsh 两个官方 skill 只借短入口、路由表、单主题 references、片段与出处的组织形式；不把其一次运行的响应式模型或规则写成 mygo 事实。格式来源：sumika-nsh/.agents/skills/nsh/SKILL.md:33–58、122–180，nsh-design-guides/SKILL.md:37–60；[文档，仅格式]。

## 可信度

- [源码]：读到固定版本实现或 API 契约；不能证明原生运行效果。
- [文档]：只由该版本文档/研究记录支持。
- [实跑]：本次执行过的独立 v0.7.4 配方；范围和输出见 testing.md 的实跑记录。
- [推断]：由事实提出的设计建议、维护风险；不是已复现故障。
- [项目约定]：ADR-279 或本次任务要求；不冒充框架强制规则。

代码块均注明 API 来源；配方是独立组合改写。源码阅读不能把「所有片段都跑过」作为结论。GPL/AGPL 来源只记结构、做法和取值，本 skill 不摘录其实现代码；即便以后补摘录，每处也不得超过 5 行并须标许可证。无明确许可证的应用不复制代码。依据：本次任务复制约束；[项目约定]。

## MIT 片段许可

本文档中采用 mygo 和 Kero 的 MIT 示例模式，版权分别为 Copyright (c) 2026 MyGo contributors、Copyright (c) 2026 EGOIST；来源为 M LICENSE:1–21、K LICENSE:1–21。[源码]。保留以下许可；将实质片段再分发到产品时一并保留原作者版权和许可。

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
