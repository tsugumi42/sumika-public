# 子命令约定

所有子命令共用下面这些约定，各自的说明里不再重复。

## 代码结构

- 每个子命令是一个函数，签名固定为 `func(args []string, in io.Reader, out io.Writer) error`（类型名 `command`，见 main.go）。
- 函数名叫 `runXxx`，写在 `cmd_xxx.go` 里（现成的例子：`cmd_lines.go`）。
- 新子命令要登记到 main.go 的 `commands` 表里，名字就是命令行上敲的子命令名。
- 结果只写到 `out`，不要直接写 os.Stdout；出错返回 error，不要 panic，也不要调用 os.Exit。

## 参数与输入

- 参数用标准库的 flag 解析，请用 util.go 里的 `newFlags(name)` 建 FlagSet（它已经设成出错时返回 error、不往屏幕上打印用法）。
- 解析完以后用 util.go 里的 `readInput(fs, in)` 取输入：有文件参数就读第一个文件，没有就读 `in`。
- 传了不认识的参数，直接把 flag 的错误返回。

## 输出

- 每行输出以 `\n` 结尾。
- 带计数的行格式见各子命令的说明。需要把“数字 + 一行文字”拼在一起的，用 util.go 的 `formatCount`。
