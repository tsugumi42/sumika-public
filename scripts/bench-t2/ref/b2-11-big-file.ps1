param([Parameter(Mandatory)][string]$Dir)
# 参考做法：读说明和测试，把参考实现（b2-11-big-file.semver.go.txt）原样写成 semver.go，跑一遍测试和 vet
$ErrorActionPreference = 'Stop'
Set-Location $Dir
Get-Content docs\spec.md | Out-Null
Get-Content semver_test.go | Out-Null
Copy-Item (Join-Path $PSScriptRoot 'b2-11-big-file.semver.go.txt') (Join-Path $Dir 'semver.go')
go test ./... 2>&1 | Out-Null
go vet ./... 2>&1 | Out-Null
