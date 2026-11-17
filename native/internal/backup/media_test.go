package backup

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func mediaPaths(m Manifest) []string {
	var out []string
	for _, f := range m.Files {
		if f.Class == ClassMedia {
			out = append(out, f.Path)
		}
	}
	sort.Strings(out)
	return out
}

func rel(h string, root string) string { return root + "/" + h[:2] + "/" + h }

func TestMediaStickersOnly(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonDaily) // 配置是 stickers
	m, _ := ReadManifest(res.Folder)
	got := mediaPaths(m)
	want := []string{rel(hAvatar, "go/media/qq"), rel(hSticker, "go/media/qq"), rel(hLegacy, "shared/media")}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("stickers 档该带 %v，实际 %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("stickers 档该带 %v，实际 %v", want, got)
		}
	}
	if exists(filepath.Join(res.Folder, "data", "go", "media", "qq", hImage[:2], hImage)) {
		t.Fatal("普通图片不该在 stickers 档里")
	}
}

func TestMediaAll(t *testing.T) {
	f := newFx(t)
	res, err := f.svc.Run(t.Context(), Request{Reason: ReasonManual, Media: MediaAll})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := ReadManifest(res.Folder)
	got := mediaPaths(m)
	if len(got) != 5 { // go/media/qq 三个、shared/media 两个；.tmp 里的不带
		t.Fatalf("all 档该带 5 个媒体：%v", got)
	}
	for _, p := range got {
		if filepath.Base(filepath.Dir(p)) == ".tmp" {
			t.Fatalf("临时文件不该带：%s", p)
		}
	}
}

func TestMediaNone(t *testing.T) {
	f := newFx(t)
	res, err := f.svc.Run(t.Context(), Request{Reason: ReasonManual, Media: MediaNone})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := ReadManifest(res.Folder)
	if len(mediaPaths(m)) != 0 || exists(filepath.Join(res.Folder, "data", "go", "media")) {
		t.Fatalf("none 档不该带媒体：%v", mediaPaths(m))
	}
	for _, c := range m.Classes {
		if c == ClassMedia {
			t.Fatal("没带媒体，类里不该有 media")
		}
	}
}

func TestMediaRejectsUnknownMode(t *testing.T) {
	f := newFx(t)
	if _, err := f.svc.Run(t.Context(), Request{Reason: ReasonManual, Media: "everything"}); err == nil {
		t.Fatal("不认识的媒体档该拒绝")
	}
}

func TestMediaHardLinksToPrevious(t *testing.T) {
	f := newFx(t)
	big := strings.Repeat("m", 200_000) // 媒体要够大，共用省下的空间才看得出来
	for _, h := range []string{hSticker, hAvatar} {
		write(t, filepath.Join(f.p.GoMediaDir(), "qq", h[:2], h), big)
	}
	write(t, filepath.Join(f.p.MediaDir(), hLegacy[:2], hLegacy), big)
	first := f.mustRun(ReasonDaily)
	f.clock.Add(21 * time.Hour) // 隔了一天
	second := f.mustRun(ReasonDaily)
	m2, _ := ReadManifest(second.Folder)
	if m2.Linked != 3 {
		t.Fatalf("三个媒体都该和上一份共用：linked=%d", m2.Linked)
	}
	for _, rel := range mediaPaths(m2) {
		a, err1 := os.Stat(filepath.Join(first.Folder, "data", filepath.FromSlash(rel)))
		b, err2 := os.Stat(filepath.Join(second.Folder, "data", filepath.FromSlash(rel)))
		if err1 != nil || err2 != nil || !os.SameFile(a, b) {
			t.Errorf("%s 该是同一个文件（硬链接）：%v %v", rel, err1, err2)
		}
		fe, _ := fileOf(m2, rel)
		if !fe.Linked || fe.SHA256 == "" {
			t.Errorf("%s 清单该标共用并带着上一份的哈希：%+v", rel, fe)
		}
	}
	// 非媒体文件不建硬链接（各自一份）
	a, _ := os.Stat(filepath.Join(first.Folder, "data", "config", "mcp.json"))
	b, _ := os.Stat(filepath.Join(second.Folder, "data", "config", "mcp.json"))
	if os.SameFile(a, b) {
		t.Error("普通文件不该共用")
	}
	// 总占用：共用的只算一次
	if u, naive := Usage(f.dir()), usageTree(t.Context(), first.Folder, map[string]bool{})+usageTree(t.Context(), second.Folder, map[string]bool{}); u > naive-3*int64(len(big))+1000 {
		t.Errorf("共用的媒体该只算一次：总 %d、各算各的 %d", u, naive)
	}
}

// 永远不和数据目录本身建硬链接：数据那份坏了，备份跟着坏
func TestMediaNeverLinksToDataDir(t *testing.T) {
	f := newFx(t)
	var linked []string
	f.svc.link = func(oldname, newname string) error {
		linked = append(linked, oldname)
		return os.Link(oldname, newname)
	}
	first := f.mustRun(ReasonDaily)
	f.clock.Add(21 * time.Hour)
	second := f.mustRun(ReasonDaily)
	for _, src := range linked {
		if within(f.data, src) {
			t.Fatalf("硬链接的来源在数据目录里：%s", src)
		}
		if !within(first.Folder, src) {
			t.Fatalf("硬链接的来源该是上一份备份：%s", src)
		}
	}
	for _, res := range []Result{first, second} {
		m, _ := ReadManifest(res.Folder)
		for _, rel := range mediaPaths(m) {
			a, err1 := os.Stat(filepath.Join(res.Folder, "data", filepath.FromSlash(rel)))
			b, err2 := os.Stat(filepath.Join(f.data, filepath.FromSlash(rel)))
			if err1 != nil || err2 != nil || os.SameFile(a, b) {
				t.Errorf("%s 的备份副本不能和数据目录里的是同一个文件：%v %v", rel, err1, err2)
			}
		}
	}
}

func TestMediaFallsBackToCopyWhenLinkFails(t *testing.T) {
	f := newFx(t)
	first := f.mustRun(ReasonDaily)
	f.svc.link = func(oldname, newname string) error { return errors.New("模拟：跨盘，不能建硬链接") }
	f.clock.Add(21 * time.Hour)
	second := f.mustRun(ReasonDaily)
	m2, _ := ReadManifest(second.Folder)
	if m2.Status != StatusOK || m2.Linked != 0 || len(mediaPaths(m2)) != 3 {
		t.Fatalf("链接建不了该退成复制：%+v", m2)
	}
	for _, rel := range mediaPaths(m2) {
		a, _ := os.Stat(filepath.Join(first.Folder, "data", filepath.FromSlash(rel)))
		b, err := os.Stat(filepath.Join(second.Folder, "data", filepath.FromSlash(rel)))
		if err != nil || os.SameFile(a, b) {
			t.Errorf("%s 该是各自的副本：%v", rel, err)
		}
		fe, _ := fileOf(m2, rel)
		size, sum, _ := sha256File(filepath.Join(second.Folder, "data", filepath.FromSlash(rel)))
		if fe.Linked || fe.SHA256 != sum || fe.Size != size {
			t.Errorf("%s 清单该是复制出来的：%+v", rel, fe)
		}
	}
}

// bad 和 .partial 不当作硬链接来源：它们的文件内容没人保证
func TestMediaLinkSourceSkipsBadAndPartial(t *testing.T) {
	f := newFx(t)
	good := f.mustRun(ReasonDaily)
	// 造一份更新的 bad 备份和一份更新的 .partial，里面有同名同大小的媒体文件
	r := rel(hSticker, "go/media/qq")
	fabricate(t, f.dir(), "每天-20261001-0730", ReasonDaily, StatusBad, f.clock.Now().Add(2*time.Hour), mediaFile{r, "sticker-bytes"})
	fabricate(t, f.dir(), "每天-20261001-0830", ReasonDaily, StatusPartial, f.clock.Now().Add(3*time.Hour), mediaFile{r, "sticker-bytes"})
	var sources []string
	f.svc.link = func(oldname, newname string) error {
		sources = append(sources, oldname)
		return os.Link(oldname, newname)
	}
	f.clock.Add(21 * time.Hour)
	f.mustRun(ReasonManual)
	if len(sources) == 0 {
		t.Fatal("前提：该有硬链接")
	}
	for _, s := range sources {
		if !within(good.Folder, s) {
			t.Fatalf("来源该是校验通过的那一份 %s，实际 %s", good.Folder, s)
		}
	}
}

// 媒体目录里有文件、对应的库却不在（旧版库没有、平台没开）：不算失败，记一笔，stickers 档不带那个目录
func TestMediaStickersWithoutMatchingDB(t *testing.T) {
	f := newFx(t)
	if err := os.Remove(f.p.BunEventsDB()); err != nil {
		t.Fatal(err)
	}
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	if m.Status != StatusOK {
		t.Fatalf("%+v", m.Problems)
	}
	for _, p := range mediaPaths(m) {
		if strings.HasPrefix(p, "shared/media/") {
			t.Fatalf("没有对应的库，旧版媒体该一个都不带：%s", p)
		}
	}
	found := false
	for _, n := range m.Notes {
		if strings.Contains(n, "shared/events.db") && strings.Contains(n, "没有副本") {
			found = true
		}
	}
	if !found {
		t.Fatalf("该记一笔：%v", m.Notes)
	}
}

// 硬链接复用只对文件名是 64 位十六进制哈希的文件：别的名字各复制各的
func TestMediaLinksOnlyHashNamedFiles(t *testing.T) {
	f := newFx(t)
	odd := filepath.Join(f.p.GoMediaDir(), "qq", "ab", "not-a-hash.bin")
	write(t, odd, "同名同大小")
	first, err := f.svc.Run(t.Context(), Request{Reason: ReasonDaily, Media: MediaAll})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Add(21 * time.Hour)
	second, err := f.svc.Run(t.Context(), Request{Reason: ReasonDaily, Media: MediaAll})
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := ReadManifest(second.Folder)
	fe, ok := fileOf(m2, "go/media/qq/ab/not-a-hash.bin")
	if !ok || fe.Linked {
		t.Fatalf("文件名不是哈希的不该建硬链接：%+v %v", fe, ok)
	}
	a, _ := os.Stat(filepath.Join(first.Folder, "data", "go", "media", "qq", "ab", "not-a-hash.bin"))
	b, _ := os.Stat(filepath.Join(second.Folder, "data", "go", "media", "qq", "ab", "not-a-hash.bin"))
	if os.SameFile(a, b) {
		t.Fatal("不该是同一个文件")
	}
	if m2.Linked != 5 { // 五个哈希名的媒体照常共用
		t.Fatalf("哈希名的照常共用：%d", m2.Linked)
	}
}
