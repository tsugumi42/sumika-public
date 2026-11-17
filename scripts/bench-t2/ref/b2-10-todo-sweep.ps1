param([Parameter(Mandatory)][string]$Dir)
# b2-10-todo-sweep 的参考做法：收集 TODO 写 TODO.md，再改掉 3 个 easy 项，每项发布一次
$ErrorActionPreference = 'Stop'
$enc = New-Object System.Text.UTF8Encoding($false)
function ReadF($rel) { [IO.File]::ReadAllText((Join-Path $Dir $rel)) }
function WriteF($rel, $text) { [IO.File]::WriteAllText((Join-Path $Dir $rel), $text, $enc) }
Push-Location $Dir
try {
    $todo = @'
# TODO

| 优先级 | 文件 | 类型 | 内容 |
|---|---|---|---|
| P1 | scripts/backup.sh | normal | 备份前先检查磁盘空间 |
| P1 | scripts/migrate.sql | normal | 迁移前先备份数据库 |
| P1 | src/config.py | normal | 支持从配置文件读取，不要只靠环境变量 |
| P1 | src/inventory.py | normal | 并发修改时要加锁 |
| P1 | src/web/server.js | normal | 上线前必须换掉硬编码的端口 |
| P2 | scripts/backup.sh | easy | 保留份数从 3 改成 7 |
| P2 | src/app.py | normal | 命令行参数改用 argparse |
| P2 | src/config.py | easy | 默认超时改成 60 秒 |
| P2 | src/report.py | normal | 报表支持导出 CSV |
| P2 | src/web/server.js | normal | 加上请求日志 |
| P2 | src/web/static/app.js | normal | 搜索框加防抖 |
| P3 | scripts/lint.sh | normal | 也检查 .js 文件 |
| P3 | scripts/migrate.sql | normal | 给 shelf_items.isbn 加索引 |
| P3 | scripts/seed.py | normal | 示例数据再多一点 |
| P3 | src/util.py | easy | 把 fmt_name 改名成 format_name，所有调用的地方一起改 |
| P3 | src/util.py | normal | 负数的情况还没处理 |
| P3 | src/web/static/style.css | normal | 深色模式的配色 |
'@
    WriteF 'TODO.md' $todo
    & (Join-Path $Dir 'tools\publish.ps1') -Name collect

    # easy 1：默认超时
    $t = ReadF 'src\config.py'
    $t = $t.Replace('DEFAULT_TIMEOUT = 30  # TODO(p2) [easy]: 默认超时改成 60 秒', 'DEFAULT_TIMEOUT = 60')
    WriteF 'src\config.py' $t
    & (Join-Path $Dir 'tools\publish.ps1') -Name easy-timeout

    # easy 2：改名，定义和所有调用处
    $t = ReadF 'src\util.py'
    $t = $t.Replace("    # TODO(p3) [easy]: 把 fmt_name 改名成 format_name，所有调用的地方一起改`n", '').Replace("    # TODO(p3) [easy]: 把 fmt_name 改名成 format_name，所有调用的地方一起改`r`n", '')
    WriteF 'src\util.py' ($t.Replace('fmt_name', 'format_name'))
    foreach ($f in 'src\inventory.py', 'src\report.py', 'src\app.py') {
        WriteF $f ((ReadF $f).Replace('fmt_name', 'format_name'))
    }
    & (Join-Path $Dir 'tools\publish.ps1') -Name easy-rename

    # easy 3：备份份数
    $t = ReadF 'scripts\backup.sh'
    $t = $t.Replace('KEEP=3   # TODO(P2) [easy]: 保留份数从 3 改成 7', 'KEEP=7')
    WriteF 'scripts\backup.sh' $t
    & (Join-Path $Dir 'tools\publish.ps1') -Name easy-keep

    # 自检：不能再有 fmt_name 和 [easy]
    $left = Get-ChildItem -Recurse -File src, scripts | Select-String -SimpleMatch 'fmt_name', '[easy]'
    if ($left) { throw "还有残留：$($left | Out-String)" }
    python -B -c "import sys; sys.path.insert(0, 'src'); import app" | Out-Null
}
finally { Pop-Location }
