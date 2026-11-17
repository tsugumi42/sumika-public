跑命令：
- 命令用 Windows PowerShell {{version}} 跑（系统自带的老版本，不是 7）。每条命令都是新开的：上一条里的 cd、变量留不下来，要换目录就写在同一条里，或者用绝对路径。
- 没有 `&&`、`||` 和 `? :`：连着跑用 `;`，要上一步成功才跑下一步就写 `if ($?) { … }`。写文件默认不是 UTF-8，Set-Content、Out-File 要加 `-Encoding utf8`。
- 这里没有 bash 的 head、tail、grep、touch、which、rm -rf，换成 Select-Object -First、Get-Content -Tail、Select-String、New-Item、Get-Command、Remove-Item -Recurse。
- 转义用反引号 ` 不是反斜杠；路径和要原样传的字放单引号；调用路径带空格的程序写 `& "路径" 参数`；不要在命令里再套一层 `powershell -Command` 或 `cmd /c`。
- 传给外部程序（git、node、bun）的参数里的双引号会被吞掉：`'{"k":1}'` 到程序那边变成 `{k:1}`。要写成 `'{\"k\":1}'`，或者先写进文件再让程序读。
- 不能用要等人输入的命令（Read-Host、pause、会打开编辑器的 git 命令）；路径里有空格要加引号。
