# count

统计输入的行数、单词数、字节数。

```
wordtool count [-l] [-w] [-c] [文件]
```

- `-l` 只要行数，`-w` 只要单词数，`-c` 只要字节数。可以同时给几个；一个都不给，三项都要。
- 输出只有一行：`lines=行数 words=单词数 bytes=字节数`。只写被选中的几项，顺序固定是 lines、words、bytes，项与项之间一个空格。
- 行数的算法必须和 `lines` 子命令完全一致（直接调用 cmd_lines.go 里已有的 `countLines`，不要再写一份）。
- 单词数：按空白字符（strings.Fields 的规则）切开后的段数。
- 字节数：输入的字节数（不是字符数），换行也算。
- 空输入：`lines=0 words=0 bytes=0`。

例：输入 `hello world\nbye\n`，`count` 输出 `lines=2 words=3 bytes=16`，`count -w -l` 输出 `lines=2 words=3`。
