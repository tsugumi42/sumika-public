package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenNeedsMeta(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), MetaFile) {
		t.Fatalf("没有身份证的目录应该报错并提到 %s，得到 %v", MetaFile, err)
	}
	// 只读打开不写文件
	if _, err := os.Stat(filepath.Join(dir, MetaFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Open 不该写身份证")
	}
}

func TestOpenLegacy(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "shared"), 0o755)
	os.WriteFile(filepath.Join(dir, "shared", "events.db"), nil, 0o644)
	if _, err := Open(dir); !errors.Is(err, ErrLegacy) {
		t.Fatalf("旧布局应该报 ErrLegacy，得到 %v", err)
	}
}

func TestInitAndNewerFormat(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"format": 1`) || strings.Contains(string(b), "minReaderFormat") {
		t.Fatalf("新建只写 format，不写和它一样的 minReaderFormat：%s", b)
	}
	os.WriteFile(filepath.Join(dir, MetaFile), []byte(`{"format":2}`), 0o644)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "第 2 版") {
		t.Fatalf("比程序新的格式应该拒绝，得到 %v", err)
	}
	for _, bad := range []string{`{}`, `{"format":0}`, `{"format":-1}`, `{"format":1,"minReaderFormat":2}`, `{"format":2,"minReaderFormat":3}`} {
		os.WriteFile(filepath.Join(dir, MetaFile), []byte(bad), 0o644)
		if _, err := Open(dir); err == nil {
			t.Fatalf("身份证 %s 应该拒绝", bad)
		}
	}
}

// 格式号按区间读：format 可以比程序新，只要 minReaderFormat 不超过程序的 DataFormat；拒绝时文件一个字节都不改。
func TestOpenFormatRange(t *testing.T) {
	tests := []struct {
		name    string
		meta    string
		open    bool
		wantErr string
	}{
		{name: "format1", meta: `{"format":1}`, open: true},
		{name: "format1-min1", meta: `{"format":1,"minReaderFormat":1}`, open: true},
		{name: "format1-min2", meta: `{"format":1,"minReaderFormat":2}`, wantErr: "改坏"},
		{name: "format1-min3", meta: `{"format":1,"minReaderFormat":3}`, wantErr: "改坏"},
		{name: "format2", meta: `{"format":2}`, wantErr: "升级程序"},
		{name: "format2-min1", meta: `{"format":2,"minReaderFormat":1}`, open: true},
		{name: "format2-min2", meta: `{"format":2,"minReaderFormat":2}`, wantErr: "升级程序"},
		{name: "format2-min3", meta: `{"format":2,"minReaderFormat":3}`, wantErr: "改坏"},
		{name: "format3-min1", meta: `{"format":3,"minReaderFormat":1}`, open: true},
		{name: "format0", meta: `{"format":0}`, wantErr: "改坏"},
		{name: "format0-min1", meta: `{"format":0,"minReaderFormat":1}`, wantErr: "改坏"},
		{name: "format-1", meta: `{"format":-1}`, wantErr: "改坏"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, MetaFile)
			if err := os.WriteFile(file, []byte(tt.meta), 0o644); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			_, openErr := Open(dir)
			if tt.open && openErr != nil {
				t.Fatalf("%s 该能打开：%v", tt.meta, openErr)
			}
			if !tt.open && (openErr == nil || !strings.Contains(openErr.Error(), tt.wantErr)) {
				t.Fatalf("%s 该拒绝并提到 %q，得到 %v", tt.meta, tt.wantErr, openErr)
			}
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("Open 改了身份证：%q -> %q", before, after)
			}
		})
	}
}

// 共用记忆库在 shared/ 下，文件名不变（不搬家）；它不是 Bun 的旧库，所以不和 BunEventsDB、BunTraceDB 混
func TestOwnerMemoryDBPath(t *testing.T) {
	p := Paths{Root: filepath.Join("d", "data")}
	if got, want := p.OwnerMemoryDB(), filepath.Join("d", "data", "shared", "owner-memory.db"); got != want {
		t.Fatalf("共用记忆库路径 %q，要 %q", got, want)
	}
}

// 恢复成新目录做到一半（或没通过核对）的目录：备份里带着 sumika-data.json，没有标记检查它照样能被打开
func TestOpenRefusesIncompleteRestore(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatalf("没有标记时该能打开：%v", err)
	}
	marker := filepath.Join(dir, RestoreDir, RestoreIncompleteFile)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(`{"state":"restoring"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrRestoreIncomplete) {
		t.Fatalf("带未完成标记的目录该报 ErrRestoreIncomplete，得到 %v", err)
	}
	if _, err := Init(dir); !errors.Is(err, ErrRestoreIncomplete) {
		t.Fatalf("Init 也不该把它当成能用的目录：%v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatalf("标记删掉以后该能打开：%v", err)
	}
}

// ErrUninitialized 只在两种情况下返回：目录不存在；目录存在但顶层只有 config/、characters/、models/。
// 有数据、只是丢了身份证的目录，旧布局、没恢复完、身份证读坏的目录都照旧报原来的错，OpenOrInit 一个文件都不写。
func TestOpenOrInitOnlyForUninitialized(t *testing.T) {
	snapshot := func(root string) map[string]string {
		got := map[string]string{}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if d.IsDir() {
				got[rel] = "<dir>"
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			got[rel] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	equal := func(a, b map[string]string) bool {
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

	t.Run("目录不存在", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "新目录")
		if _, err := Open(dir); !errors.Is(err, ErrUninitialized) {
			t.Fatalf("不存在的目录该报 ErrUninitialized，得到 %v", err)
		}
		if p, initialized, err := OpenOrInit(dir); err != nil {
			t.Fatal(err)
		} else if !initialized || p.Root == "" {
			t.Fatalf("OpenOrInit 该报告这次初始化了：initialized=%v root=%q", initialized, p.Root)
		}
		if _, initialized, err := OpenOrInit(dir); err != nil || initialized {
			t.Fatalf("已经初始化的目录不该再报初始化：initialized=%v err=%v", initialized, err)
		}
		if _, err := os.Stat(filepath.Join(dir, MetaFile)); err != nil {
			t.Fatalf("OpenOrInit 该写身份证：%v", err)
		}
	})

	t.Run("只有白名单", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"config", "characters", "models"} {
			if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		card := filepath.Join(dir, "characters", "example", "card", "character.yml")
		if err := os.MkdirAll(filepath.Dir(card), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(card, []byte("名字：编的"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrUninitialized) {
			t.Fatalf("只有白名单的目录该报 ErrUninitialized，得到 %v", err)
		}
		if _, initialized, err := OpenOrInit(dir); err != nil {
			t.Fatal(err)
		} else if !initialized {
			t.Fatal("只有白名单的目录该报告这次初始化了")
		}
		if _, err := os.Stat(card); err != nil {
			t.Fatalf("初始化不该删角色卡：%v", err)
		}
	})

	t.Run("有别的就不算没初始化", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			set  func(t *testing.T, dir string)
			want error
		}{
			{"go 目录", func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
					t.Fatal(err)
				}
			}, nil},
			{"任何 db", func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "qq.db"), []byte("编的"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, nil},
			{"logs", func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
					t.Fatal(err)
				}
			}, nil},
			{"旧布局", func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "shared"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "shared", "events.db"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}, ErrLegacy},
			{"没恢复完", func(t *testing.T, dir string) {
				marker := filepath.Join(dir, RestoreDir, RestoreIncompleteFile)
				if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte(`{"state":"restoring"}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}, ErrRestoreIncomplete},
			{"身份证读坏", func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte(`{"format":`), 0o644); err != nil {
					t.Fatal(err)
				}
			}, nil},
		} {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				tt.set(t, dir)
				before := snapshot(dir)
				_, openErr := Open(dir)
				if openErr == nil {
					t.Fatal("该报错")
				}
				if errors.Is(openErr, ErrUninitialized) {
					t.Fatalf("不该当成没初始化：%v", openErr)
				}
				if tt.want != nil && !errors.Is(openErr, tt.want) {
					t.Fatalf("错误 %v，要 %v", openErr, tt.want)
				}
				_, initialized, initErr := OpenOrInit(dir)
				if initErr == nil {
					t.Fatal("OpenOrInit 该原样报错，不能初始化")
				}
				if initialized {
					t.Fatal("OpenOrInit 报错时不该说初始化了")
				}
				if errors.Is(initErr, ErrUninitialized) {
					t.Fatalf("OpenOrInit 报的也不该是 ErrUninitialized：%v", initErr)
				}
				if after := snapshot(dir); !equal(before, after) {
					t.Fatalf("OpenOrInit 写了文件：之前 %v，之后 %v", before, after)
				}
			})
		}
	})
}

// 系统自己生成的文件不算数据：desktop.ini、Thumbs.db、.DS_Store（只认这三个确切的名字，不分大小写）。
// Thumbs.db 是唯一的 .db 例外，别的 .db 照旧挡住初始化。
func TestOpenIgnoresSystemFiles(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "Thumbs.db")
	for _, name := range []string{"desktop.ini", "Thumbs.db", ".DS_Store"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("编的"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("只有系统文件时该报 ErrUninitialized，得到 %v", err)
	}
	if _, initialized, err := OpenOrInit(dir); err != nil || !initialized {
		t.Fatalf("只有系统文件时该初始化：initialized=%v err=%v", initialized, err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("初始化不该删系统文件：%v", err)
	}

	dir = t.TempDir()
	for _, name := range []string{"Thumbs.db", "notes.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("编的"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); err == nil || errors.Is(err, ErrUninitialized) || !strings.Contains(err.Error(), "notes.db") {
		t.Fatalf("Thumbs.db 之外的 .db 该算有数据并点名 notes.db，得到 %v", err)
	}

	// 名叫 Thumbs.db、desktop.ini、.DS_Store 的目录不算系统文件：里面可能装着数据
	for _, name := range []string{"Thumbs.db", "desktop.ini", ".DS_Store"} {
		dir = t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil || errors.Is(err, ErrUninitialized) || !strings.Contains(err.Error(), name+"/") {
			t.Fatalf("名叫 %s 的目录该算有数据并点名它，得到 %v", name, err)
		}
		if _, _, err := OpenOrInit(dir); err == nil || errors.Is(err, ErrUninitialized) {
			t.Fatalf("名叫 %s 的目录不该被初始化，得到 %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, MetaFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("名叫 %s 的目录不该被写身份证：%v", name, err)
		}
	}
}

// Windows 的文件名不分大小写：Config、CONFIG 也算 config。
func TestOpenWindowsWhitelistCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("文件名不分大小写只适用于 Windows")
	}
	for _, name := range []string{"Config", "CONFIG", "Models"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrUninitialized) {
				t.Fatalf("%s 该算 config/models 白名单，得到 %v", name, err)
			}
			if _, initialized, err := OpenOrInit(dir); err != nil || !initialized {
				t.Fatalf("%s 的白名单目录该初始化：initialized=%v err=%v", name, initialized, err)
			}
		})
	}
}

// 算有数据时错误要点名第一个挡住的条目（目录带 /），不是只说“没有身份证”。
func TestOpenNamesFirstBlocker(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qq.db"), []byte("编的"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir)
	if err == nil || errors.Is(err, ErrUninitialized) {
		t.Fatalf("有 go/ 的目录不该算没初始化：%v", err)
	}
	if !strings.Contains(err.Error(), "go/") || !strings.Contains(err.Error(), "不是空的新目录") {
		t.Fatalf("错误该点名 go/ 且说明不是空的新目录：%v", err)
	}
}
