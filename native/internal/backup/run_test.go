package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/sqlitesnap"
)

func fileOf(m Manifest, rel string) (FileEntry, bool) {
	for _, f := range m.Files {
		if f.Path == rel {
			return f, true
		}
	}
	return FileEntry{}, false
}

func TestRunBasic(t *testing.T) {
	f := newFx(t)
	const snapshotRel = "go/upgrade-snapshots/go-chat-qq.db-chat-v16-to-v17-20261001-053000.sqlite"
	write(t, filepath.Join(f.p.Root, filepath.FromSlash(snapshotRel)), "升级前快照")
	res := f.mustRun(ReasonDaily)
	if res.Status != StatusOK || !strings.HasPrefix(res.Name, "每天-20261001-0530") {
		t.Fatalf("结果不对：%+v", res)
	}
	m, err := ReadManifest(res.Folder)
	if err != nil {
		t.Fatal(err)
	}
	if m.Program != Program || m.Format != FormatVersion || m.Version != "test" || m.Reason != ReasonDaily || m.Status != StatusOK || m.Media != MediaStickers {
		t.Fatalf("清单头不对：%+v", m)
	}
	// 带的
	for _, rel := range []string{"sumika-data.json", "config/sumika.yml", "config/mcp.json", "characters/a/card/character.md", "characters/a/drafts/d.md",
		"go/reminders.json", "dev/replay/r.json", "dev/seeds/s.json", "reports/r.md", "go/chat/qq.db", "go/trace.db", "go/characters/a/memory.db", "shared/events.db"} {
		fe, ok := fileOf(m, rel)
		if !ok {
			t.Errorf("清单里没有 %s", rel)
			continue
		}
		size, sum, err := sha256File(filepath.Join(res.Folder, "data", filepath.FromSlash(rel)))
		if err != nil || size != fe.Size || sum != fe.SHA256 {
			t.Errorf("%s 的副本和清单对不上：%v %d/%d %s/%s", rel, err, size, fe.Size, sum, fe.SHA256)
		}
	}
	// 不带的
	for _, rel := range []string{"cache/c.bin", "logs/l.log", "run/rpc-go.json", "dev/screens/s.png", "go/media-thumbs/qq/ab/x.png",
		"go/characters/a/memory-v2.db", "go/local.lock", "go/chat/zzz.db-wal", "backups/old.db", "go/media/qq/.tmp/put-1",
		"dev/test-runs/full-output.txt", "dev/handoff/分支.md", snapshotRel} {
		if _, ok := fileOf(m, rel); ok {
			t.Errorf("%s 不该进备份", rel)
		}
		if exists(filepath.Join(res.Folder, "data", filepath.FromSlash(rel))) {
			t.Errorf("%s 的副本不该存在", rel)
		}
	}
	// 类
	if fe, _ := fileOf(m, "dev/replay/r.json"); fe.Class != ClassMaterial {
		t.Errorf("回放样本应该是素材：%v", fe.Class)
	}
	if fe, _ := fileOf(m, "characters/a/drafts/d.md"); fe.Class != ClassMaterial {
		t.Errorf("草稿应该是素材：%v", fe.Class)
	}
	if fe, _ := fileOf(m, "config/mcp.json"); fe.Class != ClassSelf {
		t.Errorf("配置应该是她本人：%v", fe.Class)
	}
	if len(m.Classes) != 3 {
		t.Errorf("该有三类：%v", m.Classes)
	}
	// 库：登记的和兜底的、行数、校验
	byPath := map[string]DBEntry{}
	for _, d := range m.DBs {
		byPath[d.Path] = d
		if d.Integrity != "ok" {
			t.Errorf("%s 校验结果 %q", d.Path, d.Integrity)
		}
	}
	if d := byPath["go/chat/qq.db"]; d.Method != MethodHolder || d.Tables["events"] != 3 || d.Tables["qq_avatars"] != 1 {
		t.Errorf("聊天库条目不对：%+v", d)
	}
	if d := byPath["go/trace.db"]; d.Method != MethodHolder || d.Tables["turns"] != 2 {
		t.Errorf("trace 条目不对：%+v", d)
	}
	if d := byPath["go/characters/a/memory.db"]; d.Method != MethodFallback || d.Tables["facts"] != 3 {
		t.Errorf("记忆库应该走兜底：%+v", d)
	}
	if len(m.DBs) != 4 {
		t.Errorf("该有 4 个库：%+v", m.DBs)
	}
	// 副本旁边没有 -wal、-shm
	filepath.WalkDir(res.Folder, func(p string, d os.DirEntry, err error) error {
		if err == nil && (strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm")) {
			t.Errorf("备份里不该有 %s", p)
		}
		return nil
	})
	// 不是 .partial 了，也没有临时清单
	if exists(res.Folder+".partial") || exists(filepath.Join(res.Folder, ManifestFile+".tmp")) {
		t.Error("不该留下 .partial 或临时清单")
	}
	if got := f.dir(); filepath.Dir(res.Folder) != got {
		t.Errorf("备份该在 %s，实际 %s", got, res.Folder)
	}
}

func TestRunSameMinuteGetsSuffix(t *testing.T) {
	f := newFx(t)
	// 手动的不会被轮换删掉，所以同一分钟连做三份，名字都还在占着
	a := f.mustRun(ReasonManual)
	b := f.mustRun(ReasonManual)
	c := f.mustRun(ReasonManual)
	d := f.mustRun(ReasonDaily)
	if filepath.Base(a.Folder) != "手动-20261001-0530" || filepath.Base(b.Folder) != "手动-20261001-0530-2" || filepath.Base(c.Folder) != "手动-20261001-0530-3" {
		t.Fatalf("重名该加后缀：%s %s %s", a.Name, b.Name, c.Name)
	}
	if filepath.Base(d.Folder) != "每天-20261001-0530" {
		t.Fatalf("前缀不同不用加后缀：%s", d.Name)
	}
}

func TestRunPreChangeNeedsLabel(t *testing.T) {
	f := newFx(t)
	if _, err := f.svc.Run(context.Background(), Request{Reason: ReasonPreChange}); err == nil {
		t.Fatal("改动前备份没有标签应该拒绝")
	}
	if _, err := f.svc.Run(context.Background(), Request{Reason: ReasonPreChange, Label: "../x"}); err == nil {
		t.Fatal("标签里有路径字符应该拒绝")
	}
	res, err := f.svc.Run(context.Background(), Request{Reason: ReasonPreChange, Label: "重判-1"})
	if err != nil || !strings.HasPrefix(res.Name, "改动前-重判-1-2026") {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestRunFailureLeavesNoOKFolder(t *testing.T) {
	f := newFx(t)
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		return errors.New("模拟：磁盘满了")
	}
	res, err := f.run(ReasonDaily)
	if err == nil || res.Status != StatusBad {
		t.Fatalf("应该失败并标 bad：%v %+v", err, res)
	}
	entries, _ := Scan(f.dir())
	for _, e := range entries {
		if e.Manifest.Status == StatusOK {
			t.Fatalf("失败的备份不能是好的：%+v", e)
		}
	}
	if st := f.svc.Status(); st.HaveGood || st.LastFailure == "" {
		t.Fatalf("状态该记着失败、没有好的备份：%+v", st)
	}
	// 之后恢复正常就能做出好的
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { return sqlitesnap.Into(ctx, db, dst) }
	f.clock.Add(time.Minute)
	if res := f.mustRun(ReasonDaily); res.Status != StatusOK {
		t.Fatal("恢复后应该能做出好的")
	}
	if st := f.svc.Status(); !st.HaveGood || st.LastFailure != "" {
		t.Fatalf("成功以后该清掉失败记录：%+v", st)
	}
}

func TestRunDetectsCopyCorruption(t *testing.T) {
	f := newFx(t)
	real := f.svc.copyFile
	f.svc.copyFile = func(ctx context.Context, src, dst string) (int64, string, error) {
		if strings.HasSuffix(src, "r.md") {
			return 0, "", errors.New("写下去以后读回来对不上")
		}
		return real(ctx, src, dst)
	}
	res, err := f.run(ReasonManual)
	if err == nil || res.Status != StatusBad {
		t.Fatalf("复制校验不过应该是 bad：%v %+v", err, res)
	}
}

func TestRunCancelRemovesPartial(t *testing.T) {
	f := newFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.holderSnapshot = func(c context.Context, db *sql.DB, dst string) error {
		cancel() // 关机
		return c.Err()
	}
	_, err := f.svc.Run(ctx, Request{Reason: ReasonDaily})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应该报被取消：%v", err)
	}
	ents, _ := os.ReadDir(f.dir())
	if len(ents) != 0 {
		t.Fatalf("取消以后不该留下任何文件夹：%v", ents)
	}
}

func TestRunRefusesConcurrent(t *testing.T) {
	f := newFx(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		once.Do(func() { close(started) })
		<-release
		return sqlitesnap.Into(ctx, db, dst)
	}
	done := make(chan error, 1)
	go func() { _, err := f.run(ReasonDaily); done <- err }()
	<-started
	if _, err := f.run(ReasonManual); !errors.Is(err, ErrBusy) {
		t.Fatalf("第二份该得到 ErrBusy：%v", err)
	}
	if !f.svc.Status().Running {
		t.Error("状态该是正在做")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.svc.Status().Running {
		t.Error("做完以后状态该是没在做")
	}
}

func TestRunCreatesMissingDir(t *testing.T) {
	f := newFx(t)
	if exists(f.dir()) {
		t.Fatal("前提：备份目录还不存在")
	}
	f.mustRun(ReasonDaily)
	if !exists(f.dir()) {
		t.Fatal("该自己建")
	}
}

func TestRunUnwritableDirFails(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "占着")
	if err := os.WriteFile(blocker, []byte("我是个文件"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFx(t, func(o *Options) { o.Dir = blocker })
	if _, err := f.run(ReasonDaily); err == nil {
		t.Fatal("备份目录那个路径是个文件，应该失败")
	}
	if st := f.svc.Status(); st.LastFailure == "" {
		t.Fatal("每天备份失败该记下原因（自检要读）")
	}
}

// 属于素材（reports/）的路径是链接：没法带，这份不能记 ok，原因点名它；链接目标里的东西没被动
func TestRunSkipsLinksInData(t *testing.T) {
	f := newFx(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "秘密.txt"), "不该被带走")
	makeLink(t, outside, filepath.Join(f.data, "reports", "外链"))
	res, err := f.run(ReasonManual)
	if err == nil || res.Status != StatusBad || !strings.Contains(strings.Join(res.Problems, "；"), "reports/外链") {
		t.Fatalf("素材路径是链接：该记 bad 并点名：%v %+v", err, res)
	}
	if st := f.svc.Status(); st.HaveGood {
		t.Fatal("坏的不算好的")
	}
	if _, err := os.Stat(filepath.Join(outside, "秘密.txt")); err != nil {
		t.Fatal("外面的文件被动了")
	}
}

// 媒体路径是链接：只记一笔、清单记个数，这份仍然 ok；backup list 的行和自检的状态看得到跳过数
func TestRunMediaLinkIsNoteAndCounted(t *testing.T) {
	f := newFx(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "x.png"), "不该被带走")
	makeLink(t, outside, filepath.Join(f.p.GoMediaDir(), "qq", "ff"))
	res := f.mustRun(ReasonDaily)
	m, _ := ReadManifest(res.Folder)
	if m.Status != StatusOK || m.SkippedMedia != 1 {
		t.Fatalf("媒体链接只记一笔：%+v", m)
	}
	found := false
	for _, n := range m.Notes {
		if strings.Contains(n, "go/media/qq/ff") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes 里该点名：%v", m.Notes)
	}
	if st := f.svc.Status(); !st.HaveGood || st.LastSkipped != 1 {
		t.Fatalf("状态里该看得到跳过数（自检读它）：%+v", st)
	}
	if exists(filepath.Join(res.Folder, "data", "go", "media", "qq", "ff")) {
		t.Fatal("链接目录不该被跟进去")
	}
}

// 验收：一边持续往库里写，一边备份，备份出的库 integrity_check 通过、能打开、条数不早于备份开始时
func TestSnapshotWhileWriting(t *testing.T) {
	f := newFx(t)
	mustExec(t, f.chat, `CREATE TABLE IF NOT EXISTS load (n INTEGER)`)
	var n atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := f.chat.Exec(`INSERT INTO load VALUES (1)`); err != nil {
				return
			}
			n.Add(1)
		}
	}()
	for n.Load() < 200 {
		time.Sleep(time.Millisecond)
	}
	startedAt := n.Load() // 备份开始前已经写进去的条数
	res := f.mustRun(ReasonManual)
	close(stop)
	<-done
	m, _ := ReadManifest(res.Folder)
	var chat DBEntry
	for _, d := range m.DBs {
		if d.Path == "go/chat/qq.db" {
			chat = d
		}
	}
	if chat.Integrity != "ok" {
		t.Fatalf("备份出的库没通过检查：%q", chat.Integrity)
	}
	got := chat.Tables["load"]
	if got < startedAt || got > n.Load() {
		t.Fatalf("条数该在 [备份开始前 %d, 备份结束后 %d] 之间，实际 %d", startedAt, n.Load(), got)
	}
	// 清单里的行数就是副本里数出来的
	rep, err := sqlitesnap.Inspect(context.Background(), filepath.Join(res.Folder, "data", "go", "chat", "qq.db"))
	if err != nil || !rep.OK() || rep.Tables["load"] != got {
		t.Fatalf("副本和清单对不上：%+v %v", rep, err)
	}
}

func TestUnregisteredDBFallsBackWithNote(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	found := false
	for _, n := range m.Notes {
		if strings.Contains(n, "go/characters/a/memory.db") && strings.Contains(n, "兜底") {
			found = true
		}
	}
	if !found {
		t.Fatalf("没有登记的库该在清单里记一笔：%v", m.Notes)
	}
}

// 数据目录里有不是 SQLite 的 .db（别的东西改的名、空文件）：看文件头，对不上的当普通文件带走并记一笔；Thumbs.db 之类不带
func TestRunNotADatabaseIsCarriedAsFile(t *testing.T) {
	f := newFx(t)
	write(t, filepath.Join(f.data, "plugins", "x", "junk.db"), "我不是 SQLite，只是名字叫 .db")
	write(t, filepath.Join(f.data, "plugins", "x", "empty.db"), "")
	write(t, filepath.Join(f.data, "plugins", "x", "Thumbs.db"), "windows 缩略图缓存")
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	for _, rel := range []string{"plugins/x/junk.db", "plugins/x/empty.db"} {
		if _, ok := fileOf(m, rel); !ok {
			t.Errorf("%s 该当普通文件带走", rel)
		}
		for _, d := range m.DBs {
			if d.Path == rel {
				t.Errorf("%s 不是库，不该进 dbs", rel)
			}
		}
	}
	if _, ok := fileOf(m, "plugins/x/Thumbs.db"); ok {
		t.Error("Thumbs.db 不该带")
	}
	if !strings.Contains(strings.Join(m.Notes, "\n"), "plugins/x/junk.db：名字是 .db 但文件头不是 SQLite") {
		t.Errorf("该记一笔：%v", m.Notes)
	}
}
