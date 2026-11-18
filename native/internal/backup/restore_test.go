package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/paths"
)

// 恢复成新目录的测试（规格-数据目录与备份 10.10）：一律用真备份（f.mustRun），清单里的哈希是真的

// treeHashes 给一个目录里每个普通文件算 sha256（相对路径、斜杠）；skip 里的相对路径前缀不算
func treeHashes(t *testing.T, root string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, s := range skip {
			if rel == s || strings.HasPrefix(rel, s+"/") {
				return nil
			}
		}
		_, sum, err := sha256File(p)
		if err != nil {
			return err
		}
		out[rel] = sum
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (f *fx) target(name string) string { return filepath.Join(f.base, name) }

func readReport(t *testing.T, root string) RestoreReport {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, paths.RestoreDir, restoreReportFile))
	if err != nil {
		t.Fatalf("读不到核对报告：%v", err)
	}
	var r RestoreReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func readMarker(t *testing.T, root string) incompleteMarker {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, paths.RestoreDir, paths.RestoreIncompleteFile))
	if err != nil {
		t.Fatalf("读不到未完成标记：%v", err)
	}
	var m incompleteMarker
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// rewriteManifest 读出一份备份的清单、交给 mut 改、再写回（备份文件夹已经是正式名字，直接覆盖清单）
func rewriteManifest(t *testing.T, folder string, mut func(*Manifest)) {
	t.Helper()
	m, err := ReadManifest(folder)
	if err != nil {
		t.Fatal(err)
	}
	mut(&m)
	if err := writeManifest(folder, m); err != nil {
		t.Fatal(err)
	}
}

func dbIndex(t *testing.T, m Manifest, rel string) int {
	t.Helper()
	for i, d := range m.DBs {
		if d.Path == rel {
			return i
		}
	}
	t.Fatalf("清单里没有库 %s：%+v", rel, m.DBs)
	return -1
}

// 带真数据的备份：聊天库里放够多行，页数多一点（给“库坏了”的测试用）
func (f *fx) bigBackup() Result {
	f.t.Helper()
	mustExec(f.t, f.chat, `CREATE TABLE IF NOT EXISTS load (n INTEGER, pad TEXT)`)
	for i := 0; i < 300; i++ {
		mustExec(f.t, f.chat, `INSERT INTO load VALUES (?, ?)`, i, strings.Repeat("填", 80))
	}
	return f.mustRun(ReasonManual)
}

// 备份 → 删掉库 → 恢复成新目录 → 各表行数和清单一致（P1-1 的验收原文）
func TestRestoreRoundTrip(t *testing.T) {
	f := newFx(t)
	res := f.bigBackup()
	m, err := ReadManifest(res.Folder)
	if err != nil {
		t.Fatal(err)
	}
	backupBefore := treeHashes(t, res.Folder)
	// 备份之后数据变了、库被删了
	mustExec(t, f.chat, `INSERT INTO load VALUES (-1, '备份之后才写的')`)
	f.trace.Close()
	for _, side := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(f.p.GoTraceDB() + side)
	}
	write(t, f.p.ConfigFile(), "备份之后改过的配置")
	dataBefore := treeHashes(t, f.data)

	target := f.target("恢复出的数据目录")
	rep, err := f.svc.Restore(context.Background(), res.Name, target)
	if err != nil {
		t.Fatalf("恢复失败：%v %v", err, rep.Problems)
	}
	if !rep.OK || rep.Files != len(m.Files) || len(rep.DBs) != len(m.DBs) || !rep.RowsRecorded {
		t.Fatalf("报告不对：%+v", rep)
	}
	for _, d := range rep.DBs {
		if d.Integrity != "ok" || d.Rows != RowsMatch {
			t.Errorf("库 %s 核对不是通过：%+v", d.Path, d)
		}
	}
	// 每个文件的哈希就是清单里的
	for _, e := range m.Files {
		_, sum, err := sha256File(filepath.Join(target, filepath.FromSlash(e.Path)))
		if err != nil || sum != e.SHA256 {
			t.Errorf("%s 哈希对不上：%v %s", e.Path, err, sum)
		}
	}
	// 删掉的库回来了，备份之后写的那一行没有（恢复的是备份那一刻的状态）
	chat := openRW(t, filepath.Join(target, "go", "chat", "qq.db"))
	var n int
	if err := chat.QueryRow(`SELECT count(*) FROM load`).Scan(&n); err != nil || n != 300 {
		t.Fatalf("恢复出的聊天库 load 表该是备份时的 300 行：%d %v", n, err)
	}
	if !exists(filepath.Join(target, "go", "trace.db")) {
		t.Fatal("被删掉的 trace 库该回来")
	}
	if got, err := os.ReadFile(filepath.Join(target, "config", "sumika.yml")); err != nil || strings.Contains(string(got), "备份之后改过") {
		t.Fatalf("配置该是备份那一刻的：%v %q", err, got)
	}
	// 报告写进了新目录，未完成标记删了，新目录能当数据目录打开
	if r := readReport(t, target); !r.OK || r.Files != rep.Files {
		t.Fatalf("新目录里的报告对不上：%+v", r)
	}
	if exists(filepath.Join(target, paths.RestoreDir, paths.RestoreIncompleteFile)) {
		t.Fatal("核对通过以后未完成标记该删掉")
	}
	if _, err := paths.Open(target); err != nil {
		t.Fatalf("恢复出的目录该能当数据目录打开：%v", err)
	}
	// 只读备份、不碰现在的数据目录、不切换
	if !sameTree(backupBefore, treeHashes(t, res.Folder)) {
		t.Fatal("恢复动了备份文件夹")
	}
	if !sameTree(dataBefore, treeHashes(t, f.data)) {
		t.Fatal("恢复动了现在的数据目录")
	}
}

func TestRestoreRefusesBadNames(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	dir := f.dir()
	fabricate(t, dir, "手动-20261001-0000", ReasonManual, StatusBad, at(1, 0, 0))
	fabricate(t, dir, "手动-20261002-0000", ReasonManual, StatusPartial, at(2, 0, 0))
	cases := []struct {
		name string
		want error
	}{
		{"", ErrBadBackupName},
		{"   ", ErrBadBackupName},
		{"../" + res.Name, ErrBadBackupName},
		{"..", ErrBadBackupName},
		{res.Name + "/data", ErrBadBackupName},
		{`a\b`, ErrBadBackupName},
		{"C:" + res.Name, ErrBadBackupName},
		{filepath.Join(dir, res.Name), ErrBadBackupName},
		{"手动-不存在", ErrBackupNotFound},
		{"手动-20261001-0000", ErrBackupUnusable}, // 没通过校验
		{"手动-20261002-0000", ErrBackupUnusable}, // 还在做（.partial）
		{"手动-20261002-0000.partial", ErrBackupNotFound},
	}
	for _, c := range cases {
		target := filepath.Join(f.base, "t", "x")
		_, err := f.svc.Restore(context.Background(), c.name, target)
		if !errors.Is(err, c.want) {
			t.Errorf("备份名 %q：想要 %v，得到 %v", c.name, c.want, err)
		}
		if exists(filepath.Join(f.base, "t")) {
			t.Fatalf("备份名 %q 被拒绝了，目标目录却被建出来了", c.name)
		}
	}
	if isWindows() { // Windows 的文件夹名不分大小写
		upper := strings.ToUpper(res.Name)
		if upper != res.Name {
			rep, err := f.svc.Restore(context.Background(), upper, f.target("大小写"))
			if err != nil || !rep.OK {
				t.Errorf("Windows 上大小写不同的备份名该认成同一份：%v", err)
			}
		}
	}
}

func TestRestoreAllowsOtherSourceWithNote(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	rewriteManifest(t, res.Folder, func(m *Manifest) { m.Source = "别的电脑上的数据目录" })
	rep, err := f.svc.Restore(context.Background(), res.Name, f.target("换电脑恢复"))
	if err != nil || !rep.OK || !rep.OtherSource {
		t.Fatalf("别的数据目录的备份该能恢复并标出来：%v %+v", err, rep)
	}
	if !strings.Contains(strings.Join(rep.Notes, "；"), "别的数据目录") {
		t.Fatalf("报告里该写一句说明：%v", rep.Notes)
	}
}

// 目标目录的各种形状：一律拒绝，不建任何东西、不碰备份和数据目录
func TestRestoreTargetRules(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Dir = "上级/子级/备份" }) // 备份目录在 base/上级/子级/备份：给“备份目录的上级”一个不同于数据目录上级的位置
	res := f.mustRun(ReasonManual)
	dir := f.dir()
	backupBefore := treeHashes(t, res.Folder)
	dataBefore := treeHashes(t, f.data)

	nonEmpty := f.target("已有东西")
	write(t, filepath.Join(nonEmpty, ".keep"), "x")
	aFile := f.target("是个文件")
	write(t, aFile, "x")
	emptyDir := f.target("空目录")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkToEmpty := f.target("指向空目录的联接")
	makeLink(t, emptyDir, linkToEmpty)
	root := filepath.VolumeName(f.base) + string(filepath.Separator)

	type tc struct {
		name, to string
		created  string // 不该被建出来的路径（空 = 不查）
	}
	// 错误信息里该有的话（按名字，没写的不查）：位置、联接、非空这些检查互相兜着，光看“被拒绝了”分不出是哪一道
	wantMsg := map[string]string{
		"联接（链到空目录）":       "联接或符号链接",
		"已存在非空（只有一个隐藏文件）": "不是空的",
		"已存在是文件":          "是个文件",
		"就是数据目录":          "就是数据目录，或在数据目录",
		"数据目录里不存在的子目录":    "就是数据目录，或在数据目录",
		"数据目录的上级":         "是数据目录",
		"就是备份目录":          "就是备份目录，或在备份目录",
		"备份目录里不存在的子目录":    "就是备份目录，或在备份目录",
		"备份目录的上级":         "是备份目录",
	}
	cases := []tc{
		{"没写", "", ""},
		{"相对路径", filepath.Join("相对", "目录"), filepath.Join("相对", "目录")},
		{"已存在非空（只有一个隐藏文件）", nonEmpty, ""},
		{"已存在是文件", aFile, ""},
		{"就是数据目录", f.data, ""},
		{"数据目录里不存在的子目录", filepath.Join(f.data, "新子目录"), filepath.Join(f.data, "新子目录")},
		{"数据目录里已有的子目录", filepath.Join(f.data, "config"), ""},
		{"数据目录的上级", f.base, ""},
		{"盘符根", root, ""},
		{"就是备份目录", dir, ""},
		{"备份目录里不存在的子目录", filepath.Join(dir, "新子目录"), filepath.Join(dir, "新子目录")},
		{"备份目录的上级", filepath.Dir(dir), ""},
		{"联接（链到空目录）", linkToEmpty, ""},
		{"UNC", `\\server\share\restore`, ""},
		{"设备路径写成 UNC", `\\?\UNC\server\share\restore`, ""},
		{"斜杠写的 UNC", `//server/share/restore`, ""},
	}
	if isWindows() {
		cases = append(cases,
			tc{"末尾带点", f.target("以点结尾."), ""},
			tc{"末尾带空格", f.target("以空格结尾 "), ""},
			tc{"带冒号（备用数据流）", f.target("a:流"), ""},
			tc{"保留名 NUL", f.target("NUL"), ""},
			tc{"保留名 con.txt", filepath.Join(f.base, "con.txt", "x"), ""},
			tc{"设备路径前缀的数据目录", `\\?\` + f.data, ""},
		)
	}
	for _, c := range cases {
		_, err := f.svc.Restore(context.Background(), res.Name, c.to)
		if !errors.Is(err, ErrTargetUnsafe) {
			t.Errorf("%s（%q）：想要 ErrTargetUnsafe，得到 %v", c.name, c.to, err)
		} else if m := wantMsg[c.name]; m != "" && !strings.Contains(err.Error(), m) {
			t.Errorf("%s（%q）：该是因为“%s”被拒绝，实际是：%v", c.name, c.to, m, err)
		}
		if c.created != "" && exists(c.created) {
			t.Errorf("%s：被拒绝了却建出了 %s", c.name, c.created)
		}
	}
	if ents, _ := os.ReadDir(emptyDir); len(ents) != 0 {
		t.Errorf("链到空目录的联接被拒绝了，空目录里却写进了东西：%v", ents)
	}
	if ents, _ := os.ReadDir(nonEmpty); len(ents) != 1 {
		t.Errorf("非空目录被拒绝了，里面却变了：%v", ents)
	}
	if !sameTree(backupBefore, treeHashes(t, res.Folder)) || !sameTree(dataBefore, treeHashes(t, f.data)) {
		t.Fatal("被拒绝的恢复动了备份文件夹或数据目录")
	}
}

// 上级目录是联接、解开以后落进数据目录里：解到真实路径再比，拒绝
func TestRestoreTargetViaLinkIntoData(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	link := f.target("通往数据目录的联接")
	makeLink(t, f.data, link)
	to := filepath.Join(link, "新的恢复目录")
	if _, err := f.svc.Restore(context.Background(), res.Name, to); !errors.Is(err, ErrTargetUnsafe) {
		t.Fatalf("经联接落进数据目录里该拒绝：%v", err)
	}
	if exists(filepath.Join(f.data, "新的恢复目录")) {
		t.Fatal("被拒绝了，数据目录里却建出了东西")
	}
}

// Windows 上大小写不同的同一个路径
func TestRestoreTargetCaseAlias(t *testing.T) {
	if !isWindows() {
		t.Skip("只有 Windows 的路径不分大小写")
	}
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	for _, to := range []string{strings.ToUpper(f.data), strings.ToLower(f.data), strings.ToUpper(f.dir())} {
		if _, err := f.svc.Restore(context.Background(), res.Name, to); !errors.Is(err, ErrTargetUnsafe) {
			t.Errorf("%s：大小写不同的同一个路径该拒绝：%v", to, err)
		}
	}
}

func TestRestoreEmptyExistingTargetOK(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	to := f.target("预先建好的空目录")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := f.svc.Restore(context.Background(), res.Name, to)
	if err != nil || !rep.OK {
		t.Fatalf("空目录该能恢复进去：%v %+v", err, rep)
	}
}

// 备份之间的媒体靠硬链接共用：恢复出来的必须是独立的文件，改它不能改到备份
func TestRestoreCopiesLinkedFiles(t *testing.T) {
	f := newFx(t)
	f.mustRun(ReasonDaily)
	f.clock.Add(25 * time.Hour)
	second, err := f.svc.Run(context.Background(), Request{Reason: ReasonManual, Media: MediaAll})
	if err != nil {
		t.Fatal(err)
	}
	if second.Linked == 0 {
		t.Skip("这个文件系统建不了硬链接")
	}
	m, _ := ReadManifest(second.Folder)
	var linked FileEntry
	for _, e := range m.Files {
		if e.Linked {
			linked = e
			break
		}
	}
	to := f.target("恢复含共用媒体")
	rep, err := f.svc.Restore(context.Background(), second.Name, to)
	if err != nil || !rep.OK {
		t.Fatalf("%v %+v", err, rep)
	}
	inBackup := filepath.Join(second.Folder, "data", filepath.FromSlash(linked.Path))
	restored := filepath.Join(to, filepath.FromSlash(linked.Path))
	a, _ := os.Stat(inBackup)
	b, _ := os.Stat(restored)
	if os.SameFile(a, b) {
		t.Fatal("恢复出的文件和备份里的是同一个文件（硬链接）")
	}
	if err := os.WriteFile(restored, []byte("恢复出来以后又改了"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, sum, err := sha256File(inBackup)
	if err != nil || sum != linked.SHA256 {
		t.Fatalf("改恢复出的文件改到了备份：%v %s", err, sum)
	}
}

func TestRestoreDetectsHashMismatch(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	victim := filepath.Join(res.Folder, "data", "config", "sumika.yml")
	b, _ := os.ReadFile(victim)
	b[0] ^= 0xff // 大小不变，内容变了
	if err := os.WriteFile(victim, b, 0o644); err != nil {
		t.Fatal(err)
	}
	backupBefore := treeHashes(t, res.Folder)
	to := f.target("哈希不对")
	rep, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrRestoreFailed) || rep.OK {
		t.Fatalf("哈希不对该失败：%v %+v", err, rep)
	}
	if !strings.Contains(err.Error(), "config/sumika.yml") {
		t.Fatalf("错误里该点名是哪个文件：%v", err)
	}
	if exists(filepath.Join(to, "config", "sumika.yml")) {
		t.Fatal("对不上的那个文件不该留在新目录里")
	}
	if mk := readMarker(t, to); mk.State != "failed" {
		t.Fatalf("标记该是 failed：%+v", mk)
	}
	if _, err := paths.Open(to); !errors.Is(err, paths.ErrRestoreIncomplete) {
		t.Fatalf("没恢复完的目录不能当数据目录：%v", err)
	}
	if r := readReport(t, to); r.OK || len(r.Problems) == 0 {
		t.Fatalf("报告该写明失败：%+v", r)
	}
	if !sameTree(backupBefore, treeHashes(t, res.Folder)) {
		t.Fatal("失败的恢复也不许动备份")
	}
}

func TestRestoreDetectsSizeMismatch(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	victim := filepath.Join(res.Folder, "data", "config", "mcp.json")
	if err := os.WriteFile(victim, []byte("短了"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Restore(context.Background(), res.Name, f.target("大小不对"))
	if !errors.Is(err, ErrRestoreFailed) || !strings.Contains(err.Error(), "config/mcp.json") || !strings.Contains(err.Error(), "大小") {
		t.Fatalf("%v", err)
	}
}

func TestRestoreDetectsMissingFile(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	if err := os.Remove(filepath.Join(res.Folder, "data", "go", "reminders.json")); err != nil {
		t.Fatal(err)
	}
	to := f.target("缺文件")
	_, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrRestoreFailed) || !strings.Contains(err.Error(), "reminders.json") {
		t.Fatalf("%v", err)
	}
	if mk := readMarker(t, to); mk.State != "failed" {
		t.Fatalf("%+v", mk)
	}
}

// 备份文件夹里有清单之外的文件：不恢复它，报告说一句
func TestRestoreIgnoresExtraFilesInBackup(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	write(t, filepath.Join(res.Folder, "data", "多出来的.txt"), "不在清单里")
	to := f.target("多文件")
	rep, err := f.svc.Restore(context.Background(), res.Name, to)
	if err != nil || !rep.OK {
		t.Fatalf("%v %+v", err, rep)
	}
	if exists(filepath.Join(to, "多出来的.txt")) {
		t.Fatal("清单之外的文件不该被恢复")
	}
	if !strings.Contains(strings.Join(rep.Notes, "；"), "清单之外") {
		t.Fatalf("报告里该写一句：%v", rep.Notes)
	}
}

// 清单是从盘上读来的数据：不可信的写法一律在开工前拒绝，一个文件都没写，也没写到目标之外
func TestRestoreRejectsHostileManifest(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	orig, _ := ReadManifest(res.Folder)
	mutate := func(mut func(*Manifest)) {
		m := orig
		m.Files = append([]FileEntry(nil), orig.Files...)
		m.DBs = append([]DBEntry(nil), orig.DBs...)
		mut(&m)
		if err := writeManifest(res.Folder, m); err != nil {
			t.Fatal(err)
		}
	}
	setPath := func(p string) func(*Manifest) {
		return func(m *Manifest) { m.Files[0].Path = p }
	}
	cases := map[string]func(*Manifest){
		"向上逃出":     setPath("../逃出.txt"),
		"中间有 ..":   setPath("config/../../逃出.txt"),
		"绝对路径":     setPath("/绝对.txt"),
		"盘符":       setPath("C:/盘符.txt"),
		"冒号（流）":    setPath("config/a.txt:流"),
		"反斜杠":      setPath(`config\a.txt`),
		"不规整":      setPath("config//a.txt"),
		"点开头的当前目录": setPath("./a.txt"),
		"恢复记录目录":   setPath(".restore/incomplete.json"),
		"保留名":      setPath("config/nul.txt"),
		"以点结尾":     setPath("config/a."),
		"空路径":      setPath(""),
		"控制字符":     setPath("config/a\x01.txt"),
		"重复路径":     func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) },
		"只有大小写不同的重复": func(m *Manifest) {
			dup := m.Files[0]
			dup.Path = strings.ToUpper(dup.Path)
			m.Files = append(m.Files, dup)
		},
		"哈希不是十六进制": func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("z", 64) },
		"哈希太短":     func(m *Manifest) { m.Files[0].SHA256 = "ab" },
		"大小是负数":    func(m *Manifest) { m.Files[0].Size = -1 },
		"没有任何文件":   func(m *Manifest) { m.Files = nil },
		"库不在文件列表里": func(m *Manifest) { m.DBs = append(m.DBs, DBEntry{Path: "go/不存在的库.db"}) },
	}
	beforeBase := treeHashes(t, f.base, "data")
	for name, mut := range cases {
		mutate(mut)
		to := filepath.Join(f.base, "恢复目录", "x")
		_, err := f.svc.Restore(context.Background(), res.Name, to)
		if !errors.Is(err, ErrManifestUnsafe) {
			t.Errorf("%s：想要 ErrManifestUnsafe，得到 %v", name, err)
		}
		if exists(filepath.Join(f.base, "恢复目录")) {
			t.Fatalf("%s：被拒绝了却建出了目标", name)
		}
	}
	// 还原清单：现在该能恢复
	if err := writeManifest(res.Folder, orig); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"逃出.txt", "绝对.txt"} {
		if exists(filepath.Join(f.base, p)) || exists(filepath.Join(filepath.Dir(f.base), p)) {
			t.Fatalf("写到了目标之外：%s", p)
		}
	}
	after := treeHashes(t, f.base, "data")
	for k, v := range after {
		if !strings.HasPrefix(k, "Sumika-备份/") && beforeBase[k] != v {
			t.Fatalf("多出或改了 %s", k)
		}
	}
}

func TestRestoreRowCountMismatch(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	rewriteManifest(t, res.Folder, func(m *Manifest) {
		i := dbIndex(t, *m, "go/chat/qq.db")
		m.DBs[i].Tables["events"] += 5
	})
	to := f.target("行数不对")
	rep, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrRestoreFailed) || rep.OK {
		t.Fatalf("行数对不上该失败：%v %+v", err, rep)
	}
	var chat DBCheck
	for _, d := range rep.DBs {
		if d.Path == "go/chat/qq.db" {
			chat = d
		}
	}
	if chat.Rows != RowsMismatch || len(chat.Mismatch) == 0 || !strings.Contains(chat.Mismatch[0], "events") {
		t.Fatalf("该点名库和表：%+v", chat)
	}
	if mk := readMarker(t, to); mk.State != "failed" {
		t.Fatalf("%+v", mk)
	}
}

// 旧备份没记行数：如实说，不记成失败，也不记成“行数核对通过”
func TestRestoreUnrecordedRows(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	rewriteManifest(t, res.Folder, func(m *Manifest) {
		for i := range m.DBs {
			m.DBs[i].Tables = nil
		}
	})
	to := f.target("没记行数")
	rep, err := f.svc.Restore(context.Background(), res.Name, to)
	if err != nil || !rep.OK {
		t.Fatalf("没记行数的旧备份其余都对时该通过：%v %+v", err, rep)
	}
	if rep.RowsRecorded || !strings.Contains(strings.Join(rep.Notes, "；"), "这份备份没记行数，只核对了文件和完整性") {
		t.Fatalf("该如实说没记行数：%+v", rep)
	}
	for _, d := range rep.DBs {
		if d.Rows != RowsUnrecorded {
			t.Errorf("库 %s 的行数核对该是 unrecorded：%+v", d.Path, d)
		}
	}
}

// 哈希是对的，但库本身是坏的（备份时就坏了）：核对不过
func TestRestoreDBIntegrityFails(t *testing.T) {
	f := newFx(t)
	res := f.bigBackup()
	victim := filepath.Join(res.Folder, "data", "go", "chat", "qq.db")
	b, err := os.ReadFile(victim)
	if err != nil || len(b) < 4096*6 {
		t.Fatalf("测试库太小，没法坏掉中间的页：%d %v", len(b), err)
	}
	for i := 4096 * 2; i < 4096*4; i++ {
		b[i] = 0xAB
	}
	if err := os.WriteFile(victim, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, sum, _ := sha256File(victim)
	rewriteManifest(t, res.Folder, func(m *Manifest) { // 让清单里的哈希配得上坏掉的库，只剩库本身的检查能发现
		for i := range m.Files {
			if m.Files[i].Path == "go/chat/qq.db" {
				m.Files[i].SHA256 = sum
			}
		}
	})
	rep, err := f.svc.Restore(context.Background(), res.Name, f.target("库是坏的"))
	if !errors.Is(err, ErrRestoreFailed) || rep.OK {
		t.Fatalf("库坏了该失败：%v %+v", err, rep)
	}
	joined := strings.Join(rep.Problems, "；")
	if !strings.Contains(joined, "go/chat/qq.db") || !(strings.Contains(joined, "integrity_check") || strings.Contains(joined, "打不开") || strings.Contains(joined, "行数")) {
		t.Fatalf("问题里该点名这个库：%v", rep.Problems)
	}
}

func TestRestoreRefusesLinkedSource(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	cfg := filepath.Join(res.Folder, "data", "config")
	moved := filepath.Join(f.base, "搬走的config")
	if err := os.Rename(cfg, moved); err != nil {
		t.Fatal(err)
	}
	makeLink(t, moved, cfg) // 备份里的 config 变成指向别处的联接
	to := f.target("源是联接")
	_, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrRestoreFailed) || !strings.Contains(err.Error(), "config") {
		t.Fatalf("备份里的目录是联接该失败：%v", err)
	}
	if exists(filepath.Join(to, "config", "sumika.yml")) {
		t.Fatal("跟进联接把别处的文件读出来了")
	}
}

func TestRestoreRefusesWhenSpaceShort(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	f.svc.free = func(string) (int64, bool) { return 1 << 20, true }
	to := f.target("空间不够")
	if _, err := f.svc.Restore(context.Background(), res.Name, to); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("%v", err)
	}
	if exists(to) {
		t.Fatal("空间不够被拒绝了，目标却被建出来了")
	}
	// 查不出剩余空间的文件系统不拦
	f.svc.free = func(string) (int64, bool) { return 0, false }
	if rep, err := f.svc.Restore(context.Background(), res.Name, to); err != nil || !rep.OK {
		t.Fatalf("查不出空间不该拦：%v", err)
	}
}

func TestRestoreRefusesLongPath(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	f.svc.maxPathLen = len(f.base) + 20
	to := f.target("长路径")
	_, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrTargetUnsafe) || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("路径太长该在开工前拒绝：%v", err)
	}
	if exists(to) {
		t.Fatal("被拒绝了，目标却被建出来了")
	}
}

// 复制到一半出错（盘满）：停下，标记写 failed 并留着，已写出的留着，目录不能当数据目录
func TestRestoreCopyErrorLeavesIncompleteMarker(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	calls := 0
	real := f.svc.restoreCopy
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		calls++
		if calls == 3 {
			return 0, "", errors.New("There is not enough space on the disk")
		}
		return real(ctx, src, dst)
	}
	to := f.target("盘满")
	rep, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrRestoreFailed) || rep.OK {
		t.Fatalf("%v", err)
	}
	if mk := readMarker(t, to); mk.State != "failed" || !strings.Contains(mk.Reason, "not enough space") {
		t.Fatalf("标记该写明原因：%+v", mk)
	}
	if _, err := paths.Open(to); !errors.Is(err, paths.ErrRestoreIncomplete) {
		t.Fatalf("%v", err)
	}
}

// 标记在第一个文件之前就写好了：进程在复制中途被杀，留下的目录也不能当数据目录
func TestRestoreMarkerWrittenBeforeFiles(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	to := f.target("标记先写")
	real := f.svc.restoreCopy
	checked := false
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		if !checked {
			checked = true
			if mk := readMarker(t, to); mk.State != "restoring" {
				t.Errorf("第一个文件复制前标记就该是 restoring：%+v", mk)
			}
			if _, err := paths.Open(to); !errors.Is(err, paths.ErrRestoreIncomplete) {
				t.Errorf("复制中途的目录不能当数据目录：%v", err)
			}
		}
		return real(ctx, src, dst)
	}
	if _, err := f.svc.Restore(context.Background(), res.Name, to); err != nil || !checked {
		t.Fatalf("%v %v", err, checked)
	}
}

func TestRestoreCancelLeavesMarker(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	real := f.svc.restoreCopy
	calls := 0
	f.svc.restoreCopy = func(c context.Context, src, dst string) (int64, string, error) {
		calls++
		n, sum, err := real(c, src, dst)
		if calls == 2 {
			cancel()
		}
		return n, sum, err
	}
	to := f.target("被取消")
	backupBefore := treeHashes(t, res.Folder)
	_, err := f.svc.Restore(ctx, res.Name, to)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("被取消该回取消：%v", err)
	}
	if mk := readMarker(t, to); mk.State != "cancelled" {
		t.Fatalf("%+v", mk)
	}
	if _, err := paths.Open(to); !errors.Is(err, paths.ErrRestoreIncomplete) {
		t.Fatalf("%v", err)
	}
	if !sameTree(backupBefore, treeHashes(t, res.Folder)) {
		t.Fatal("取消的恢复动了备份")
	}
}

// 恢复、备份、抽验共用一把锁：同时来两个、恢复遇到备份、备份遇到恢复，后到的得 ErrBusy
func TestRestoreBusy(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	entered := make(chan struct{})
	release := make(chan struct{})
	real := f.svc.restoreCopy
	var once = make(chan struct{}, 1)
	once <- struct{}{}
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		select {
		case <-once:
			close(entered)
			<-release
		default:
		}
		return real(ctx, src, dst)
	}
	done, err := f.svc.GoRestore(res.Name, f.target("第一个恢复"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := f.svc.Restore(context.Background(), res.Name, f.target("第二个恢复")); !errors.Is(err, ErrBusy) {
		t.Errorf("第二个恢复该得到 ErrBusy：%v", err)
	}
	if exists(f.target("第二个恢复")) {
		t.Error("ErrBusy 的恢复不该建目录")
	}
	if _, err := f.run(ReasonManual); !errors.Is(err, ErrBusy) {
		t.Errorf("恢复进行时备份该得到 ErrBusy（轮换在备份里，所以也不会同时跑）：%v", err)
	}
	if _, err := f.svc.Go(Request{Reason: ReasonManual}); !errors.Is(err, ErrBusy) {
		t.Errorf("%v", err)
	}
	close(release)
	if out := <-done; out.Err != nil || !out.Report.OK {
		t.Fatalf("第一个恢复该做完：%+v", out)
	}
	// 反过来：备份进行时恢复得 ErrBusy
	blockCopy := make(chan struct{})
	started := make(chan struct{})
	realCopy := f.svc.copyFile
	var one = make(chan struct{}, 1)
	one <- struct{}{}
	f.svc.copyFile = func(ctx context.Context, src, dst string) (int64, string, error) {
		select {
		case <-one:
			close(started)
			<-blockCopy
		default:
		}
		return realCopy(ctx, src, dst)
	}
	ch, err := f.svc.Go(Request{Reason: ReasonManual})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := f.svc.Restore(context.Background(), res.Name, f.target("备份时恢复")); !errors.Is(err, ErrBusy) {
		t.Errorf("备份进行时恢复该得到 ErrBusy：%v", err)
	}
	close(blockCopy)
	if o := <-ch; o.Err != nil {
		t.Fatal(o.Err)
	}
}

// 关机：Close 取消正在做的恢复，不等满期限
func TestRestoreCloseCancels(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	entered := make(chan struct{})
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		close(entered)
		<-ctx.Done()
		return 0, "", ctx.Err()
	}
	to := f.target("关机时")
	done, err := f.svc.GoRestore(res.Name, to)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	f.svc.Close()
	out := <-done
	if !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("关机时该回取消：%v", out.Err)
	}
	if mk := readMarker(t, to); mk.State != "cancelled" {
		t.Fatalf("%+v", mk)
	}
	if _, err := f.svc.Restore(context.Background(), res.Name, f.target("关机后")); !errors.Is(err, ErrClosed) {
		t.Fatalf("关了以后该回 ErrClosed：%v", err)
	}
}

// 路径长度按 UTF-16 单位数（Windows 的算法），不按 UTF-8 字节：一个中文字是 1 不是 3。
// 备份自己写得出的长中文路径，抽验不能因为算法不一样一直失败
func TestRestoreLongPathCountsUTF16Units(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	to := f.target("恢复出的数据目录")
	root, err := realExisting(to)
	if err != nil {
		t.Fatal(err)
	}
	longest, longestBytes := 0, 0
	for _, e := range m.Files {
		if n := pathUnits(e.Path); n > longest {
			longest = n
		}
		if n := len(e.Path); n > longestBytes {
			longestBytes = n
		}
	}
	need := pathUnits(root) + 1 + longest
	if len(root)+1+longestBytes <= need {
		t.Fatalf("这条测试要有区分力：按字节算该比按 UTF-16 单位算更长（%d 对 %d）", len(root)+1+longestBytes, need)
	}
	f.svc.maxPathLen = need - 1
	if _, err := f.svc.Restore(context.Background(), res.Name, to); !errors.Is(err, ErrTargetUnsafe) || exists(to) {
		t.Fatalf("少一个单位就该拒绝：%v", err)
	}
	f.svc.maxPathLen = need
	if rep, err := f.svc.Restore(context.Background(), res.Name, to); err != nil || !rep.OK {
		t.Fatalf("正好够的时候该能恢复（中文路径按 UTF-16 单位算，不按字节）：%v %+v", err, rep.Problems)
	}
}

// 核对这一层是独立的：复制那一步报告“写对了”，写出去的东西后来变了（杀毒软件、盘坏、别的进程），核对层照样要发现。
// 篡改备份文件的测试都在复制那一步就被拦下，守不到这一层
func TestRestoreVerifyLayerCatchesCorruptionAfterCopy(t *testing.T) {
	for name, damage := range map[string]func(dst string) error{
		"改一个字节（哈希不对）": func(dst string) error {
			b, err := os.ReadFile(dst)
			if err != nil || len(b) == 0 {
				return errors.New("没法改")
			}
			b[0] ^= 0xff
			return os.WriteFile(dst, b, 0o644)
		},
		"多一个字节（大小不对）": func(dst string) error {
			fh, err := os.OpenFile(dst, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			defer fh.Close()
			_, err = fh.Write([]byte{'x'})
			return err
		},
		"删掉（缺文件）": func(dst string) error { return os.Remove(dst) },
	} {
		f := newFx(t)
		res := f.mustRun(ReasonManual)
		real := f.svc.restoreCopy
		var victim string
		f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
			n, sum, err := real(ctx, src, dst)
			if err == nil && victim == "" && strings.HasSuffix(filepath.ToSlash(dst), "config/mcp.json") {
				victim = dst
				if derr := damage(dst); derr != nil {
					t.Errorf("%s：弄坏不了：%v", name, derr)
				}
			}
			return n, sum, err // 照样报“写对了”
		}
		to := f.target("复制之后坏了")
		rep, err := f.svc.Restore(context.Background(), res.Name, to)
		if victim == "" {
			t.Fatalf("%s：没碰到要弄坏的文件", name)
		}
		if !errors.Is(err, ErrRestoreFailed) || rep.OK || len(rep.Problems) == 0 || !strings.Contains(strings.Join(rep.Problems, "；"), "config/mcp.json") {
			t.Errorf("%s：核对层该发现并点名文件：%v %+v", name, err, rep.Problems)
		}
		if mk := readMarker(t, to); mk.State != "failed" {
			t.Errorf("%s：标记该是 failed：%+v", name, mk)
		}
		if _, err := paths.Open(to); !errors.Is(err, paths.ErrRestoreIncomplete) {
			t.Errorf("%s：没通过核对的目录不能当数据目录：%v", name, err)
		}
	}
}

// 数据目录的上级、备份目录的上级各自一格：备份目录放在别处时，数据目录的上级不是备份目录的上级，
// 这一道检查不会被“备份目录的上级”那一道顶替；上级必然不是空的，也不会被“非空”碰巧拦住（拦的原因要对）
func TestRestoreTargetParentOfDataOnly(t *testing.T) {
	elsewhere := filepath.Join(t.TempDir(), "别处的备份")
	f := newFx(t, func(o *Options) { o.Dir = elsewhere })
	res := f.mustRun(ReasonManual)
	_, err := f.svc.Restore(context.Background(), res.Name, f.base)
	if !errors.Is(err, ErrTargetUnsafe) || !strings.Contains(err.Error(), "是数据目录") || !strings.Contains(err.Error(), "的上级") {
		t.Fatalf("数据目录的上级该因为“数据目录的上级”被拒绝：%v", err)
	}
	// 数据目录的上级的上级也一样
	if _, err := f.svc.Restore(context.Background(), res.Name, filepath.Dir(f.base)); !errors.Is(err, ErrTargetUnsafe) || !strings.Contains(err.Error(), "上级") {
		t.Fatalf("%v", err)
	}
}

// 检查到建立之间有人把目标换成通往数据目录的联接：建完目录以后再核对一遍，不对就一个字都不写
func TestRestoreTargetSwappedAfterCheck(t *testing.T) {
	f := newFx(t)
	res := f.mustRun(ReasonManual)
	to := f.target("检查之后被换掉")
	f.svc.free = func(string) (int64, bool) { // 检查目标之后、建目录之前会问一次剩余空间：就在这个空档里把目标换掉
		makeLink(t, f.data, to)
		return 1 << 40, true
	}
	_, err := f.svc.Restore(context.Background(), res.Name, to)
	if !errors.Is(err, ErrTargetUnsafe) || !strings.Contains(err.Error(), "变了") {
		t.Fatalf("目标被换成联接该在建好以后查出来：%v", err)
	}
	if exists(filepath.Join(f.data, paths.RestoreDir)) {
		t.Fatal("数据目录里被写进了恢复记录")
	}
}
