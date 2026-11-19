package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/datalock"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
	"sumika-native/internal/testutil/junction"
)

func writeMediaFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mediaFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeMediaFixture(t, filepath.Join(dir, "go", "backend.lock"), nil)
	return dir
}

func putGoMedia(t *testing.T, dir, platform, content string) (string, string) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, "go", "media", platform, hash[:2], hash)
	writeMediaFixture(t, path, []byte(content))
	return hash, path
}

func readGoMedia(t *testing.T, dir, platform, hash, want string) {
	t.Helper()
	f, err := mediastore.OpenMedia(dir, platform, hash)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	closeErr := f.Close()
	if err != nil || closeErr != nil || string(b) != want {
		t.Fatalf("媒体内容不对：got=%q want=%q err=%v close=%v", b, want, err, closeErr)
	}
}

func TestMediaReadPlatformIsolation(t *testing.T) {
	dir := mediaFixture(t)
	const content = "编的跨平台媒体"
	hash, local := putGoMedia(t, dir, "local", content)
	_, qq := putGoMedia(t, dir, "qq", content)
	readGoMedia(t, dir, "local", hash, content)
	readGoMedia(t, dir, "qq", hash, content)
	// 同时放旧源，缺文件时两个位置都不能兜底。
	writeMediaFixture(t, filepath.Join(dir, "shared", "media", hash[:2], hash), []byte(content))
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	f, err := mediastore.OpenMedia(dir, "local", hash)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, mediastore.ErrMediaMissing) {
		t.Fatalf("local 缺失却没报缺媒体，可能读到了另一平台：%v", err)
	}
	readGoMedia(t, dir, "qq", hash, content)
	if _, err := os.Stat(qq); err != nil {
		t.Fatal("读取 local 不得动 qq：", err)
	}
}

func TestMediaReadRejectsInvalidHash(t *testing.T) {
	dir := mediaFixture(t)
	for _, hash := range []string{"../x", strings.Repeat("A", 64), strings.Repeat("a", 63), "ab/" + strings.Repeat("a", 61), "ab\\" + strings.Repeat("a", 61)} {
		f, err := mediastore.OpenMedia(dir, "local", hash)
		if f != nil {
			f.Close()
		}
		if !errors.Is(err, mediastore.ErrMediaInvalidHash) {
			t.Errorf("非法 hash 未在读盘前拒绝：%q：%v", hash, err)
		}
	}
	hash, _ := putGoMedia(t, dir, "local", "编的合法媒体")
	if f, err := mediastore.OpenMedia(dir, "../qq", hash); err == nil {
		f.Close()
		t.Fatal("非法平台没有拒绝")
	}
}

func TestMediaCheckIgnoresRetiredImportHistory(t *testing.T) {
	dir := mediaFixture(t)
	writeMediaFixture(t, filepath.Join(dir, "go", "import", "interrupted", "activation.json"), []byte("损坏的旧启用日志"))
	addMediaEvents(t, dir, "qq")
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-mode", "media-check", "-data", dir}, &out, &stderr, nil); code != 0 {
		t.Fatalf("旧日志阻止媒体核对：code=%d out=%s err=%s", code, &out, &stderr)
	}
}

func addMediaEvents(t *testing.T, dir, platform string, envelopes ...string) {
	t.Helper()
	file := filepath.Join(dir, "go", "chat", platform+".db")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenChatFile(file, platform)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, env := range envelopes {
		if _, err := db.Exec(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,created_at) VALUES (?,?,'编的会话','owner','owner','message','编的正文',?,1)`, fmt.Sprintf("media-%d", i), platform, env); err != nil {
			t.Fatal(err)
		}
	}
}

func mediaTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[path] = "directory"
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		files[path] = hex.EncodeToString(h[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestMediaCheckReadOnly(t *testing.T) {
	dir := mediaFixture(t)
	hash, _ := putGoMedia(t, dir, "local", "编的历史和新媒体")
	missing := strings.Repeat("b", 64)
	// 第三条模拟旧 mface 的 hash 只留在 legacy 里，按 ref 和下标核对后才能读。
	addMediaEvents(t, dir, "local",
		fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}],"import_meta":{"version":1,"legacy_segments":[{"type":"image","hash":%q}]}}`, hash, hash),
		fmt.Sprintf(`{"segs":[{"kind":"emoji","hash":%q}]}`, hash),
		fmt.Sprintf(`{"segs":[{"kind":"emoji","ref":"编的表情"}],"import_meta":{"version":1,"legacy_segments":[{"type":"mface","emojiId":"编的表情","hash":%q}]}}`, hash),
		fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}]}`, missing),
		`{"segs":[{"kind":"image"},{"kind":"text","text":"编的正文"}]}`)
	addMediaEvents(t, dir, "qq", `{"segs":[{"kind":"emoji"}]}`)
	before := mediaTree(t, dir)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"-mode", "media-check", "-data", dir}, &out, &stderr, nil)
	want := "local 段数=5 读到=3 缺文件=1 内容不符=0 hash为空=1 读取出错=0\nqq 段数=1 读到=0 缺文件=0 内容不符=0 hash为空=1 读取出错=0\n"
	if code != 0 || out.String() != want || stderr.Len() != 0 {
		t.Fatalf("新旧段或缺媒体计数不对：code=%d\nout=%s\nerr=%s", code, &out, &stderr)
	}
	for _, secret := range []string{hash, missing, dir, "编的正文", "编的表情"} {
		if strings.Contains(out.String()+stderr.String(), secret) {
			t.Errorf("诊断泄漏了路径、hash 或正文：%q", secret)
		}
	}
	if after := mediaTree(t, dir); !reflect.DeepEqual(before, after) {
		for path, digest := range after {
			if before[path] != digest {
				t.Errorf("媒体核对改了文件：%s：%q → %q", filepath.Base(path), before[path], digest)
			}
		}
		for path := range before {
			if _, ok := after[path]; !ok {
				t.Errorf("媒体核对删了文件：%s", filepath.Base(path))
			}
		}
	}
}

func TestMediaCheckFailures(t *testing.T) {
	for _, kind := range []string{"内容不符", "非法hash", "不是普通文件", "旧段hash冲突", "旧段身份冲突", "旧版本未知", "坏JSON", "坏库"} {
		t.Run(kind, func(t *testing.T) {
			dir := mediaFixture(t)
			hash, path := putGoMedia(t, dir, "local", "编的正常媒体")
			env := fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}]}`, hash)
			want := "内容不符=0 hash为空=0 读取出错=1"
			switch kind {
			case "内容不符":
				writeMediaFixture(t, path, []byte("编的错误内容"))
				want = "内容不符=1 hash为空=0 读取出错=0"
			case "非法hash":
				env = `{"segs":[{"kind":"image","hash":"../x"}]}`
			case "不是普通文件":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "旧段hash冲突":
				env = fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}],"import_meta":{"version":1,"legacy_segments":[{"type":"image","hash":%q}]}}`, hash, strings.Repeat("b", 64))
			case "旧段身份冲突":
				env = fmt.Sprintf(`{"segs":[{"kind":"emoji","ref":"编的表情甲","hash":%q}],"import_meta":{"version":1,"legacy_segments":[{"type":"mface","emojiId":"编的表情乙","hash":%q}]}}`, hash, hash)
			case "旧版本未知":
				env = fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}],"import_meta":{"version":2,"legacy_segments":[{"type":"image","hash":%q}]}}`, hash, hash)
			case "坏JSON":
				env = `{`
			}
			addMediaEvents(t, dir, "local", env)
			if kind == "坏库" {
				writeMediaFixture(t, filepath.Join(dir, "go", "chat", "local.db"), []byte("编的坏库"))
			}
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"-mode", "media-check", "-data", dir}, &out, &stderr, nil)
			if code != exitError || !strings.Contains(out.String(), want) || stderr.String() != "媒体核对失败\n" {
				t.Fatalf("错误未单列或退出码不对：code=%d out=%s err=%s", code, &out, &stderr)
			}
			if strings.Contains(out.String()+stderr.String(), hash) || strings.Contains(out.String()+stderr.String(), dir) {
				t.Fatal("失败输出泄漏了 hash 或路径")
			}
		})
	}
}

// 真起命令，证明参数分流、退出码和计数能在没有配置、后台和日志的目录里工作。
func TestMediaCheckCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("要现编一个 exe，放在每日整套")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exe := filepath.Join(t.TempDir(), "sumika-go.exe")
	if b, err := exec.CommandContext(ctx, "go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("编译诊断命令：%v\n%s", err, b)
	}
	dir := mediaFixture(t)
	hash, _ := putGoMedia(t, dir, "local", "编的命令媒体")
	addMediaEvents(t, dir, "local", fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}]}`, hash))
	writeMediaFixture(t, filepath.Join(dir, "go", "import", "编的非法 run", "activation.json"), []byte("编的无关日志"))
	before := mediaTree(t, dir)
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, "-mode", "media-check", "-data", dir)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if err != nil || out.String() != "local 段数=1 读到=1 缺文件=0 内容不符=0 hash为空=0 读取出错=0\n" || stderr.Len() != 0 {
		t.Fatalf("真实诊断命令输出不对：%v\nstdout=%s\nstderr=%s", err, &out, &stderr)
	}
	t.Logf("真实诊断输出：%s；stderr 字节=%d", &out, stderr.Len())
	if !reflect.DeepEqual(before, mediaTree(t, dir)) {
		t.Fatal("真实诊断命令写了数据目录")
	}
}

func TestMediaCheckWALReadOnly(t *testing.T) {
	dir := mediaFixture(t)
	hash, _ := putGoMedia(t, dir, "local", "编的未合并WAL媒体")
	addMediaEvents(t, dir, "local")
	db, err := store.OpenChatFile(filepath.Join(dir, "go", "chat", "local.db"), "local")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0; INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,created_at) VALUES ('wal-media','console','编的会话','owner','owner','message','编的WAL正文',?,1)`, fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}]}`, hash)); err != nil {
		t.Fatal(err)
	}
	// 写连接保持打开，防止关闭时自动 checkpoint；读取期间不再写入。
	before := mediaTree(t, dir)
	counts, err := mediastore.CheckMedia(context.Background(), dir)
	want := []mediastore.MediaCheckCounts{{Platform: "local", Segments: 1, Read: 1}}
	if err != nil || !reflect.DeepEqual(counts, want) {
		t.Fatalf("未读到 WAL 中的段：counts=%+v err=%v", counts, err)
	}
	if !reflect.DeepEqual(before, mediaTree(t, dir)) {
		t.Fatal("核对改了主库、WAL 或 SHM")
	}
}

func TestMediaReadRejectsLinks(t *testing.T) {
	for _, level := range []string{"平台", "分片", "文件位置"} {
		t.Run(level, func(t *testing.T) {
			dir := mediaFixture(t)
			hash, qq := putGoMedia(t, dir, "qq", "编的链接目标")
			link := filepath.Join(dir, "go", "media", "local")
			target := filepath.Dir(filepath.Dir(qq))
			if level != "平台" {
				if err := os.MkdirAll(link, 0o755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, hash[:2])
				target = filepath.Dir(qq)
				if level == "文件位置" {
					if err := os.MkdirAll(link, 0o755); err != nil {
						t.Fatal(err)
					}
					link = filepath.Join(link, hash)
				}
			}
			makeMediaLink(t, link, target)
			f, err := mediastore.OpenMedia(dir, "local", hash)
			if f != nil {
				f.Close()
			}
			if !errors.Is(err, mediastore.ErrMediaLink) || errors.Is(err, mediastore.ErrMediaMissing) || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("联接必须报明确的拒绝错误，不能算缺文件：%v", err)
			}
			addMediaEvents(t, dir, "local", fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}]}`, hash))
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"-mode", "media-check", "-data", dir}, &out, &stderr, nil)
			want := "local 段数=1 读到=0 缺文件=0 内容不符=0 hash为空=0 读取出错=1\n"
			if code != exitError || out.String() != want || stderr.String() != "媒体核对失败\n" {
				t.Fatalf("联接没有记入读取出错：code=%d out=%s err=%s", code, &out, &stderr)
			}
			readGoMedia(t, dir, "qq", hash, "编的链接目标")
		})
	}
}

func makeMediaLink(t *testing.T, link, target string) {
	t.Helper()
	if err := junction.Create(link, target); err != nil {
		t.Fatalf("建编造联接：%v", err)
	}
}

func TestMediaReadDataRootLink(t *testing.T) {
	dir := mediaFixture(t)
	hash, _ := putGoMedia(t, dir, "local", "编的根联接媒体")
	addMediaEvents(t, dir, "local", fmt.Sprintf(`{"segs":[{"kind":"image","hash":%q}]}`, hash))
	alias := filepath.Join(t.TempDir(), "编的根联接")
	makeMediaLink(t, alias, dir)
	readGoMedia(t, alias, "local", hash, "编的根联接媒体")
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"-mode", "media-check", "-data", alias}, &out, &stderr, nil)
	if code != 0 || out.String() != "local 段数=1 读到=1 缺文件=0 内容不符=0 hash为空=0 读取出错=0\n" || stderr.Len() != 0 {
		t.Fatalf("数据根联接没有沿用启动路径的许可：code=%d out=%s err=%s", code, &out, &stderr)
	}
}

func TestMediaCheckRequiresIdleData(t *testing.T) {
	dir := mediaFixture(t)
	addMediaEvents(t, dir, "local", `{"segs":[]}`)
	before := mediaTree(t, dir)
	lock, err := datalock.LockData(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = mediastore.CheckMedia(context.Background(), dir)
	if !errors.Is(err, datalock.ErrDataInUse) {
		t.Errorf("后台占用时未拒绝：%v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, mediaTree(t, dir)) {
		t.Fatal("锁失败时写了数据目录")
	}
	if err := os.Remove(filepath.Join(dir, "go", "backend.lock")); err != nil {
		t.Fatal(err)
	}
	before = mediaTree(t, dir)
	if _, err := mediastore.CheckMedia(context.Background(), dir); err == nil {
		t.Fatal("缺锁文件时不应创建或绕过锁")
	}
	if !reflect.DeepEqual(before, mediaTree(t, dir)) {
		t.Fatal("诊断创建了锁文件")
	}
}
