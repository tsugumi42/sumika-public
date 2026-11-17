param([Parameter(Mandatory)][string]$Dir)
# 参考做法：按 NAMING.md 改四个名字，改默认值，补一个测试；每项做完发布一次
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$utf8 = New-Object System.Text.UTF8Encoding($false)
function Rename-In([string[]]$Files) {
    foreach ($f in $Files) {
        $p = Join-Path $Dir $f
        $s = [IO.File]::ReadAllText($p, $utf8)
        $s = $s -replace '\bLoadCfg\b', 'Load'
        $s = $s -replace '\bCfg\b', 'Config'
        $s = $s -replace '\bGetQty\b', 'Quantity'
        $s = $s -replace '\bPrintLowStock\b', 'WriteLowStock'
        [IO.File]::WriteAllText($p, $s, $utf8)
    }
}
function Publish([string]$n) { & (Join-Path $Dir 'tools\publish.ps1') -Name $n }

Get-Content NAMING.md | Out-Null
Get-Content README.md | Out-Null
Rename-In @('config\config.go', 'config\config_test.go')
go build ./config
Publish 'rename-config'

Rename-In @('store\store.go', 'store\store_test.go')
Publish 'rename-store'

Rename-In @('report\lowstock.go', 'report\summary.go', 'report\report_test.go')
Publish 'rename-report'

Rename-In @('export\csv.go', 'export\csv_test.go', 'cmd\shelf\main.go')
Publish 'rename-callers'
go build ./...

Rename-In @('README.md')
Publish 'docs'

$p = Join-Path $Dir 'config\defaults.json'
$s = [IO.File]::ReadAllText($p, $utf8)
$s = $s -replace '"low_stock_threshold": 5', '"low_stock_threshold": 10'
$s = $s -replace '"page_size": 20', '"page_size": 50'
[IO.File]::WriteAllText($p, $s, $utf8)
$p = Join-Path $Dir 'README.md'
$s = [IO.File]::ReadAllText($p, $utf8)
$s = $s -replace '\| 5 \|', '| 10 |'
$s = $s -replace '\| 20 \|', '| 50 |'
[IO.File]::WriteAllText($p, $s, $utf8)
Publish 'defaults'

$test = @'
package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.LowStockThreshold != 10 || c.PageSize != 50 || c.Currency != "CNY" {
		t.Errorf("默认值不对：%+v", c)
	}
}
'@
[IO.File]::WriteAllText((Join-Path $Dir 'config\load_test.go'), $test, $utf8)
go vet ./...
go test ./...
Publish 'new-test'
