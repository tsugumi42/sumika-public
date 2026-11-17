package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/paths"
	"sumika-native/internal/sqlitesnap"
)

// 假时钟：测试里推着走，不真等
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func at(day, hour, min int) time.Time { // 2026-10-<day> hour:min（day 可以超过 31，time.Date 会进位）
	return time.Date(2026, 10, day, hour, min, 0, 0, time.Local)
}

const (
	hSticker = "aa11111111111111111111111111111111111111111111111111111111111111"
	hAvatar  = "bb22222222222222222222222222222222222222222222222222222222222222"
	hImage   = "cc33333333333333333333333333333333333333333333333333333333333333"
	hLegacy  = "dd44444444444444444444444444444444444444444444444444444444444444"
)

// fx 是一个假数据目录加一个备份服务
type fx struct {
	t     *testing.T
	base  string // 数据目录和 Sumika-备份 的上一级
	data  string
	p     paths.Paths
	clock *fakeClock
	chat  *sql.DB
	trace *sql.DB
	svc   *Service

	holderSnapshot func(ctx context.Context, db *sql.DB, dst string) error // 测试可换：登记的聊天库快照怎么取
	busy           bool
	opt            Options
}

func write(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openRW(t *testing.T, file string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // 和各库一样的单连接池
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s：%v", q, err)
	}
}

// testSource 是测试里备份归属的数据目录标识（fabricate 造的备份和 fx 的服务都用它）
const testSource = "测试数据目录"

func newFx(t *testing.T, mut ...func(*Options)) *fx {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "data")
	p, err := paths.Init(data)
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{t: t, base: base, data: data, p: p, clock: &fakeClock{t: at(1, 5, 30)}}
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { return sqlitesnap.Into(ctx, db, dst) }

	// 库：聊天库和 trace 有持有方（登记），记忆库和旧版 events.db 没有（兜底）
	f.chat = openRW(t, p.ChatDB("qq"))
	mustExec(t, f.chat, `CREATE TABLE events (seq INTEGER PRIMARY KEY AUTOINCREMENT, segments_json TEXT)`)
	mustExec(t, f.chat, `CREATE TABLE qq_avatars (key TEXT PRIMARY KEY, hash TEXT, fetched_at INTEGER NOT NULL)`)
	mustExec(t, f.chat, `CREATE TABLE blobs (hash TEXT PRIMARY KEY, kind TEXT NOT NULL)`)
	mustExec(t, f.chat, `INSERT INTO events (segments_json) VALUES (?)`, `{"segs":[{"kind":"emoji","hash":"`+hSticker+`"}]}`)
	mustExec(t, f.chat, `INSERT INTO events (segments_json) VALUES (?)`, `{"segs":[{"kind":"image","hash":"`+hImage+`"}]}`)
	mustExec(t, f.chat, `INSERT INTO events (segments_json) VALUES (NULL)`)
	mustExec(t, f.chat, `INSERT INTO qq_avatars VALUES ('u1', ?, 1)`, hAvatar)
	f.trace = openRW(t, p.GoTraceDB())
	mustExec(t, f.trace, `CREATE TABLE turns (id TEXT PRIMARY KEY)`)
	mustExec(t, f.trace, `INSERT INTO turns VALUES ('t1'), ('t2')`)
	mem := openRW(t, p.MemoryDB("a"))
	// 哑表：备份只数行数、不看记忆库结构，不改成 BaseSchema。
	mustExec(t, mem, `CREATE TABLE facts (id INTEGER PRIMARY KEY, text TEXT)`)
	mustExec(t, mem, `INSERT INTO facts (text) VALUES ('f1'), ('f2'), ('f3')`)
	mem.Close() // 记忆库没有登记的持有方
	legacy := openRW(t, p.BunEventsDB())
	mustExec(t, legacy, `CREATE TABLE blobs (hash TEXT PRIMARY KEY, kind TEXT NOT NULL)`)
	mustExec(t, legacy, `INSERT INTO blobs VALUES (?, 'sticker')`, hLegacy)
	legacy.Close()

	// 文件
	write(t, p.ConfigFile(), "qq:\n  accounts:\n    - id: \"10001\"\n      token: 令牌样本甲\n      url: ws://127.0.0.1:3001\n    - id: \"10002\"\n      token: ${QQ_TOKEN}\n")
	write(t, filepath.Join(p.ConfigDir(), "mcp.json"), `{"mcpServers":{"s":{"command":"x","env":{"API_KEY":"示例密钥甲","MODE":"fast"}}}}`)
	write(t, filepath.Join(p.CardDir("a"), "character.md"), "她的卡")
	write(t, filepath.Join(p.CharacterDir("a"), "drafts", "d.md"), "草稿")
	write(t, p.RemindersFile(), `[]`)
	write(t, filepath.Join(p.DevDir(), "replay", "r.json"), `{}`)
	write(t, filepath.Join(p.ReportsDir(), "r.md"), "报告")
	// 不带的
	write(t, filepath.Join(p.CacheDir(), "c.bin"), "cache")
	write(t, filepath.Join(p.LogsDir(), "l.log"), "log")
	write(t, p.RPCGoRunFile(), `{"token":"运行时样本"}`)
	write(t, filepath.Join(p.DevDir(), "screens", "s.png"), "png")
	write(t, filepath.Join(p.DevDir(), "test-runs", "full-output.txt"), "测试完整输出")
	write(t, filepath.Join(p.DevDir(), "handoff", "分支.md"), "交接")
	write(t, filepath.Join(p.DevDir(), "seeds", "s.json"), "{}")
	write(t, filepath.Join(p.GoThumbsDir(), "qq", "ab", "x.png"), "thumb")
	write(t, filepath.Join(p.GoCharactersDir(), "a", "memory-v2.db"), "派生索引")
	write(t, filepath.Join(p.Root, "go", "local.lock"), "")
	write(t, p.ChatDB("zzz")+"-wal", "junk")
	write(t, filepath.Join(p.BackupsDir(), "old.db"), "旧版数据目录里的备份")
	// 媒体
	write(t, filepath.Join(p.GoMediaDir(), "qq", hSticker[:2], hSticker), "sticker-bytes")
	write(t, filepath.Join(p.GoMediaDir(), "qq", hAvatar[:2], hAvatar), "avatar-bytes")
	write(t, filepath.Join(p.GoMediaDir(), "qq", hImage[:2], hImage), "image-bytes")
	write(t, filepath.Join(p.GoMediaDir(), "qq", ".tmp", "put-1"), "half")
	write(t, filepath.Join(p.MediaDir(), hLegacy[:2], hLegacy), "legacy-sticker")
	write(t, filepath.Join(p.MediaDir(), "ee", "ee55555555555555555555555555555555555555555555555555555555555555"), "legacy-other")

	f.opt = Options{Paths: p, Source: testSource, TempRoots: []string{}, MinFreeBytes: 1, Media: MediaStickers, Daily: true, Version: "test", Now: f.clock.Now,
		Busy: func() bool { return f.busy }, StartDelay: -1, Every: time.Hour}
	for _, m := range mut {
		m(&f.opt)
	}
	f.svc = New(f.opt)
	f.svc.Register(func() Contribution {
		rel := func(file string) string {
			r, _ := filepath.Rel(p.Root, file)
			return filepath.ToSlash(r)
		}
		return Contribution{
			DBs: []DBSource{
				{Rel: rel(p.ChatDB("qq")), Snapshot: func(ctx context.Context, dst string) error { return f.holderSnapshot(ctx, f.chat, dst) }},
				{Rel: rel(p.GoTraceDB()), Snapshot: func(ctx context.Context, dst string) error { return f.holderSnapshot(ctx, f.trace, dst) }},
			},
			Stickers: []StickerSource{
				{MediaRel: "go/media/qq", DBRel: rel(p.ChatDB("qq")), Hashes: testHashes},
				{MediaRel: "shared/media", DBRel: "shared/events.db", Hashes: testHashes},
			},
		}
	})
	return f
}

// testHashes：表情包和头像的哈希（真正的取法在 mediastore.StickerHashes；这里只要够测备份包自己）
func testHashes(ctx context.Context, db *sql.DB) ([]string, error) {
	var out []string
	rows, err := db.QueryContext(ctx, `SELECT hash FROM blobs WHERE kind IN ('sticker','avatar')`)
	if err == nil {
		for rows.Next() {
			var h string
			_ = rows.Scan(&h)
			out = append(out, h)
		}
		rows.Close()
	}
	if rows, err := db.QueryContext(ctx, `SELECT hash FROM qq_avatars WHERE hash IS NOT NULL`); err == nil {
		for rows.Next() {
			var h string
			_ = rows.Scan(&h)
			out = append(out, h)
		}
		rows.Close()
	}
	if rows, err := db.QueryContext(ctx, `SELECT segments_json FROM events WHERE segments_json LIKE '%"emoji"%'`); err == nil {
		for rows.Next() {
			var raw string
			_ = rows.Scan(&raw)
			var env struct {
				Segs []struct{ Kind, Hash string } `json:"segs"`
			}
			if json.Unmarshal([]byte(raw), &env) == nil {
				for _, s := range env.Segs {
					if s.Kind == "emoji" {
						out = append(out, s.Hash)
					}
				}
			}
		}
		rows.Close()
	}
	return out, nil
}

func (f *fx) dir() string {
	d, err := f.svc.Dir()
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *fx) run(reason Reason) (Result, error) {
	return f.svc.Run(context.Background(), Request{Reason: reason})
}

func (f *fx) mustRun(reason Reason) Result {
	f.t.Helper()
	res, err := f.run(reason)
	if err != nil {
		f.t.Fatalf("备份失败：%v（%v）", err, res.Problems)
	}
	return res
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func isWindows() bool { return runtime.GOOS == "windows" }

// makeLink 建一个目录链接：Windows 上是联接（mklink /J，不要管理员），别处是符号链接；建不了就跳过这条测试
func makeLink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Skipf("建不了联接：%v %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("建不了符号链接：%v", err)
	}
}

// mediaFile 是 fabricate 里额外放的一个媒体文件
type mediaFile struct{ rel, content string }

// fabricate 在备份目录里直接造一份备份（写清单和一个文件），给轮换这类不需要真做备份的测试用
func fabricate(t *testing.T, dir, name string, reason Reason, status Status, when time.Time, media ...mediaFile) string {
	t.Helper()
	return fabricateAs(t, testSource, dir, name, reason, status, when, media...)
}

func fabricateAs(t *testing.T, source, dir, name string, reason Reason, status Status, when time.Time, media ...mediaFile) string {
	t.Helper()
	folder := filepath.Join(dir, name)
	if status == StatusPartial {
		folder += ".partial"
	}
	if err := os.MkdirAll(filepath.Join(folder, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(folder, "data", "x.txt"), "x")
	m := Manifest{Source: source, Name: name, Time: when, Reason: reason, Status: status, Media: MediaNone, Classes: []Class{ClassSelf},
		Files: []FileEntry{{Path: "x.txt", Size: 1, SHA256: "00", Class: ClassSelf}}}
	for _, mf := range media {
		write(t, filepath.Join(folder, "data", filepath.FromSlash(mf.rel)), mf.content)
		m.Files = append(m.Files, FileEntry{Path: mf.rel, Size: int64(len(mf.content)), SHA256: "ff", Class: ClassMedia})
	}
	if err := writeManifest(folder, m); err != nil {
		t.Fatal(err)
	}
	return folder
}

func names(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}
