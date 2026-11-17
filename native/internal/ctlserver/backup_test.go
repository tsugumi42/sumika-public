package ctlserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

type backupFake struct {
	fakeBackend
	nowCalls     []string
	settled      bool
	restoreCalls []string
	verifyCalls  []string
	restored     ctlproto.BackupRestored
	restoreErr   error
	verified     ctlproto.BackupVerified
}

func (f *backupFake) BackupNow(_ context.Context, media string, wait time.Duration) (ctlproto.BackupNow, error) {
	f.rec("backup-now")
	f.nowCalls = append(f.nowCalls, media+"|"+wait.String())
	return ctlproto.BackupNow{Settled: f.settled, Name: "手动-2026\x1b[31m", Status: "ok", Files: 3, Linked: 1, Bytes: 10, Problems: []string{"坏\n了"}}, nil
}

func (f *backupFake) BackupList(context.Context) (ctlproto.BackupList, error) {
	f.rec("backup-list")
	return ctlproto.BackupList{Overview: ctlproto.BackupOverview{Dir: "D:\\备份\x07", Total: 1},
		Rows: []ctlproto.BackupRow{{Name: "每天\n-1", Reason: "daily", Status: "ok"}}}, nil
}

func TestBackupListIsReadOnlyAndCleansText(t *testing.T) {
	f := &backupFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"list"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.BackupList](t, r)
	if strings.ContainsAny(v.Rows[0].Name+v.Overview.Dir, "\n\x07") {
		t.Fatalf("控制字符没清掉：%+v", v)
	}
	if len(logs) != 0 {
		t.Fatalf("list 只读，不该写改状态的日志：%v", logs)
	}
}

func TestBackupNowDefaultsAndForwardsWait(t *testing.T) {
	f := &backupFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"now"}})
	if !r.OK || f.nowCalls[0] != "|1m0s" {
		t.Fatalf("不写 --wait 默认等 60 秒、--media 空用配置：%+v %v", r, f.nowCalls)
	}
	v := dataOf[ctlproto.BackupNow](t, r)
	if v.Settled || strings.ContainsAny(v.Name+v.Problems[0], "\x1b\n") {
		t.Fatalf("%+v", v)
	}
	r = ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"now"}, Wait: "5s", Backup: &ctlproto.BackupArgs{Media: "all"}})
	if !r.OK || f.nowCalls[1] != "all|5s" {
		t.Fatalf("%+v %v", r, f.nowCalls)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "控制接口") {
		t.Fatalf("backup now 会写备份目录，该记日志：%v", logs)
	}
}

// 到点还没做完时 Settled 为假，由客户端告诉人“已开始，用 backup list 看”
func TestBackupNowWaitsThenReportsRunning(t *testing.T) {
	f := &backupFake{settled: false}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"now"}, Wait: "1s"})
	if v := dataOf[ctlproto.BackupNow](t, r); !r.OK || v.Settled {
		t.Fatalf("%+v", r)
	}
}

func TestBackupRejectsBadShapes(t *testing.T) {
	f := &backupFake{}
	name, _ := start(t, f, Options{})
	bad := []ctlproto.Request{
		{Cmd: "backup"},
		{Cmd: "backup", Args: []string{"restore"}},
		{Cmd: "backup", Args: []string{"now", "extra"}},
		{Cmd: "backup", Args: []string{"list", "extra"}},
		{Cmd: "backup", Args: []string{"now"}, Backup: &ctlproto.BackupArgs{Media: "everything"}},
		{Cmd: "backup", Args: []string{"now"}, Backup: &ctlproto.BackupArgs{Media: " "}},
		{Cmd: "backup", Args: []string{"list"}, Backup: &ctlproto.BackupArgs{Media: "all"}},
		{Cmd: "backup", Args: []string{"list"}, Wait: "5s"},
		{Cmd: "backup", Args: []string{"now"}, Wait: "-5s"},
		{Cmd: "backup", Args: []string{"now"}, Wait: "99h"},
		{Cmd: "status", Backup: &ctlproto.BackupArgs{Media: "all"}},
		{Cmd: "status", Wait: "5s"},
	}
	for i, req := range bad {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("第 %d 条该被拒（invalid）：%+v", i, r)
		}
	}
	if len(f.got()) != 0 {
		t.Fatalf("校验不过的不许调后端：%v", f.got())
	}
}

func TestBackupReadOnlyTable(t *testing.T) {
	if !readOnly(ctlproto.Request{Cmd: "backup", Args: []string{"list"}}) {
		t.Fatal("backup list 是只读")
	}
	if readOnly(ctlproto.Request{Cmd: "backup", Args: []string{"now"}}) {
		t.Fatal("backup now 会写备份目录，不是只读")
	}
	if got := describe(ctlproto.Request{Cmd: "backup", Args: []string{"now", "--media=假的"}}); got != "backup now" {
		t.Fatalf("日志里只写固定的词：%q", got)
	}
}

// ───────────── backup restore / verify（规格-数据目录与备份 10.10.5） ─────────────

func (f *backupFake) BackupRestore(_ context.Context, name, to string, wait time.Duration) (ctlproto.BackupRestored, error) {
	f.rec("backup-restore")
	f.restoreCalls = append(f.restoreCalls, name+"|"+to+"|"+wait.String())
	return f.restored, f.restoreErr
}

func (f *backupFake) BackupVerify(_ context.Context, name string, wait time.Duration) (ctlproto.BackupVerified, error) {
	f.rec("backup-verify")
	f.verifyCalls = append(f.verifyCalls, name+"|"+wait.String())
	return f.verified, nil
}

func TestBackupRestoreArgs(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "恢复出的目录")
	f := &backupFake{restored: ctlproto.BackupRestored{Settled: true, OK: true, Name: "手动-1\x1b[31m", Target: abs, Files: 3}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"restore", "手动-1"}, Backup: &ctlproto.BackupArgs{To: abs}})
	if !r.OK || len(f.restoreCalls) != 1 || f.restoreCalls[0] != "手动-1|"+abs+"|1m0s" {
		t.Fatalf("不写 --wait 默认等 60 秒：%+v %v", r, f.restoreCalls)
	}
	if v := dataOf[ctlproto.BackupRestored](t, r); strings.ContainsAny(v.Name, "\x1b") {
		t.Fatalf("控制字符没清掉：%+v", v)
	}
	r = ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"restore", "手动-1"}, Wait: "5s", Backup: &ctlproto.BackupArgs{To: abs}})
	if !r.OK || f.restoreCalls[1] != "手动-1|"+abs+"|5s" {
		t.Fatalf("%+v %v", r, f.restoreCalls)
	}

	for i, req := range []ctlproto.Request{
		{Cmd: "backup", Args: []string{"restore"}, Backup: &ctlproto.BackupArgs{To: abs}},                            // 没写备份名
		{Cmd: "backup", Args: []string{"restore", ""}, Backup: &ctlproto.BackupArgs{To: abs}},                        // 空备份名
		{Cmd: "backup", Args: []string{"restore", "a", "b"}, Backup: &ctlproto.BackupArgs{To: abs}},                  // 多余的参数
		{Cmd: "backup", Args: []string{"restore", "a"}},                                                              // 没写 --to
		{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: " "}},                       // 空白 --to（命令行把空值变成这个）
		{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: filepath.Join("相对", "目录")}}, // 相对路径
		{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: abs, Media: "all"}},         // --media 只用于 now
		{Cmd: "backup", Args: []string{"restore", "a"}, Wait: "-5s", Backup: &ctlproto.BackupArgs{To: abs}},          // 坏的 --wait
		{Cmd: "backup", Args: []string{"now"}, Backup: &ctlproto.BackupArgs{To: abs}},                                // --to 只用于 restore
		{Cmd: "backup", Args: []string{"list"}, Backup: &ctlproto.BackupArgs{To: abs}},                               // 同上
		{Cmd: "backup", Args: []string{"verify"}, Backup: &ctlproto.BackupArgs{To: abs}},                             // verify 没有 --to：目录它自己定
		{Cmd: "backup", Args: []string{"verify", "a", "b"}},                                                          // 多余的参数
		{Cmd: "backup", Args: []string{"verify", ""}},                                                                // 空备份名
		{Cmd: "backup", Args: []string{"verify"}, Backup: &ctlproto.BackupArgs{Media: "all"}},                        // --media 只用于 now
		{Cmd: "backup", Args: []string{"verify"}, Wait: "99h"},                                                       // 坏的 --wait
	} {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("第 %d 条该被拒（invalid）：%+v", i, r)
		}
	}
	if len(f.restoreCalls) != 2 || len(f.verifyCalls) != 0 {
		t.Fatalf("校验不过的不许调后端：%v %v", f.restoreCalls, f.verifyCalls)
	}
}

func TestBackupVerifyForwardsNameAndWait(t *testing.T) {
	f := &backupFake{verified: ctlproto.BackupVerified{Settled: true, OK: true, Name: "每天-1", Files: 5}}
	name, _ := start(t, f, Options{})
	if r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"verify"}}); !r.OK || f.verifyCalls[0] != "|1m0s" {
		t.Fatalf("不写名字、不写 --wait：%+v %v", r, f.verifyCalls)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"verify", "每天-1"}, Wait: "3s"}); !r.OK || f.verifyCalls[1] != "每天-1|3s" {
		t.Fatalf("%+v %v", r, f.verifyCalls)
	}
}

// 核对没通过：回复是失败（退出码 1），当时的核对结果随回复一起给出，文字里的控制字符清掉
func TestBackupRestoreFailureCarriesData(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x")
	f := &backupFake{restored: ctlproto.BackupRestored{Settled: true, OK: false, Target: abs, Problems: []string{"哈希\n不对"},
		DBs: []ctlproto.BackupDBCheck{{Path: "go/chat/qq.db\x07", Integrity: "ok", Rows: "mismatch", Mismatch: []string{"表 a\x1b：清单 1 行"}}}}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: abs}})
	if r.OK || r.Code != ctlproto.CodeFailed || ctlproto.ExitCode(r) != ctlproto.ExitFailed {
		t.Fatalf("核对没通过该是失败（退出码 1）：%+v", r)
	}
	v := dataOf[ctlproto.BackupRestored](t, r)
	if len(v.DBs) != 1 || v.DBs[0].Rows != "mismatch" || strings.ContainsAny(v.Problems[0]+v.DBs[0].Path+v.DBs[0].Mismatch[0]+r.Error, "\n\x07\x1b") {
		t.Fatalf("失败的回复里该带着核对结果、控制字符清掉：%+v %q", v, r.Error)
	}
	// 没做完（Settled 为假）不是失败
	f.restored = ctlproto.BackupRestored{Settled: false, Target: abs}
	if r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: abs}}); !r.OK {
		t.Fatalf("到点没做完回“已开始”，不是失败：%+v", r)
	}
	// 后端开工前就拒绝：错误码照原样
	f.restoreErr = ctlproto.Errorf(ctlproto.CodeInvalid, "恢复目标不合适")
	if r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: abs}}); r.OK || r.Code != ctlproto.CodeInvalid {
		t.Fatalf("%+v", r)
	}
	// verify 同理
	f.verified = ctlproto.BackupVerified{Settled: true, OK: false, Problems: []string{"x"}}
	if r := ask(t, name, ctlproto.Request{Cmd: "backup", Args: []string{"verify"}}); r.OK || r.Code != ctlproto.CodeFailed || len(r.Data) == 0 {
		t.Fatalf("%+v", r)
	}
}

// 演示数据模式一律不做
func TestBackupRestoreDemoRefused(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x")
	f := &backupFake{}
	name, _ := start(t, f, Options{Demo: true})
	for _, req := range []ctlproto.Request{
		{Cmd: "backup", Args: []string{"restore", "a"}, Backup: &ctlproto.BackupArgs{To: abs}},
		{Cmd: "backup", Args: []string{"verify"}},
	} {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
			t.Errorf("演示模式该拒绝：%+v", r)
		}
	}
	if len(f.restoreCalls)+len(f.verifyCalls) != 0 {
		t.Fatal("演示模式不许调后端")
	}
}

// restore、verify 不是只读：服务端的 readOnly 表和策略名单对拍（policy_parity_test.go）也守着这一点
func TestBackupRestoreVerifyAreNotReadOnly(t *testing.T) {
	for _, sub := range []string{"restore", "verify"} {
		if readOnly(ctlproto.Request{Cmd: "backup", Args: []string{sub}}) || readOnly(ctlproto.Request{Cmd: "backup", Args: []string{sub, "x"}}) {
			t.Errorf("backup %s 会写盘，不是只读", sub)
		}
	}
}

// 日志里只写固定的词：目标目录、备份名是请求里给的文字，不原样写进日志
func TestBackupRestoreVerifyDescribeIsFixedWords(t *testing.T) {
	r := ctlproto.Request{Cmd: "backup", Args: []string{"restore", "名字\n伪造一行日志"}, Backup: &ctlproto.BackupArgs{To: `D:\秘密\目标`}}
	if got := describe(r); got != "backup restore" {
		t.Fatalf("%q", got)
	}
	if got := describe(ctlproto.Request{Cmd: "backup", Args: []string{"verify", "x"}}); got != "backup verify" {
		t.Fatalf("%q", got)
	}
}
