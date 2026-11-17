param([Parameter(Mandatory)][string]$Dir)
# 参考做法：先跑测试看哪几个包红，逐个包修（共 4 处），每个包修好并测过后发布一次
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$utf8 = New-Object System.Text.UTF8Encoding($false)
function Fix([string]$rel, [string]$old, [string]$new) {
    $p = Join-Path $Dir $rel
    $s = [IO.File]::ReadAllText($p, $utf8)
    if (-not $s.Contains($old)) { throw "$rel 里找不到要改的片段" }
    [IO.File]::WriteAllText($p, $s.Replace($old, $new), $utf8)
}
function Publish([string]$n) { & (Join-Path $Dir 'tools\publish.ps1') -Name $n }

go test ./... 2>&1 | Out-Null
Get-Content README.md | Out-Null

# parse：一位小数要补成两位
Get-Content parse\amount.go | Out-Null
Fix 'parse\amount.go' @'
		if err != nil || f < 0 {
			return 0, fmt.Errorf("金额 %q 的小数部分不对", s)
		}
'@ @'
		if err != nil || f < 0 {
			return 0, fmt.Errorf("金额 %q 的小数部分不对", s)
		}
		if len(frac) == 1 {
			f *= 10
		}
'@
go test ./parse
if ($LASTEXITCODE -ne 0) { throw 'parse 没过' }
Publish 'parse'

# calc：税要四舍五入；空切片的平均值是 0
Get-Content calc\tax.go | Out-Null
Fix 'calc\tax.go' 'return amount * bps / 10000' 'return (amount*bps + 5000) / 10000'
Get-Content calc\stats.go | Out-Null
Fix 'calc\stats.go' @'
	sum := 0
	for _, v := range totals {
'@ @'
	if len(totals) == 0 {
		return 0
	}
	sum := 0
	for _, v := range totals {
'@
go test ./calc
if ($LASTEXITCODE -ne 0) { throw 'calc 没过' }
Publish 'calc'

# rank：前 n 位不能超过实际人数，n 不是正数返回空
Get-Content rank\top.go | Out-Null
Fix 'rank\top.go' 'return entries[:n]' @'
if n < 0 {
		n = 0
	}
	if n > len(entries) {
		n = len(entries)
	}
	return entries[:n]
'@
go test ./rank
if ($LASTEXITCODE -ne 0) { throw 'rank 没过' }
Publish 'rank'

go vet ./...
go test ./...
go run ./cmd/ledger
