package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nativeManifestSample = `{"program":"sumika-native-backup","format":1,"name":"x","files":[]}`
const bunManifestSample = `{"kind":"sumika-backup","version":1,"app":"sumika","files":[{"path":"a","size":1}]}`

// 数据目录里放着的别的备份不带（换 exe 时留的手动备份、别处拷来的备份文件夹），每天和手动备份一样；清单 notes 记跳过了哪些、多大。
// 别的东西照常带：插件的 manifest.json、角色目录里叫 backup 的子目录、名字像但不是的文件
func TestRunSkipsBackupsInsideDataDir(t *testing.T) {
	for _, reason := range []Reason{ReasonDaily, ReasonManual} {
		t.Run(string(reason), func(t *testing.T) {
			f := newFx(t)
			big := strings.Repeat("x", 2<<20)
			write(t, filepath.Join(f.data, "backup", "swap-20261113-0245", "big.bin"), big)
			write(t, filepath.Join(f.data, "backup", "swap-20261113-0245", "characters", "a", "memory.db"), "不是真库")
			write(t, filepath.Join(f.data, "go", "backup-swap-20261111-2146", "trace.db"), "不是真库")
			write(t, filepath.Join(f.data, "go", "backup-swap-20261111-2146", "sumika.exe"), "exe")
			write(t, filepath.Join(f.data, "go", "别人拷来的", "manifest.json"), nativeManifestSample)
			write(t, filepath.Join(f.data, "go", "别人拷来的", "data", "x.txt"), "x")
			write(t, filepath.Join(f.data, "reports", "旧版备份", "manifest.json"), bunManifestSample)
			write(t, filepath.Join(f.data, "reports", "旧版备份", "data", "y.txt"), "y")
			// 要带的
			write(t, filepath.Join(f.data, "plugins", "p", "manifest.json"), `{"name":"p","kind":"tool"}`)
			write(t, filepath.Join(f.data, "characters", "a", "backup", "n.md"), "她自己的叫 backup 的目录")
			write(t, filepath.Join(f.data, "go", "backups-keep.txt"), "名字像但不是")
			write(t, filepath.Join(f.data, "go", "backup-list.md"), "也不是 backup-swap-")
			res := f.mustRun(reason)
			m, err := ReadManifest(res.Folder)
			if err != nil || m.Status != StatusOK {
				t.Fatalf("%v %v", err, m.Problems)
			}
			for _, f := range m.Files {
				for _, bad := range []string{"backup/", "backups/", "go/backup-swap-", "go/别人拷来的/", "reports/旧版备份/"} {
					if strings.HasPrefix(f.Path, bad) {
						t.Errorf("数据目录里的备份不该带：%s", f.Path)
					}
				}
			}
			for _, rel := range []string{"plugins/p/manifest.json", "characters/a/backup/n.md", "go/backups-keep.txt", "go/backup-list.md"} {
				if _, ok := fileOf(m, rel); !ok {
					t.Errorf("%s 不是备份，该照常带", rel)
				}
			}
			note := ""
			for _, n := range m.Notes {
				if strings.Contains(n, "没带") && strings.Contains(n, "是备份") {
					note = n
				}
			}
			if note == "" {
				t.Fatalf("notes 该记一句跳过了哪些备份：%v", m.Notes)
			}
			for _, want := range []string{"backup（2.0 MB）", "go/backup-swap-20261111-2146", "go/别人拷来的", "reports/旧版备份", "合计"} {
				if !strings.Contains(note, want) {
					t.Errorf("notes 里该有 %q：%s", want, note)
				}
			}
			if strings.Contains(strings.Join(m.Notes, "；"), "不是 SQLite") {
				t.Errorf("备份里的假库不该被碰到：%v", m.Notes)
			}
		})
	}
}

// 备份很多份时 notes 只点名前几处，再给总数；没有这类备份时不写这一句
func TestSkippedBackupNoteIsBoundedAndAbsentWhenNone(t *testing.T) {
	f := newFx(t)
	if err := os.RemoveAll(f.p.BackupsDir()); err != nil { // 夹具里放了一个旧版的 backups/
		t.Fatal(err)
	}
	res := f.mustRun(ReasonManual)
	m, _ := ReadManifest(res.Folder)
	for _, n := range m.Notes {
		if strings.Contains(n, "是备份") {
			t.Errorf("没有这类备份时不该有这句：%s", n)
		}
	}
	var list []skippedBackup
	for i := 0; i < 12; i++ {
		list = append(list, skippedBackup{rel: "go/backup-swap-" + string(rune('a'+i)), bytes: 230 << 20})
	}
	note := skippedBackupNote(list)
	if !strings.Contains(note, "等共 12 处") || strings.Count(note, "backup-swap-") != 5 || !strings.Contains(note, "2.70 GB") {
		t.Fatalf("%s", note)
	}
}

func TestSniffBackupManifest(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"本程序的":            {nativeManifestSample, true},
		"旧 Bun 的":         {bunManifestSample, true},
		"标记不在第一个键":        {`{"a":1,"b":[1,2,{"c":3}],"program":"sumika-native-backup"}`, true},
		"插件清单":            {`{"name":"p","kind":"tool","version":"1"}`, false},
		"program 值不对":     {`{"program":"别的","kind":"别的"}`, false},
		"program 不是字符串":   {`{"program":5}`, false},
		"顶层是数组":           {`[{"program":"sumika-native-backup"}]`, false},
		"不是 json":         {`program: sumika-native-backup`, false},
		"空对象":             {`{}`, false},
		"截断":              {`{"a":"` + strings.Repeat("x", 100), false},
		"标记只在嵌套里":         {`{"x":{"program":"sumika-native-backup"}}`, false},
		"标记在 64 个键以后（不认）": {manyKeys(70) + `"program":"sumika-native-backup"}`, false},
	} {
		if got := sniffBackupManifest(strings.NewReader(tc.body)); got != tc.want {
			t.Errorf("%s：想要 %v 得到 %v", name, tc.want, got)
		}
	}
}

func manyKeys(n int) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < n; i++ {
		b.WriteString(`"k` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":1,`)
	}
	return b.String()
}

// backupNamed 的边界：只认顶层的 backup、backups 和 go/ 下 backup-swap- 开头的；名字相近的、别处的同名目录不认
func TestBackupNamedBoundaries(t *testing.T) {
	for rel, want := range map[string]bool{
		"backup":                       true,
		"backup/swap-1/a.db":           true,
		"backups":                      true,
		"backups/x":                    true,
		"Backup/x":                     isWindows(),
		"go/backup-swap-1":             true,
		"go/backup-swap-1/trace.db":    true,
		"go/backup-swap-":              true,
		"go/backups-x":                 false,
		"go/backup-list.md":            false,
		"backup-swap-1":                false,
		"characters/a/backup/n.md":     false,
		"plugins/backup/x":             false,
		"go/sub/backup-swap-1":         false,
		"backupx/a":                    false,
		"sumika-data.json":             false,
		"go/media/qq/ab/backup-swap-1": false,
	} {
		if got := backupNamed(rel); got != want {
			t.Errorf("%s：想要 %v 得到 %v", rel, want, got)
		}
	}
}
