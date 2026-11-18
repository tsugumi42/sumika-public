package tools

// 并行的一批工具里同时有几个 ls（对拍 tool-repeat 第一步就是"一批里两次 ls"）：排序用的 Collator 里有每次比较都改写的缓冲区，
// 共用一个会互相踩乱（顺序错，-race 下报竞争）。几个 ls 同时跑同一个目录，各自的结果都要和单独跑的一样

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"sumika-native/internal/jsjson"
)

func TestLsParallel(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.md", "a.md", "Zeta.txt", "alpha.txt", "Beta", "_x", "10.txt", "9.txt", "éclair", "Ecole", "c-d", "c_d", "cd"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args, err := jsjson.Parse(`{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ls(dir, args)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				got, err := ls(dir, args)
				if err != nil {
					t.Error(err)
					return
				}
				if got != want {
					t.Errorf("同时跑的 ls 顺序乱了：\n%s\n应该是：\n%s", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}
