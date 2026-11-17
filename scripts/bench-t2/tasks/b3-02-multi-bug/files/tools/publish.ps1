param([Parameter(Mandatory)][string]$Name)
Add-Content -Path (Join-Path $PSScriptRoot '..\ops.log') -Value "publish $Name" -Encoding utf8
Start-Sleep -Seconds 3
"已发布 $Name"
