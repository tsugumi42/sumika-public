# 第三方内容与许可

Sumika 本体以 MIT 许可发布（[LICENSE](LICENSE)）。仓库里另外带着下面这些第三方内容，各自保留原许可证；改了之后再分发的部分仍按原许可证发布。

| 内容 | 来源 | 许可证 | 许可文本 |
|---|---|---|---|
| MingCute 图标（全套，生成进 `icons_gen.go`） | npm 包 [mingcute_icon](https://www.npmjs.com/package/mingcute_icon) 2.9.72，https://mingcute.com | Apache-2.0 | [native/internal/assets/icons/LICENSE-MingCute](native/internal/assets/icons/LICENSE-MingCute) |
| Lucide 图标（`native/assets/nav/` 的 10 个 SVG，1.58.0） | https://lucide.dev，https://github.com/lucide-icons/lucide | ISC（Feather 部分 MIT） | [native/assets/nav/LICENSE.txt](native/assets/nav/LICENSE.txt) |
| goSentencePiece（fork，只读 tokenizer.json） | https://github.com/tggo/goSentencePiece v1.1.0 | MIT | [native/third_party/goSentencePiece/LICENSE](native/third_party/goSentencePiece/LICENSE) |
| MewTypeUI 组件库 | https://github.com/tsugumi42/MewTypeUI（自有组件库） | MIT | [native/third_party/MewTypeUI/LICENSE](native/third_party/MewTypeUI/LICENSE) |
| modern-web-guidance（AI skill，v1.0.0） | https://github.com/GoogleChrome/modern-web-guidance | Apache-2.0 | [.claude/skills/modern-web-guidance/LICENSE](.claude/skills/modern-web-guidance/LICENSE) |
| mygo / Kero 模板和示例（`mygo-maintenance`、`mygo-ui` skill） | https://github.com/egoist/mygo v0.9.1、https://github.com/egoist/kero | MIT | [native/cmd/logotool/LICENSE-mygo](native/cmd/logotool/LICENSE-mygo)；Kero 的版权和许可全文在 [.claude/skills/mygo-ui/references/sources.md](.claude/skills/mygo-ui/references/sources.md) |
| Exercism Go 题（经 Aider-AI/polyglot-benchmark 导入 `scripts/bench-t2/tasks/`） | https://github.com/Aider-AI/polyglot-benchmark，题目来自 https://github.com/exercism/go | MIT | [scripts/bench-t2/LICENSES.md](scripts/bench-t2/LICENSES.md) |
| LongMemEval（裁进 `scripts/bench-t2/tasks/b4-*`） | https://github.com/xiaowu0162/LongMemEval（ICLR 2025） | MIT | [scripts/bench-t2/LICENSES.md](scripts/bench-t2/LICENSES.md) |
| 文渊圆体（界面字体，字体文件不进 git） | https://github.com/takushun-wu/ | SIL OFL 1.1（含保留字体名） | [native/fonts/WenYuan-LICENSE.md](native/fonts/WenYuan-LICENSE.md)；测试用改名子集的许可证在 [native/testdata/fonts/OFL.txt](native/testdata/fonts/OFL.txt) |
| Maple Mono NF CN（等宽字体，字体文件不进 git） | https://github.com/subframe7536/maple-font | SIL OFL 1.1 | [native/fonts/MapleMono-OFL.txt](native/fonts/MapleMono-OFL.txt) |
| Twemoji 表情（运行时下载，不在仓库里） | https://github.com/jdecked/twemoji v17.0.3 | 图形 CC-BY 4.0（代码 MIT），要署名 | 下载脚本把 `LICENSE-GRAPHICS` 一起放到 `native/twemoji/`，不进 git；CC-BY 4.0 全文见 https://creativecommons.org/licenses/by/4.0/ |

软件自带素材的详细来源、放置位置和打包说明见 [native/internal/assets/THIRD_PARTY.md](native/internal/assets/THIRD_PARTY.md)，这里不重复。

字体、图标等素材文件本身大多不进 git（`.gitignore` 排除），进仓库的是来源说明和许可文本；发布程序时按 `native/README.md` 的打包步骤把对应许可文件放到 exe 旁边。
