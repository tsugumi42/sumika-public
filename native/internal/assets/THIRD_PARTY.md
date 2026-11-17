# 软件自带素材的来源和许可

这些素材由 `internal/assets` 提供（图标、表情、字体），发出去的程序要带上对应的许可证文件。

| 素材 | 来源 | 许可证 | 许可证文件 | 放在哪 |
|---|---|---|---|---|
| MingCute 图标（全套，line、fill） | npm 包 [mingcute_icon](https://www.npmjs.com/package/mingcute_icon) 2.9.72（作者官方，现在改名 @mingcute/svg；MingCute Icon，https://mingcute.com） | Apache-2.0 | [icons/LICENSE-MingCute](icons/LICENSE-MingCute) | 生成进 Go 源码（`icons/icons_gen.go`），编进 exe |
| Twemoji 表情（4009 个 SVG） | 社区版 [jdecked/twemoji](https://github.com/jdecked/twemoji) v17.0.3 | 图形 CC-BY 4.0（代码 MIT） | 下载时一起放进 `twemoji/LICENSE-GRAPHICS` | `native/twemoji/`（不进 git），打包时拷到 exe 旁的 `twemoji/` |
| 文渊圆体（WenYuan Rounded SC VF，Takushun Wu） | https://github.com/takushun-wu/ （许可证文件里写着各部分的来源） | 各部分许可证见文件 | [native/fonts/WenYuan-LICENSE.md](../../fonts/WenYuan-LICENSE.md) | `native/fonts/`（字体文件不进 git），打包时拷到 exe 旁的 `fonts/` |
| Maple Mono NF CN（subframe7536/maple-font） | https://github.com/subframe7536/maple-font | OFL-1.1 | [native/fonts/MapleMono-OFL.txt](../../fonts/MapleMono-OFL.txt) | 同上 |

CC-BY 4.0 要求署名：Twemoji 的署名要在设置里能看到（待办“界面资源打包”里记着这一项）。
