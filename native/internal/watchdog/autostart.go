package watchdog

import (
	"path"
	"strings"
)

// 开机自启（规格-看门狗 第五节）：Run 项启动的是 `"<exe>" -watch`，也就是守护进程，由它再拉起桌面程序。
// 原来 mygo 写的 Run 项是 `"<exe>" --mygo-opened-at-login`，直接启动桌面程序、没人看着；认作"开着"，第一次碰到时改写成 -watch 的写法。
// 为什么不用 mygo 的 SetOpenAtLogin：它写死参数，没法让 Run 项带 -watch。

// WatchFlag 是让 sumika.exe 当守护进程的命令行参数
const WatchFlag = "-watch"

// mygoLoginArg 是 mygo 以前写进 Run 项的参数（mygo/login.go 的 loginArg）
const mygoLoginArg = "--mygo-opened-at-login"

// RunStore 是"当前用户的 Run 项"的读写，Windows 上是注册表，测试里是假的
type RunStore interface {
	Get(name string) (value string, ok bool)
	Set(name, value string) error
	Delete(name string) error
	// Disabled：用户在任务管理器的"启动"页里关掉了它（StartupApproved）
	Disabled(name string) bool
	ClearDisabled(name string) error
}

// Autostart 管 Run 里 Name 这一项
type Autostart struct {
	Store RunStore
	Name  string // Run 里的值名：mygo 用的是应用名，这里沿用，才能把旧的那条替换掉而不是多出一条
	Exe   string // 这个 sumika.exe 的完整路径
}

func (a Autostart) command() string { return `"` + a.Exe + `" ` + WatchFlag }

// matches 判断 Run 项的值是不是“这个 exe 加这个参数”。路径按文件认，不按字面认：
// 大小写、\\?\ 前缀、正斜杠、多余的 . 和 ..、8.3 短路径名、有没有加引号，写法不同但指向同一个文件的都算。
// os.Executable 加 EvalSymlinks 给出的写法和注册表里存的写法不保证逐字相同，逐字比会把“开着”误判成“关”
func (a Autostart) matches(v, arg string) bool {
	exe, args, ok := splitCommand(v)
	if !ok {
		return false
	}
	want := normPath(a.Exe)
	return want != "" && normPath(exe) == want && args == arg
}

// splitCommand 把 Run 项的值拆成程序路径和参数（参数里的连续空白并成一个空格）。路径带引号或不带都认；引号没闭合、路径为空拆不出来
func splitCommand(v string) (exe, args string, ok bool) {
	v = strings.TrimSpace(v)
	var rest string
	if strings.HasPrefix(v, `"`) {
		end := strings.IndexByte(v[1:], '"')
		if end < 0 {
			return "", "", false
		}
		exe, rest = v[1:1+end], v[2+end:]
	} else if i := strings.IndexAny(v, " \t"); i >= 0 {
		exe, rest = v[:i], v[i:]
	} else {
		exe = v
	}
	if strings.TrimSpace(exe) == "" {
		return "", "", false
	}
	return exe, strings.Join(strings.Fields(rest), " "), true
}

// normPath 把路径化成可比较的写法：统一反斜杠、去掉 \\?\ 前缀、化简 . 和 ..、短路径名换成长的（只在 Windows 上、文件存在时）、转小写。
// 空路径返回空串。网络路径（\\服务器\共享）保留开头两个反斜杠
func normPath(p string) string {
	p = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
	switch {
	case strings.HasPrefix(p, "//?/unc/"): // 前面已经转成小写
		p = "//" + p[len("//?/unc/"):]
	case strings.HasPrefix(p, "//?/"), strings.HasPrefix(p, "//./"):
		p = p[len("//?/"):]
	}
	if p == "" {
		return ""
	}
	unc := strings.HasPrefix(p, "//")
	p = path.Clean(p)
	if unc {
		p = "/" + p
	}
	return strings.ToLower(longPath(strings.ReplaceAll(p, "/", `\`)))
}

// On 开机自启开没开：Run 项是守护进程的写法或 mygo 以前的写法，并且没被任务管理器关掉
func (a Autostart) On() bool {
	v, ok := a.Store.Get(a.Name)
	if !ok || !(a.matches(v, WatchFlag) || a.matches(v, mygoLoginArg)) {
		return false
	}
	return !a.Store.Disabled(a.Name)
}

// Set 开或关。开：写守护进程的写法，并解除任务管理器里的禁用（用户这次是明确要开）
func (a Autostart) Set(on bool) error {
	if !on {
		if err := a.Store.Delete(a.Name); err != nil {
			return err
		}
		return a.Store.ClearDisabled(a.Name)
	}
	if err := a.Store.Set(a.Name, a.command()); err != nil {
		return err
	}
	return a.Store.ClearDisabled(a.Name)
}

// Migrate 把 mygo 以前写的 Run 项改成守护进程的写法，别的什么都不动（连"被任务管理器关掉"的状态也留着）。
// 只认指向这个 exe 的旧写法：别的路径、别的写法一概不碰。改了返回 true
func (a Autostart) Migrate() (bool, error) {
	v, ok := a.Store.Get(a.Name)
	if !ok || !a.matches(v, mygoLoginArg) {
		return false, nil
	}
	if err := a.Store.Set(a.Name, a.command()); err != nil {
		return false, err
	}
	return true, nil
}
