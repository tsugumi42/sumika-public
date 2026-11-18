package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func resultFixture(t *testing.T) (*Store, kernel.Invocation) {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	inv := kernel.Invocation{Scope: kernel.Scope{CharacterID: "made-a", Conversation: "console:made-a", ScopeID: "made-scope", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "made-owner", AuthorityID: "made-grant", AuthorityRevision: 1}, RootID: "made-root", RunID: "made-run", StepID: "made-step", ToolID: "made-tool", Kind: "foreground"}
	return New(db, p.WorkResultsDir(), config.DefaultWork()), inv
}
func resultStep(t *testing.T, s *Store, inv kernel.Invocation, name string) {
	t.Helper()
	if err := s.DB.StepStart(store.Step{ID: inv.ToolID, CharacterID: inv.Scope.CharacterID, Conversation: inv.Scope.Conversation, TurnID: inv.RunID, Kind: "tool", Name: name, Status: "running", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
}
func putText(t *testing.T, s *Store, inv kernel.Invocation, text string) kernel.ResultRef {
	t.Helper()
	ref, err := s.Put(context.Background(), inv, "text/plain; charset=utf-8", strings.NewReader(text), true)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func TestResultReadOwnershipAndUTF8(t *testing.T) {
	s, base := resultFixture(t)
	ctx := context.Background()
	text := strings.Repeat("中😀\r\nalpha\n", 73)
	for i, scene := range []struct{ platform, channel, scene, conversation string }{{"local", "console", "private", "console:made-a"}, {"local", "console", "group", "console:group:made-g"}, {"qq", "qq", "private", "qq:private:made-bot:made-user"}, {"qq", "qq", "group", "qq:group:made-bot:made-group"}} {
		t.Run(scene.channel+"-"+scene.scene, func(t *testing.T) {
			inv := base
			inv.Scope.Platform, inv.Scope.Channel, inv.Scope.Scene, inv.Scope.Conversation = scene.platform, scene.channel, scene.scene, scene.conversation
			inv.RootID = "made-shared-root"
			inv.ToolID = fmt.Sprint("tool-", i)
			resultStep(t, s, inv, "memory_search")
			ref := putText(t, s, inv, text)
			var got strings.Builder
			for offset := int64(0); ; {
				page, err := s.Read(ctx, inv.Scope, ref.ID, offset, 525)
				if err != nil {
					t.Fatal(err)
				}
				got.WriteString(page.Text)
				if page.EOF {
					break
				}
				if page.Next <= offset {
					t.Fatal("分页未前进")
				}
				offset = page.Next
			}
			if got.String() != text {
				t.Fatal("分页未能还原 UTF-8/CRLF 原文")
			}
			hash := sha256.Sum256([]byte(text))
			if ref.SHA256 != hex.EncodeToString(hash[:]) || ref.Bytes != int64(len(text)) || !ref.Complete {
				t.Fatalf("错误原文引用 %+v", ref)
			}
			for _, change := range []func(*kernel.Scope){func(x *kernel.Scope) { x.CharacterID = "made-other" }, func(x *kernel.Scope) { x.Conversation = "made-other" }, func(x *kernel.Scope) { x.ScopeID = "made-other" }, func(x *kernel.Scope) { x.AccountID = "made-other" }, func(x *kernel.Scope) { x.AuthorityID = "made-other" }, func(x *kernel.Scope) { x.AuthorityRevision++ }, func(x *kernel.Scope) { x.ActorID = "主人\u200b" }, func(x *kernel.Scope) { x.AuthorityID = "" }, func(x *kernel.Scope) { x.CharacterID = "0" }} {
				owner := inv.Scope
				change(&owner)
				if _, err := s.Read(ctx, owner, ref.ID, 0, 525); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("越权未拒绝：%+v %v", owner, err)
				}
			}
			for _, id := range []string{"", "../secret", "file:secret", "foo:stream", strings.Repeat("f", 32)} {
				if _, err := s.Read(ctx, inv.Scope, id, 0, 525); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("恶意 ID %q：%v", id, err)
				}
			}
			for _, v := range []struct{ offset, limit int64 }{{-1, 525}, {1, 525}, {int64(len(text)) + 1, 525}, {0, 0}, {0, 513}, {0, 65537}} {
				if _, err := s.Read(ctx, inv.Scope, ref.ID, v.offset, v.limit); err == nil {
					t.Fatalf("坏分页被接纳：%+v", v)
				}
			}
			eof, err := s.Read(ctx, inv.Scope, ref.ID, int64(len(text)), 525)
			if err != nil || !eof.EOF || eof.Text != "" {
				t.Fatalf("EOF：%+v %v", eof, err)
			}
			inv.ToolID += "-local"
			resultStep(t, s, inv, "read")
			local := putText(t, s, inv, "本机工具结果")
			_, err = s.Read(ctx, inv.Scope, local.ID, 0, 1024)
			want := scene.channel == "console" && scene.scene == "private"
			if (err == nil) != want {
				t.Fatalf("原工具场合放宽：want=%t err=%v", want, err)
			}
		})
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestArtifactCommitFailures(t *testing.T) {
	for _, fault := range []string{"create", "write", "close", "rename", "db", "reader"} {
		t.Run(fault, func(t *testing.T) {
			s, inv := resultFixture(t)
			resultStep(t, s, inv, "read")
			switch fault {
			case "create":
				s.create = func(string, string) (*os.File, error) { return nil, os.ErrPermission }
			case "write":
				s.writeFile = func(*os.File, []byte) (int, error) { return 0, io.ErrShortWrite }
			case "close":
				s.closeFile = func(f *os.File) error { return errors.Join(f.Close(), os.ErrPermission) }
			case "rename":
				s.rename = func(string, string) error { return os.ErrPermission }
			case "db":
				if _, err := s.DB.Trace.Exec(`CREATE TRIGGER fail_result BEFORE INSERT ON result_refs BEGIN SELECT RAISE(ABORT,'made DB failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			var r io.Reader = strings.NewReader("执行过的证据")
			if fault == "reader" {
				r = brokenReader{}
			}
			ref, err := s.Put(context.Background(), inv, "text/plain", r, true)
			if err == nil || ref.ID != "" {
				t.Fatalf("发布了假有效引用 %+v %v", ref, err)
			}
			var n int
			if err := s.DB.Trace.QueryRow(`SELECT count(*) FROM result_refs`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("失败索引：%d %v", n, err)
			}
		})
	}
	s, inv := resultFixture(t)
	resultStep(t, s, inv, "read")
	ref := putText(t, s, inv, "文件会丢失")
	if err := os.Remove(filepath.Join(s.Dir, ref.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), inv.Scope, ref.ID, 0, 1024); err == nil {
		t.Fatal("真实不存在文件未报错")
	}
	inv.ToolID = "corrupt"
	resultStep(t, s, inv, "read")
	ref = putText(t, s, inv, "原文")
	if err := os.WriteFile(filepath.Join(s.Dir, ref.ID), []byte("伪造"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), inv.Scope, ref.ID, 0, 1024); err == nil {
		t.Fatal("损坏内容未报错")
	}
}
func TestResultLimitAndRetention(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{7, 8, 9} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, inv := resultFixture(t)
			s.Limits.ResultMaxBytes = 8
			s.Limits.RootMaxBytes = 16
			resultStep(t, s, inv, "read")
			ref := putText(t, s, inv, strings.Repeat("x", n))
			if ref.Bytes != int64(min(n, 8)) || ref.Complete != (n < 8) {
				t.Fatalf("硬限状态：%+v", ref)
			}
		})
	}
	s, inv := resultFixture(t)
	s.Limits.ResultMaxBytes = 8
	s.Limits.RootMaxBytes = 8
	resultStep(t, s, inv, "read")
	ref := putText(t, s, inv, "12345😀")
	page, err := s.Read(ctx, inv.Scope, ref.ID, 0, 1024)
	if err != nil || page.Text != "12345" || ref.Complete {
		t.Fatalf("硬限 UTF-8：%+v %v", page, err)
	}
	inv.ToolID = "second"
	resultStep(t, s, inv, "read")
	if _, err := s.Put(ctx, inv, "text/plain", strings.NewReader("a"), true); err == nil {
		t.Fatal("根硬限未拒绝新采集")
	}
	s.Now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ref.ID)); err != nil {
		t.Fatal("在用证据被删", err)
	}
	if err := s.DB.ReleaseWorkResults(ctx, inv.Scope, inv.RootID); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ref.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("过期无引用文件没删 %v", err)
	}
	orphan := filepath.Join(s.Dir, strings.Repeat("e", 32))
	if err := os.WriteFile(orphan, []byte("孤儿"), 0600); err != nil {
		t.Fatal(err)
	}
	old := s.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("孤儿没收回")
	}
	s2, inv2 := resultFixture(t)
	resultStep(t, s2, inv2, "read")
	cancelled, cancel := context.WithCancel(ctx)
	capture, err := s2.Begin(cancelled, inv2, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	capture.Write([]byte("半份"))
	cancel()
	got, err := capture.Finish(cancelled, "text/plain", true)
	if err == nil || got.ID != "" {
		t.Fatal("取消后发布了完整引用")
	}
	crashed, inv3 := resultFixture(t)
	crashed.Limits.ResultMaxBytes = 8
	crashed.Limits.RootMaxBytes = 8
	resultStep(t, crashed, inv3, "read")
	lost, err := crashed.Begin(ctx, inv3, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lost.Write([]byte("lost")); err != nil {
		t.Fatal(err)
	}
	if err := lost.file.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := New(crashed.DB, crashed.Dir, crashed.Limits)
	restarted.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if err := restarted.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	inv3.ToolID = "after-crash"
	resultStep(t, restarted, inv3, "read")
	if ref := putText(t, restarted, inv3, "ok"); !ref.Complete {
		t.Fatal("崩溃遗留预留未恢复容量")
	}
}
