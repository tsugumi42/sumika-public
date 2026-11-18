package archcheck

// drivePathBaseline 是 native/internal 下“字符串字面量里写盘符路径”的存量：规则见 drivepath_test.go 开头。
// 每项是文件（相对 native/internal，正斜杠）加解引号后内容的 SHA-256，值是为什么留着、什么时候去掉。
// 只许变少、不许新增：
//   - 新出现一处，TestInternalHasNoDriveLetterPaths 就红；
//   - 某一项已经不成立（文件没了、字面量改了一个字）也红，要从这里删掉，不留死条目。
//
// 键用内容哈希而不是整段字面量：analyzer_gen.go 里的 PowerShell 脚本有十 KB，原样放进清单没法看；
// 哈希变了说明那段内容动过，要重新确认里面没有别的盘符路径。往里加条目等于放宽规则，先在提交说明里写原因。
var drivePathBaseline = map[driveLiteral]string{
	{File: "policy/analyzer_gen.go", Hash: "4366a6f04ffbc3e597cb8f465dffff59e05550957850753f528cc97bb0e1ecca"}: `PowerShell 脚本里的注释举例：“按路径调用的（.\x.ps1、C:\x\tool.exe）不是 PowerShell 命令”；只是说明文字，脚本不读这个路径，换机器不受影响`,
	{File: "policy/userrules.go", Hash: "2534f90e6e6d8aadb8484a92446becaf5af9c3ce69b470b8928c266835eb64d0"}:    `报错文案的示例：“项目目录要写绝对路径，例如 D:\Projects\demo”；只给人看，不读盘，不绑定本机`,
}

// drivePathBaselineMax 是 drivePathBaseline 的条数，写死。清单只许变少：删条目时把它同步改小（测试会提醒）；
// 往清单加条目就得改大它——**只许改小；要改大先问主人**。
const drivePathBaselineMax = 2
