param([Parameter(Mandatory)][string]$Dir)
$ErrorActionPreference = 'Stop'
Set-Location $Dir
function Sub([string]$p, [string]$from, [string]$to) {
  $t = [IO.File]::ReadAllText((Join-Path $Dir $p), [Text.Encoding]::UTF8)
  $t = $t.Replace($from, $to)
  [IO.File]::WriteAllText((Join-Path $Dir $p), $t, (New-Object Text.UTF8Encoding $false))
}
$pub = Join-Path $Dir 'tools\publish.ps1'

# api
Sub 'modules\api\package.json' '"version": "1.4.2"' '"version": "1.5.0"'
Sub 'modules\api\Dockerfile' 'harbor.version="1.4.2"' 'harbor.version="1.5.0"'
& $pub -Name api

# web
Sub 'modules\web\package.json' '"version": "1.4.2"' '"version": "1.5.0"'
Sub 'modules\web\package.json' '"@harbor/api": "1.4.2"' '"@harbor/api": "1.5.0"'
Sub 'modules\web\Dockerfile' '1.4.2' '1.5.0'
Sub 'modules\web\src\app-info.js' "'1.4.2'" "'1.5.0'"
& $pub -Name web

# worker
Sub 'modules\worker\version.go' 'const Version = "1.4.2"' 'const Version = "1.5.0"'
Sub 'modules\worker\Dockerfile' '1.4.2' '1.5.0'
& $pub -Name worker

# docs
Sub 'README.md' 'version-1.4.2-blue' 'version-1.5.0-blue'
Sub 'README.md' '当前版本：1.4.2' '当前版本：1.5.0'
Sub 'docs\install.md' '1.4.2' '1.5.0'
Sub 'docs\upgrade.md' '当前版本：1.4.2' '当前版本：1.5.0'
foreach ($m in 'api','web','worker') { Sub 'docker\compose.yml' "harbor/${m}:1.4.2" "harbor/${m}:1.5.0" }
$entry = @"
## [1.5.0] - 2026-10-27

### 新增
- api：任务列表支持按标签过滤（?tag=）
- web：任务页增加“复制链接”按钮

### 修复
- worker：队列为空时空转，占满一个 CPU 核
- web：深色模式下表格分隔线看不清

### 变更
- api：任务标题长度上限从 80 改为 120 个字符

"@
$entry = $entry -replace "`r`n", "`n"
Sub 'CHANGELOG.md' "## [未发布]`n`n" ("## [未发布]`n`n" + $entry)
Sub 'CHANGELOG.md' '[未发布]: https://example.com/harbor/compare/v1.4.2...HEAD' "[未发布]: https://example.com/harbor/compare/v1.5.0...HEAD`n[1.5.0]: https://example.com/harbor/compare/v1.4.2...v1.5.0"
& $pub -Name docs
'参考做法完成'
