package tools

// 桌面三件：clipboard（读写剪贴板文字）、open（用系统默认程序打开文件、文件夹或网址）、notify（弹一条系统通知）。
// 只给主人 × 本地私聊用（policy.Specs 里 owner-console，和 powershell 一样）。平台相关的实际动作在 desktop_windows.go / desktop_other.go，
// 这里只管参数校验、截断和限流；测试里把那几个变量换成假函数。
// 声明是手写的（生成器 scripts/gen-go-tools.ts 已随 Bun 退役），所以加在这里，OwnerConsole 把它们接在内置工具后面。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

const (
	clipboardMaxChars = 20000
	notifyInterval    = 10 * time.Second
)

// 平台相关的动作，测试里换掉
var (
	clipboardReadText  = readClipboardText
	clipboardWriteText = writeClipboardText
	openWithDefault    = openDefault
	// notifyFn 由装配处注入（桌面入口里用 mygo 的通知）；没注入时 notify 报错，不会悄悄不弹
	notifyFn  func(title, body string) error
	notifyNow = time.Now
)

// SetNotifier 由桌面入口在启动时调用，把系统通知出口接上
func SetNotifier(fn func(title, body string) error) { notifyFn = fn }

// desktopDecls 是三个工具给模型看的声明，接在内置工具后面
var desktopDecls = []Declaration{
	{Name: "clipboard", Description: "Read or write the text on the Windows clipboard. action \"read\" returns the clipboard text, cut to 20000 characters with a note when longer; if the clipboard holds no text (an image, a file list, or nothing) it says so. action \"write\" replaces the clipboard text with `text`. Reading needs no approval; writing follows the same approval mode as the write tool.", Parameters: `{"type":"object","required":["action"],"properties":{"action":{"type":"string","enum":["read","write"],"description":"read 读剪贴板里的文字；write 把 text 写进剪贴板"},"text":{"type":"string","description":"write 时要写入的文字"},"purpose":{"type":"string","description":"这一步要做什么，用一句话说清楚（30 字以内），会显示给所有者看"}}}`},
	{Name: "open", Description: "Open a local file, a folder, or an http/https URL with the system's default program. A path must already exist; a URL must start with http:// or https://, other schemes are refused. Always needs the owner's approval, like a shell command.", Parameters: `{"type":"object","required":["target"],"properties":{"target":{"type":"string","description":"文件路径、文件夹路径，或 http/https 网址"},"purpose":{"type":"string","description":"这一步要做什么，用一句话说清楚（30 字以内），会显示给所有者看"}}}`},
	{Name: "notify", Description: "Show one Windows desktop notification with a title and an optional body. At most one notification every 10 seconds per conversation; a call sooner than that is refused with a short note. Needs no approval.", Parameters: `{"type":"object","required":["title"],"properties":{"title":{"type":"string","description":"通知标题，一句话"},"body":{"type":"string","description":"通知正文，可省略"}}}`},
}

// clipboardTool 读或写剪贴板文字
func clipboardTool(args jsjson.Value) (string, error) {
	action, _ := argString(args, "action")
	switch action {
	case "read":
		text, isText, err := clipboardReadText()
		if err != nil {
			return "", fmt.Errorf("读剪贴板失败：%w", err)
		}
		if !isText {
			return "剪贴板里不是文字", nil
		}
		if n := store.JSLen(text); n > clipboardMaxChars {
			return store.JSSlice(text, clipboardMaxChars) + fmt.Sprintf("\n…（剪贴板文字共 %d 字，只给了前 %d 字）", n, clipboardMaxChars), nil
		}
		return text, nil
	case "write":
		text, ok := argString(args, "text")
		if !ok {
			return "", errors.New("write 要带 text（要写进剪贴板的文字）")
		}
		if err := clipboardWriteText(text); err != nil {
			return "", fmt.Errorf("写剪贴板失败：%w", err)
		}
		return fmt.Sprintf("已写入剪贴板（%d 字）", store.JSLen(text)), nil
	default:
		return "", errors.New("action 只能是 read 或 write")
	}
}

// openTool 用系统默认程序打开文件、文件夹或 http/https 网址
func openTool(cwd string, args jsjson.Value) (string, error) {
	raw, _ := argString(args, "target")
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", errors.New("target 要写文件路径、文件夹路径或 http/https 网址")
	}
	var target string
	if strings.Contains(t, "://") {
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", errors.New("网址只认 http 和 https")
		}
		target = t
	} else {
		abs := resolveToCwd(t, cwd)
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("路径不存在：%s", t)
		}
		target = abs
	}
	if err := openWithDefault(target); err != nil {
		return "", fmt.Errorf("打开失败：%w", err)
	}
	return "已打开：" + t, nil
}

// notify 的限流：每个会话记上一次成功（或正在进行的）通知的时间
var (
	notifyMu   sync.Mutex
	notifyLast = map[string]time.Time{}
)

// notifyTool 弹一条系统通知；同一会话 10 秒内第二条起返回"刚通知过"
func notifyTool(ctx context.Context, args jsjson.Value) (string, error) {
	title, _ := argString(args, "title")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("title 要写一句标题")
	}
	body, _ := argString(args, "body")
	key := kernel.InvocationFromContext(ctx).Scope.Conversation

	notifyMu.Lock()
	now := notifyNow()
	prev, seen := notifyLast[key]
	if seen && now.Sub(prev) < notifyInterval {
		notifyMu.Unlock()
		return "刚通知过，稍后再发", nil
	}
	notifyLast[key] = now
	notifyMu.Unlock()

	if notifyFn == nil {
		restoreNotify(key, prev, seen)
		return "", errors.New("系统通知没有接上")
	}
	if err := notifyFn(title, body); err != nil {
		restoreNotify(key, prev, seen)
		return "", fmt.Errorf("发通知失败：%w", err)
	}
	return "已发出通知", nil
}

// restoreNotify 发不出去就把限流时间退回去，让下一次能再试
func restoreNotify(key string, prev time.Time, seen bool) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if seen {
		notifyLast[key] = prev
	} else {
		delete(notifyLast, key)
	}
}
