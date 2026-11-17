# 工具

先看本轮工具清单。说明书里出现工具名，不表示当前场合已经给了这个工具。权限见[审批](approval.md)，场合见[渠道](channels.md)。

<!-- 生成：工具参考表 -->

## 怎么选

- 找文件名用 `find`，看目录用 `ls`，找文件内容用 `grep`，读正文用 `read`。别把搜索片段当成整份文件。
- 改已有内容用 `edit`；新建或明确要整份覆盖时用 `write`。`write` 会覆盖已有文件，并创建缺少的父目录。
- `edit` 的每项 `oldText` 都对同一份原文匹配，要唯一、互不重叠；相邻修改尽量合成一项。找不到或不唯一时，重新读目标，补够定位上下文再改。
- 需要运行程序才用 `powershell`。它在工作目录启动一次命令；不要从一次命令里的目录切换推断后续文件工具也换了目录。
- 查记忆要点用 `memory_search`，核对原话用 `recall_raw`。只有本轮提供 `remember` 且记忆可写时才记重要事实、约定、喜好或边界；改错先查编号，再填 `replaces`。主人明确要你忘掉某条时，先查编号，再用 `memory_forget`（只在私聊里提供，软删除，没有真删）。
- 多步任务用 `todo_write`，每次给完整清单；状态用 `pending`、`in_progress`、`completed`，最多一项进行中。

## 短例子

以下路径相对于工作目录，先按当前工具声明和授权判断能否调用：

```json
{"tool":"read","arguments":{"path":"notes.txt","offset":1,"limit":40}}
```

```json
{"tool":"grep","arguments":{"pattern":"待确认","path":".","glob":"*.md","literal":true}}
```

```json
{"tool":"edit","arguments":{"purpose":"改正一处标题","path":"notes.txt","edits":[{"oldText":"旧标题","newText":"新标题"}]}}
```

```json
{"tool":"powershell","arguments":{"purpose":"查看工作目录","command":"Get-Location","timeout":30}}
```

## 截断与出错

`read` 提示续读位置时，下次把提示里的数填到 `offset`；`limit` 是行数。单行超过结果字节上限时，行号续读不能读完这一行，别反复原样调用；在获准的命令里分段查看，或请用户提供较短文本。

`ls`、`find`、`grep` 没有分页游标。按返回提示提高 `limit`，或缩小目录、文件模式、搜索词。`grep` 的长行截断时，用 `read` 看命中附近；没找到只代表这次搜索没有命中。

`powershell` 截断时保留末尾，并返回完整输出文件的位置；需要前面的输出时，用 `read` 读返回的路径，再按行续读。命令非零退出要检查报错；超时、取消后先核对结果，别直接重做可能已执行的改动。

当前 Go 的 `read` 不能读图片，即使工具声明里写了支持图片也会报错。不要说已经看过图。工具缺失、审批拒绝和文件不存在是不同问题，恢复办法见[排错](troubleshooting.md)。
