package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
)

func desktopArgs(t *testing.T, s string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// stubClipboard 换掉剪贴板的读写；返回写入的文字和"写过没有"的记号
func stubClipboard(t *testing.T, text string, isText bool, readErr error) (*string, *bool) {
	t.Helper()
	oldRead, oldWrite := clipboardReadText, clipboardWriteText
	t.Cleanup(func() { clipboardReadText, clipboardWriteText = oldRead, oldWrite })
	clipboardReadText = func() (string, bool, error) { return text, isText, readErr }
	var written string
	wrote := false
	clipboardWriteText = func(s string) error {
		written, wrote = s, true
		return nil
	}
	return &written, &wrote
}

// stubOpen 换掉默认程序打开；返回打开的目标和"打开过没有"的记号
func stubOpen(t *testing.T) (*string, *bool) {
	t.Helper()
	old := openWithDefault
	t.Cleanup(func() { openWithDefault = old })
	var target string
	opened := false
	openWithDefault = func(s string) error {
		target, opened = s, true
		return nil
	}
	return &target, &opened
}

// stubNotify 换掉通知出口，时钟可以手动拨；每次测试前清空限流记录
func stubNotify(t *testing.T, fn func(title, body string) error) *time.Time {
	t.Helper()
	oldFn, oldNow := notifyFn, notifyNow
	t.Cleanup(func() {
		notifyFn, notifyNow = oldFn, oldNow
		notifyMu.Lock()
		notifyLast = map[string]time.Time{}
		notifyMu.Unlock()
	})
	notifyMu.Lock()
	notifyLast = map[string]time.Time{}
	notifyMu.Unlock()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	notifyNow = func() time.Time { return now }
	notifyFn = fn
	return &now
}

func convCtx(conv string) context.Context {
	return kernel.WithInvocation(context.Background(), kernel.Invocation{Scope: kernel.Scope{Conversation: conv}})
}

func TestClipboardReadText(t *testing.T) {
	stubClipboard(t, "你好 hello", true, nil)
	got, err := clipboardTool(desktopArgs(t, `{"action":"read"}`))
	if err != nil || got != "你好 hello" {
		t.Fatalf("读文字 = %q, %v", got, err)
	}
}

func TestClipboardReadNotText(t *testing.T) {
	stubClipboard(t, "", false, nil)
	got, err := clipboardTool(desktopArgs(t, `{"action":"read"}`))
	if err != nil || got != "剪贴板里不是文字" {
		t.Fatalf("非文字 = %q, %v", got, err)
	}
}

func TestClipboardReadTruncates(t *testing.T) {
	stubClipboard(t, strings.Repeat("字", clipboardMaxChars+5), true, nil)
	got, err := clipboardTool(desktopArgs(t, `{"action":"read"}`))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.SplitN(got, "\n…", 2)
	if len([]rune(head[0])) != clipboardMaxChars {
		t.Fatalf("截断后的正文 %d 字，要 %d 字", len([]rune(head[0])), clipboardMaxChars)
	}
	if !strings.Contains(got, "共 20005 字，只给了前 20000 字") {
		t.Fatalf("没有注明截断：…%q", got[len(got)-60:])
	}
}

func TestClipboardReadError(t *testing.T) {
	stubClipboard(t, "", false, errors.New("占着"))
	if _, err := clipboardTool(desktopArgs(t, `{"action":"read"}`)); err == nil {
		t.Fatal("读失败应该报错")
	}
}

func TestClipboardWrite(t *testing.T) {
	written, wrote := stubClipboard(t, "", false, nil)
	got, err := clipboardTool(desktopArgs(t, `{"action":"write","text":"abc"}`))
	if err != nil || !*wrote || *written != "abc" || got != "已写入剪贴板（3 字）" {
		t.Fatalf("写 = %q, %v, wrote=%v, 内容=%q", got, err, *wrote, *written)
	}
}

func TestClipboardWriteNeedsText(t *testing.T) {
	_, wrote := stubClipboard(t, "", false, nil)
	if _, err := clipboardTool(desktopArgs(t, `{"action":"write"}`)); err == nil || *wrote {
		t.Fatal("write 没有 text 应该报错且不写")
	}
}

func TestClipboardBadAction(t *testing.T) {
	_, wrote := stubClipboard(t, "", false, nil)
	if _, err := clipboardTool(desktopArgs(t, `{"action":"delete"}`)); err == nil || *wrote {
		t.Fatal("action 不对应该报错且不写")
	}
}

func TestOpenURLAllowed(t *testing.T) {
	target, opened := stubOpen(t)
	got, err := openTool(t.TempDir(), desktopArgs(t, `{"target":"https://example.com/a?b=1"}`))
	if err != nil || !*opened || *target != "https://example.com/a?b=1" || got != "已打开：https://example.com/a?b=1" {
		t.Fatalf("网址 = %q, %v, 打开=%q", got, err, *target)
	}
}

func TestOpenRejectsOtherSchemes(t *testing.T) {
	_, opened := stubOpen(t)
	for _, s := range []string{`"ftp://example.com/x"`, `"file:///C:/Windows/win.ini"`, `"https://"`, `"javascript:alert(1)"`} {
		if _, err := openTool(t.TempDir(), desktopArgs(t, `{"target":`+s+`}`)); err == nil {
			t.Errorf("%s 应该被拒绝", s)
		}
	}
	if *opened {
		t.Fatal("被拒绝的目标不应该打开")
	}
}

func TestOpenMissingPath(t *testing.T) {
	_, opened := stubOpen(t)
	if _, err := openTool(t.TempDir(), desktopArgs(t, `{"target":"nope.txt"}`)); err == nil || *opened {
		t.Fatal("路径不存在应该报错且不打开")
	}
}

func TestOpenExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	target, opened := stubOpen(t)
	if _, err := openTool(dir, desktopArgs(t, `{"target":"a.txt"}`)); err != nil || !*opened {
		t.Fatalf("存在的文件应该打开：%v", err)
	}
	if filepath.Base(*target) != "a.txt" || !filepath.IsAbs(*target) {
		t.Fatalf("打开的应该是绝对路径，实际 %q", *target)
	}
}

func TestOpenNeedsTarget(t *testing.T) {
	_, opened := stubOpen(t)
	if _, err := openTool(t.TempDir(), desktopArgs(t, `{"target":"  "}`)); err == nil || *opened {
		t.Fatal("空的 target 应该报错")
	}
}

func TestNotifyLimitedPerConversation(t *testing.T) {
	var titles []string
	now := stubNotify(t, func(title, body string) error {
		titles = append(titles, title)
		return nil
	})
	ctx := convCtx("console:private:a")
	args := desktopArgs(t, `{"title":"第一条","body":"正文"}`)

	if got, err := notifyTool(ctx, args); err != nil || got != "已发出通知" {
		t.Fatalf("第一条 = %q, %v", got, err)
	}
	*now = now.Add(3 * time.Second)
	if got, _ := notifyTool(ctx, desktopArgs(t, `{"title":"第二条"}`)); got != "刚通知过，稍后再发" {
		t.Fatalf("3 秒内第二条 = %q", got)
	}
	*now = now.Add(7 * time.Second)
	if got, err := notifyTool(ctx, desktopArgs(t, `{"title":"第三条"}`)); err != nil || got != "已发出通知" {
		t.Fatalf("10 秒后第三条 = %q, %v", got, err)
	}
	if len(titles) != 2 || titles[0] != "第一条" || titles[1] != "第三条" {
		t.Fatalf("实际发出的标题 = %v", titles)
	}
}

func TestNotifyLimitIsPerConversation(t *testing.T) {
	count := 0
	stubNotify(t, func(title, body string) error { count++; return nil })
	if _, err := notifyTool(convCtx("console:private:a"), desktopArgs(t, `{"title":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := notifyTool(convCtx("console:private:b"), desktopArgs(t, `{"title":"y"}`)); got != "已发出通知" || count != 2 {
		t.Fatalf("另一个会话应该照常发：%q, 次数 %d", got, count)
	}
}

func TestNotifyFailureDoesNotUseUpLimit(t *testing.T) {
	calls := 0
	stubNotify(t, func(title, body string) error {
		calls++
		if calls == 1 {
			return errors.New("系统拦住了")
		}
		return nil
	})
	ctx := convCtx("console:private:a")
	if _, err := notifyTool(ctx, desktopArgs(t, `{"title":"x"}`)); err == nil {
		t.Fatal("出口报错应该返回错误")
	}
	if got, err := notifyTool(ctx, desktopArgs(t, `{"title":"x"}`)); err != nil || got != "已发出通知" {
		t.Fatalf("失败以后应该马上能再发：%q, %v", got, err)
	}
}

func TestNotifyWithoutExitErrors(t *testing.T) {
	stubNotify(t, nil)
	if _, err := notifyTool(convCtx("console:private:a"), desktopArgs(t, `{"title":"x"}`)); err == nil {
		t.Fatal("没接出口时应该报错，不能悄悄不弹")
	}
}

func TestNotifyNeedsTitle(t *testing.T) {
	stubNotify(t, func(title, body string) error { t.Fatal("没有标题不该发"); return nil })
	if _, err := notifyTool(convCtx("console:private:a"), desktopArgs(t, `{"body":"只有正文"}`)); err == nil {
		t.Fatal("没有 title 应该报错")
	}
}

func TestExecuteDispatchesDesktopTools(t *testing.T) {
	stubClipboard(t, "内容", true, nil)
	res, err := Execute(context.Background(), t.TempDir(), "clipboard", desktopArgs(t, `{"action":"read"}`))
	if err != nil || res.IsError || res.Text != "内容" {
		t.Fatalf("clipboard 没有分派到：%+v, %v", res, err)
	}
}

func TestDesktopToolsDeclared(t *testing.T) {
	want := map[string]bool{"clipboard": false, "open": false, "notify": false}
	for _, d := range OwnerConsole() {
		if _, ok := want[d.Name]; ok {
			want[d.Name] = true
			if _, err := jsjson.Parse(d.Parameters); err != nil {
				t.Errorf("%s 的参数 JSON 解析不了：%v", d.Name, err)
			}
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("OwnerConsole 里没有 %s", name)
		}
		if !Builtin(name) {
			t.Errorf("%s 应该是内置工具", name)
		}
	}
}
