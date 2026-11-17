package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 每次备份结束时算好实际占用写进 .usage.json；backup list 只读它，不遍历备份里的文件、不逐个取文件身份
func TestOverviewReadsSavedUsageWithoutWalking(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	u, ok := readUsage(f.dir(), testSource)
	if !ok || u.Bytes <= 0 || !u.At.Equal(f.clock.Now()) {
		t.Fatalf("备份结束时该把占用写进小文件：%+v %v", u, ok)
	}
	// 小文件是在算完以后写的，所以比现算的少它自己那几十个字节
	if want := Usage(f.dir()); u.Bytes > want || want-u.Bytes > 4096 {
		t.Fatalf("写进去的该是真实占用（硬链接只算一次）：%d / %d", u.Bytes, want)
	}
	// 写一个哨兵值：Overview 返回的是它，说明没有现算
	if err := writeUsage(f.dir(), 12345, at(2, 0, 0), testSource); err != nil {
		t.Fatal(err)
	}
	// 再把备份里的数据文件删光：现算的话占用会变，读小文件的话不会，而且 Overview 不会因此出错
	if err := os.RemoveAll(filepath.Join(res.Folder, "data")); err != nil {
		t.Fatal(err)
	}
	o, entries := f.svc.Overview()
	if o.UsedBytes != 12345 || o.UsedEstimated || !o.UsedAt.Equal(at(2, 0, 0)) {
		t.Fatalf("占用该直接读小文件：%+v", o)
	}
	if len(entries) != 1 || o.Total != 1 || o.Manual != 1 {
		t.Fatalf("份数、原因也该来自清单：%+v %v", o, names(entries))
	}
}

// 没有算好的占用（旧备份目录、小文件被删）：按各份清单估算（自己占的加起来），标明是估算
func TestOverviewEstimatesWhenNoSavedUsage(t *testing.T) {
	f := newFx(t)
	f.mustRun(ReasonDaily)
	f.clock.Add(21 * time.Hour)
	f.mustRun(ReasonDaily)
	if err := os.Remove(filepath.Join(f.dir(), usageFile)); err != nil {
		t.Fatal(err)
	}
	o, entries := f.svc.Overview()
	if !o.UsedEstimated || !o.UsedAt.IsZero() {
		t.Fatalf("该标明是估算：%+v", o)
	}
	var want int64
	for _, e := range entries {
		want += e.Manifest.Bytes - e.Manifest.SharedBytes
	}
	if o.UsedBytes != want || want <= 0 {
		t.Fatalf("估算该是各份自己占的加起来：%d / %d", o.UsedBytes, want)
	}
	// 共用的大小记在清单里：第二份的媒体和第一份共用
	if entries[0].Manifest.SharedBytes <= 0 || entries[0].Manifest.Linked != 3 {
		t.Fatalf("清单该记共用的大小：%+v", entries[0].Manifest)
	}
}

// 读清单的头不解文件列表：files 写在最后，头齐了就停；后面就算是坏的也不影响（证明没读它）
func TestReadHeaderStopsBeforeFileList(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "每天-20261001-0530")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	head := Manifest{Program: Program, Format: FormatVersion, Name: "每天-20261001-0530", Time: at(1, 5, 30), Reason: ReasonDaily, Status: StatusOK, Bytes: 777, Linked: 2, SharedBytes: 100}
	b, _ := json.Marshal(head)
	s := strings.TrimSuffix(string(b), "}")
	// 照真实清单的顺序：头在前，files 在最后；这里的 files 后面是坏的，读头的人读到 files 就该停了
	body := strings.Replace(s, `,"files":null`, "", 1) + `,"files":[{"path":"a"` + strings.Repeat(`,{坏的`, 5)
	write(t, filepath.Join(folder, ManifestFile), body)
	m, err := readHeader(folder, "每天-20261001-0530")
	if err != nil {
		t.Fatalf("头齐了就该读得出来：%v", err)
	}
	if m.Bytes != 777 || m.Linked != 2 || m.SharedBytes != 100 {
		t.Fatalf("%+v", m)
	}
	// 但完整读（要文件列表）就读不懂了
	if _, err := ReadManifest(folder); err == nil {
		t.Fatal("完整读坏的清单该报错")
	}
	// 头没齐就碰到 files：不能凭空认
	write(t, filepath.Join(folder, ManifestFile), `{"program":"`+Program+`","format":1,"files":[],"name":"x"}`)
	if _, err := readHeader(folder, "每天-20261001-0530"); err == nil {
		t.Fatal("头没齐不该认")
	}
}

// 带很多文件的清单：读头的耗时不随文件数增长（读头不解文件列表）。只在同一台机器上比两个量级，不比绝对时间
func TestScanCostDoesNotGrowWithFileCount(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, n int) {
		folder := filepath.Join(dir, name)
		files := make([]FileEntry, n)
		for i := range files {
			files[i] = FileEntry{Path: "go/media/qq/ab/" + strings.Repeat("a", 60) + string(rune('a'+i%26)), Size: 1000, SHA256: strings.Repeat("0", 64), Class: ClassMedia}
		}
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		m := Manifest{Source: testSource, Name: name, Time: at(1, 5, 30), Reason: ReasonDaily, Status: StatusOK, Media: MediaAll, Files: files}
		if err := writeManifest(folder, m); err != nil {
			t.Fatal(err)
		}
	}
	mk("每天-20261001-0530", 150_000) // 约 30 MB 的清单
	fi, err := os.Stat(filepath.Join(dir, "每天-20261001-0530", ManifestFile))
	if err != nil || fi.Size() < 20<<20 {
		t.Fatalf("前提：清单该有二十多 MB：%v %v", fi, err)
	}
	start := time.Now()
	es, err := Scan(dir)
	took := time.Since(start)
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", err, es)
	}
	// 解 15 万条要好几秒；只读头应该远低于这个数。给足余量（机器忙时也不误红），只拦“又把文件列表全解了”
	if took > 2*time.Second {
		t.Fatalf("读头花了 %v，像是又把文件列表解了一遍", took)
	}
}
