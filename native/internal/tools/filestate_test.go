package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
)

func execIn(t *testing.T, ctx context.Context, wd, tool string, args map[string]any) Result {
	t.Helper()
	b, _ := json.Marshal(args)
	v, err := jsjson.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Execute(ctx, wd, tool, Prepare(tool, v))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func scopeCtx(conv string) context.Context {
	return kernel.WithInvocation(context.Background(), kernel.Invocation{Scope: kernel.Scope{CharacterID: "aa", Conversation: conv}})
}

// 外部改动用 Chtimes 造，不靠 sleep：把修改时间往后拨一秒，大小不变也算变了。
func touchLater(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	later := fi.ModTime().Add(time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRefusesAfterExternalChange(t *testing.T) {
	wd := t.TempDir()
	p := filepath.Join(wd, "a.txt")
	os.WriteFile(p, []byte("原文\n"), 0o644)
	ctx := scopeCtx("c1")
	if r := execIn(t, ctx, wd, "read", map[string]any{"path": "a.txt"}); r.IsError {
		t.Fatal(r.Text)
	}
	os.WriteFile(p, []byte("别人改过的，更长一些\n"), 0o644) // 外部改动：大小变
	r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "我的"})
	if !r.IsError || !strings.Contains(r.Text, "先用 read 重读") {
		t.Fatalf("应该拒绝：%+v", r)
	}
	if b, _ := os.ReadFile(p); string(b) != "别人改过的，更长一些\n" {
		t.Fatalf("拒绝时不该写：%q", b)
	}
	// 重读以后可以写
	execIn(t, ctx, wd, "read", map[string]any{"path": "a.txt"})
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "我的"}); r.IsError {
		t.Fatalf("重读后应能写：%s", r.Text)
	}
}

// 只改了修改时间（大小相同）也算变了。
func TestWriteRefusesWhenOnlyMtimeChanged(t *testing.T) {
	wd := t.TempDir()
	p := filepath.Join(wd, "a.txt")
	os.WriteFile(p, []byte("abc"), 0o644)
	ctx := scopeCtx("c1")
	execIn(t, ctx, wd, "read", map[string]any{"path": "a.txt"})
	touchLater(t, p)
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "x"}); !r.IsError {
		t.Fatal("修改时间变了应该拒绝")
	}
}

// 自己写完再写不算外部改动；edit 之后再 write 也一样。
func TestWriteAfterOwnWriteAndEditAllowed(t *testing.T) {
	wd := t.TempDir()
	ctx := scopeCtx("c1")
	for i, c := range []string{"一", "二二", "三三三"} {
		if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": c}); r.IsError {
			t.Fatalf("第 %d 次：%s", i, r.Text)
		}
	}
	if r := execIn(t, ctx, wd, "edit", map[string]any{"path": "a.txt", "edits": []any{map[string]any{"oldText": "三三三", "newText": "四四四四"}}}); r.IsError {
		t.Fatal(r.Text)
	}
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "最后"}); r.IsError {
		t.Fatalf("edit 之后再 write：%s", r.Text)
	}
}

// 别的会话改了：这个会话的记录对它算外部。
func TestWriteRefusesAfterOtherSessionWrote(t *testing.T) {
	wd := t.TempDir()
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("原文"), 0o644)
	mine, other := scopeCtx("mine"), scopeCtx("other")
	execIn(t, mine, wd, "read", map[string]any{"path": "a.txt"})
	if r := execIn(t, other, wd, "write", map[string]any{"path": "a.txt", "content": "别的会话写的更长的内容"}); r.IsError {
		t.Fatalf("没读过的会话照常写：%s", r.Text)
	}
	if r := execIn(t, mine, wd, "write", map[string]any{"path": "a.txt", "content": "我的"}); !r.IsError {
		t.Fatal("别的会话改过，应该拒绝")
	}
}

func TestWriteNeverReadAndDeletedAllowed(t *testing.T) {
	wd := t.TempDir()
	p := filepath.Join(wd, "a.txt")
	os.WriteFile(p, []byte("原文"), 0o644)
	ctx := scopeCtx("c1")
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "没读过也能写"}); r.IsError {
		t.Fatal(r.Text)
	}
	execIn(t, ctx, wd, "read", map[string]any{"path": "a.txt"})
	os.Remove(p)
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "文件被删后照常创建"}); r.IsError {
		t.Fatal(r.Text)
	}
}

// 没有 Invocation（评测、命令行程序）：空 Scope 也记，行为一致。
func TestWriteCheckWithoutInvocation(t *testing.T) {
	wd := t.TempDir()
	p := filepath.Join(wd, "a.txt")
	os.WriteFile(p, []byte("abc"), 0o644)
	ctx := context.Background()
	execIn(t, ctx, wd, "read", map[string]any{"path": "a.txt"})
	touchLater(t, p)
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "a.txt", "content": "x"}); !r.IsError {
		t.Fatal("空 Scope 也应该拒绝")
	}
}

func TestFileStateCapEvictsOldest(t *testing.T) {
	wd := t.TempDir()
	ctx := scopeCtx("cap")
	first := filepath.Join(wd, "first.txt")
	os.WriteFile(first, []byte("a"), 0o644)
	execIn(t, ctx, wd, "read", map[string]any{"path": "first.txt"})
	fi, _ := os.Stat(first)
	for i := 0; i < fileStateCap; i++ {
		rememberFile(ctx, filepath.Join(wd, "fill", string(rune('a'+i%26)), strings.Repeat("x", i%7), "f"+strconv.Itoa(i)), fi)
	}
	touchLater(t, first)
	// 最早的记录被挤掉：回到“没读过”，不误拒
	if r := execIn(t, ctx, wd, "write", map[string]any{"path": "first.txt", "content": "x"}); r.IsError {
		t.Fatalf("记录被挤掉后不该拒绝：%s", r.Text)
	}
	fileState.Lock()
	n := len(fileState.m)
	fileState.Unlock()
	if n > fileStateCap {
		t.Fatalf("记录条数 %d 超过上限", n)
	}
}
