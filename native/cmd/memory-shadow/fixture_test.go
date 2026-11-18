package main

// 测试夹具：在 t.TempDir() 里造一个 Go 形状的数据目录（两个平台的聊天库、trace、固定路径下的角色记忆库）。
// 全部是编的数据，号码一律短编号；不碰真实的 data/

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"sumika-native/internal/memory/memtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/testutil/junction"
)

// 会话 id（短编号）
const (
	convOwner   = "qq:private:1:9"   // 她(1)和主人(9)的 QQ 私聊
	convPeer    = "qq:private:1:5"   // 她和别人(5)的 QQ 私聊
	convGroupA  = "qq:group:7"       // QQ 群
	convLocal   = "console:main"     // 本地私聊
	convLocalGp = "console:group:t1" // 本地群聊
	testChar    = "suzune"
)

type fixture struct {
	t     *testing.T
	Data  string
	P     paths.Paths
	set   *store.Set
	local *store.Store
	qq    *store.Store
	at    int64
}

// newFixture 造好数据目录并保持聊天库开着（往里塞事件）；塞完调 closeStores
func newFixture(t *testing.T) *fixture {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	p, err := paths.Init(data)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	local, _ := set.For("console")
	qq, _ := set.For("qq")
	f := &fixture{t: t, Data: data, P: p, set: set, local: local, qq: qq, at: 1_700_000_000_000}
	t.Cleanup(f.closeStores)
	// 数据锁文件
	f.write(filepath.Join(data, "go", "backend.lock"), "")
	// Go 的记忆库（Bun 形状，没迁移）和 Bun 的原库（内容不同，用来认出"拷错了"）
	f.newMemDB(filepath.Join(data, "go", "characters", testChar, "memory.db"), "go")
	f.newMemDB(filepath.Join(data, "characters", testChar, "memory.db"), "bun")
	return f
}

func (f *fixture) write(file, content string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) writeJSON(file string, v any) {
	f.t.Helper()
	b, _ := json.Marshal(v)
	f.write(file, string(b))
}

// newMemDB 造一个 Bun 形状的记忆库，里面放一条标记事实（tag 用来分辨是 Go 副本还是 Bun 原库）
func (f *fixture) newMemDB(file, tag string) {
	f.t.Helper()
	memtest.BunShaped(f.t, file)
	db := f.openMem(file)
	defer db.Close()
	f.exec(db, `INSERT INTO facts (id, kind, text, subject, visibility, status, created_at, scope, conversation) VALUES ('old1', 'fact', ?, 'owner', 'private', 'active', 1000, 'owner', ?)`,
		"标记 "+tag, convOwner)
}

func (f *fixture) openMem(file string) *sql.DB {
	f.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		f.t.Fatal(err)
	}
	return db
}

func (f *fixture) exec(db *sql.DB, q string, args ...any) {
	f.t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		f.t.Fatalf("%s：%v", q, err)
	}
}

// memDB 是 Go 记忆库在数据目录里的位置
func (f *fixture) memDB() string {
	return filepath.Join(f.Data, "go", "characters", testChar, "memory.db")
}

// seedMem 往 Go 记忆库里放几行（比如已有的游标、已有的事实）；在工具拷贝之前调
func (f *fixture) seedMem(fn func(db *sql.DB)) {
	f.t.Helper()
	db := f.openMem(f.memDB())
	defer db.Close()
	fn(db)
}

// ev 写一条事件，返回 seq。actor 决定身份：owner / peer / her
func (f *fixture) ev(conv, who, text string) int64 {
	f.t.Helper()
	f.at += 60_000
	channel, _, _ := strings.Cut(conv, ":")
	st := f.qq
	if channel == "console" {
		st = f.local
	}
	e := store.NewEvent{Channel: channel, Conversation: conv, Kind: "message", Text: text, CreatedAt: f.at}
	switch {
	case who == "owner":
		e.ActorKind, e.ActorID, e.Origin = "owner", "owner", "owner"
	case who == "her":
		e.ActorKind, e.ActorID, e.Origin = "character", testChar, "character"
	case who == "auto": // 插件自动发的：整理会丢掉，不会出现在给模型的聊天记录里
		e.ActorKind, e.ActorID, e.Origin = "human", "qq:auto", "automation:test"
	default: // peer：who 是对方的编号，如 "qq:5"
		e.ActorKind, e.ActorID, e.Origin = "human", who, "peer"
	}
	seq, err := st.AppendEvent(e, []string{testChar})
	if err != nil {
		f.t.Fatal(err)
	}
	return seq
}

// closeStores 关掉聊天库（WAL 落盘），再拷之前必须调
func (f *fixture) closeStores() {
	if f.set != nil {
		f.set.Close()
		f.set = nil
	}
}

// makeLink 在 link 处造一个指向 target 的目录联接 / 符号链接；建不了就跳过这个测试
func makeLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		if err := junction.Create(link, target); err == nil {
			return
		} else {
			t.Skipf("建不了联接：%v", err)
		}
	}
	t.Skip("建不了符号链接")
}
