# bench-t2 题库的来源和许可

`tasks/` 里的题目分三类：Exercism Go 题、自己出的本地题、LongMemEval 裁出来的记忆题。自己出的题内容全是编的，跟着仓库的 MIT 许可；另外两类保留上游许可。

## Exercism Go 题（`tasks/b1-a01` 到 `b1-a12`）

- 来源：Exercism Go track，https://github.com/exercism/go。
- 导入路径：从 Aider-AI/polyglot-benchmark 的浅克隆拷入，提交，导入脚本 `import-aider.ts` 记录了每个文件的 SHA-256（脚本已删，ADR-389，历史里还在）。Polyglot benchmark 仓库本身没有单独的 LICENSE，README 写明题目内容版权归 Exercism，按 Exercism 的开源许可使用。
- 许可证：MIT，版权归 Exercism。
- 导入时删掉了 `.meta`（参考解）、`.approaches`、`.articles`，没有把解法带进仓库。

```
MIT License

Copyright (c) 2021 Exercism

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## LongMemEval（`tasks/b4-*`）

- 来源：LongMemEval，https://github.com/xiaowu0162/LongMemEval（ICLR 2025）。
- 用法：从 cleaned 版 oracle 里裁 10 问，每问拼成长历史写成 `Task.seed`，由 `import-longmemeval.ts` 生成（脚本已删，ADR-389，历史里还在）；裁过的只能叫 Sumika-B4，不是 LongMemEval 官方分。
- 许可证：MIT，版权归 Di Wu。

```
MIT License

Copyright (c) 2024 Di Wu

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## `tasks/b1-l07/files/assets/video/` 的两个 bin

`clip.bin`（12,000 字节）和 `clip-preview.bin`（7,000 字节）不是视频，也不来自任何第三方：是 2026-10-27 出 b1-l07“找最大的文件”时手写的纯 ASCII 填充，前者是 12,000 个 `V`，后者是 7,000 个 `v`，只为让 `clip.bin` 成为 `assets/` 下最大的文件。没有生成脚本，提交历史里和其他题目文件一起进来。
