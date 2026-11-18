package app

// P2-1 第 2 步：全新目录初始化 + 示例卡复制（规格-不带QQ也能用 设计第 3 条）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func appTestRoot(t *testing.T) string {
	t.Helper()
	root, err := config.FindRoot("../..")
	if err != nil {
		t.Fatalf("找项目根（在 native/internal/app 里跑）：%v", err)
	}
	return root
}

// 这次刚初始化的目录、一张卡都没有：复制示例卡；已经有卡就不动；不是这次初始化的目录什么都不做。
func TestEnsureExampleCharacter(t *testing.T) {
	t.Parallel()
	root := appTestRoot(t)
	t.Run("全新目录复制", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureExampleCharacter(p, Options{Root: root}, true); err != nil {
			t.Fatal(err)
		}
		for _, name := range exampleCardFiles {
			got, err := os.ReadFile(filepath.Join(data, "characters", "example", "card", name))
			if err != nil {
				t.Fatalf("没复制 %s：%v", name, err)
			}
			want, err := os.ReadFile(filepath.Join(root, "characters", "example", name))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s 内容不对：%q", name, got)
			}
		}
	})
	t.Run("已有卡不复制也不覆盖", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		card := filepath.Join(data, "characters", "mine", "card")
		if err := os.MkdirAll(card, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(card, "character.yml"), []byte("name: 编的角色\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := EnsureExampleCharacter(p, Options{Root: root}, true); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(data, "characters", "example")); !os.IsNotExist(err) {
			t.Fatalf("已经有卡不该复制示例卡：%v", err)
		}
		if b, err := os.ReadFile(filepath.Join(card, "character.yml")); err != nil || string(b) != "name: 编的角色\n" {
			t.Fatalf("已有卡被改了：%q %v", b, err)
		}
	})
	t.Run("不是这次初始化的目录不复制", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureExampleCharacter(p, Options{Root: root}, false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(data, "characters", "example")); !os.IsNotExist(err) {
			t.Fatalf("不是这次初始化的目录不该复制：%v", err)
		}
	})
	t.Run("资源根没有示例卡时给出路径", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		err = EnsureExampleCharacter(p, Options{Root: t.TempDir()}, true)
		if err == nil || !strings.Contains(err.Error(), "把角色卡放进") || !strings.Contains(err.Error(), filepath.Join(p.CharactersDir(), "<名字>", "card")) {
			t.Fatalf("该告诉主人把卡放哪：%v", err)
		}
		if _, statErr := os.Stat(filepath.Join(data, "characters", "example")); !os.IsNotExist(statErr) {
			t.Fatalf("报错时不该留下半份示例卡：%v", statErr)
		}
	})
	t.Run("链接源不复制", func(t *testing.T) {
		src := t.TempDir()
		cardDir := filepath.Join(src, "characters", "example")
		if err := os.MkdirAll(cardDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(src, "真的卡.yml")
		if err := os.WriteFile(target, []byte("name: 编的\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cardDir, "character.md"), []byte("# 编的\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(cardDir, "character.yml")); err != nil {
			t.Skipf("这台机器建不了符号链接：%v", err)
		}
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		err = EnsureExampleCharacter(p, Options{Root: src}, true)
		if err == nil || !strings.Contains(err.Error(), "不是普通文件") {
			t.Fatalf("链接源该被拒：%v", err)
		}
		if _, statErr := os.Stat(filepath.Join(data, "characters", "example")); !os.IsNotExist(statErr) {
			t.Fatalf("链接源被拒时不该留下东西：%v", statErr)
		}
	})
	t.Run("超过大小上限不复制", func(t *testing.T) {
		src := t.TempDir()
		cardDir := filepath.Join(src, "characters", "example")
		if err := os.MkdirAll(cardDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cardDir, "character.yml"), make([]byte, exampleCardMaxSize+1), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cardDir, "character.md"), []byte("# 编的\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		err = EnsureExampleCharacter(p, Options{Root: src}, true)
		if err == nil || !strings.Contains(err.Error(), "超过示例卡上限") {
			t.Fatalf("超大的源该被拒：%v", err)
		}
		if _, statErr := os.Stat(filepath.Join(data, "characters", "example")); !os.IsNotExist(statErr) {
			t.Fatalf("超大的源被拒时不该留下东西：%v", statErr)
		}
	})
	t.Run("中途写失败不留东西、下次能补上", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		old := exampleCardWriteFile
		calls := 0
		exampleCardWriteFile = func(file string, b []byte, mode os.FileMode) error {
			calls++
			if calls == 2 {
				return errors.New("编的写失败")
			}
			return old(file, b, mode)
		}
		t.Cleanup(func() { exampleCardWriteFile = old })
		if err := EnsureExampleCharacter(p, Options{Root: root}, true); err == nil || !strings.Contains(err.Error(), "编的写失败") {
			t.Fatalf("第二次写该失败：%v", err)
		}
		if _, statErr := os.Stat(filepath.Join(data, "characters", "example")); !os.IsNotExist(statErr) {
			t.Fatalf("失败后不该留下 example：%v", statErr)
		}
		for _, dir := range []string{data, p.CharactersDir()} {
			left, err := filepath.Glob(filepath.Join(dir, ".example-tmp-*"))
			if err != nil || len(left) != 0 {
				t.Fatalf("失败后 %s 下不该留下临时目录：%v %v", dir, left, err)
			}
		}
		exampleCardWriteFile = old
		if err := EnsureExampleCharacter(p, Options{Root: root}, true); err != nil {
			t.Fatalf("下次该能补上：%v", err)
		}
		if _, err := os.Stat(filepath.Join(data, "characters", "example", "card", "character.yml")); err != nil {
			t.Fatalf("补上的卡不对：%v", err)
		}
	})
	t.Run("临时目录建在角色目录外", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		old := exampleCardWriteFile
		calls := 0
		var before []string
		var beforeErr error
		exampleCardWriteFile = func(file string, b []byte, mode os.FileMode) error {
			calls++
			if err := old(file, b, mode); err != nil {
				return err
			}
			if calls == 1 {
				// 写完第一个文件、改名之前：临时目录不在 characters/，不该被当成有卡的角色
				before, beforeErr = characterIDsIn(p.CharactersDir())
			}
			return nil
		}
		t.Cleanup(func() { exampleCardWriteFile = old })
		if err := EnsureExampleCharacter(p, Options{Root: root}, true); err != nil {
			t.Fatal(err)
		}
		if beforeErr != nil || len(before) != 0 {
			t.Fatalf("改名之前的临时目录被当成角色了：%v %v", before, beforeErr)
		}
		entries, err := os.ReadDir(p.CharactersDir())
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) != 1 || names[0] != "example" {
			t.Fatalf("成功复制后 characters/ 下只该有 example：%v", names)
		}
		for _, dir := range []string{data, p.CharactersDir()} {
			left, err := filepath.Glob(filepath.Join(dir, ".example-tmp-*"))
			if err != nil || len(left) != 0 {
				t.Fatalf("成功后 %s 下不该留下临时目录：%v %v", dir, left, err)
			}
		}
	})
	t.Run("改名失败不留临时目录", func(t *testing.T) {
		data := t.TempDir()
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		old := exampleCardWriteFile
		calls := 0
		exampleCardWriteFile = func(file string, b []byte, mode os.FileMode) error {
			calls++
			if err := old(file, b, mode); err != nil {
				return err
			}
			if calls == 1 {
				// 第一个文件写完后把目标占成非空目录，让后面的改名失败
				blocker := filepath.Join(p.CharactersDir(), "example", "card")
				if err := os.MkdirAll(blocker, 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(blocker, "编的占位"), []byte("占位\n"), 0o644); err != nil {
					return err
				}
			}
			return nil
		}
		t.Cleanup(func() { exampleCardWriteFile = old })
		if err := EnsureExampleCharacter(p, Options{Root: root}, true); err == nil || !strings.Contains(err.Error(), "把示例卡放到") {
			t.Fatalf("改名该失败：%v", err)
		}
		for _, dir := range []string{data, p.CharactersDir()} {
			left, err := filepath.Glob(filepath.Join(dir, ".example-tmp-*"))
			if err != nil || len(left) != 0 {
				t.Fatalf("改名失败后 %s 下不该留下临时目录：%v %v", dir, left, err)
			}
		}
	})
}

// InitDataDir 只对没初始化的目录动手：写身份证、建 local/qq/trace 空库；第二次什么都不做。
func TestInitDataDir(t *testing.T) {
	t.Parallel()
	root := appTestRoot(t)
	data := filepath.Join(t.TempDir(), "新目录")
	opt := Options{Root: root, DataDir: data}
	p, initialized, err := InitDataDir(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !initialized || p.Root == "" {
		t.Fatalf("新目录该初始化：initialized=%v root=%q", initialized, p.Root)
	}
	for _, file := range []string{filepath.Join(data, paths.MetaFile), p.ChatDB("local"), p.ChatDB("qq"), p.GoTraceDB()} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("初始化该建 %s：%v", file, err)
		}
	}
	if _, initialized, err := InitDataDir(context.Background(), opt); err != nil || initialized {
		t.Fatalf("已经初始化的目录不该再报初始化：initialized=%v err=%v", initialized, err)
	}
}

// 身份证写了、建库失败：错误要说清是半初始化、怎么重来；身份证留着（下次不会当新目录再建库）。
func TestInitDataDirHalfInitialized(t *testing.T) {
	root := appTestRoot(t)
	old := openInitStores
	openInitStores = func(paths.Paths, store.Registry, store.OpenOptions) (*store.Set, error) {
		return nil, errors.New("编的建库失败")
	}
	t.Cleanup(func() { openInitStores = old })
	data := filepath.Join(t.TempDir(), "新目录")
	p, initialized, err := InitDataDir(context.Background(), Options{Root: root, DataDir: data})
	if err == nil || initialized {
		t.Fatalf("建库失败该报错、不该说初始化完成：initialized=%v err=%v", initialized, err)
	}
	for _, want := range []string{
		"数据目录初始化到一半（" + p.Root + "）",
		"确认里面没有要留的东西后，删掉 sumika-data.json 再打开",
		"编的建库失败",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里没有 %q：%v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(data, paths.MetaFile)); statErr != nil {
		t.Fatalf("身份证该已经写了：%v", statErr)
	}
}
