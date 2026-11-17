package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCheckpointWindowsUnicodeNames(t *testing.T) {
	compare := windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareStringOrdinal")
	pairs := [][2]string{{"K", "K"}, {"i", "İ"}, {"i", "ı"}, {"ß", "ẞ"}, {"ß", "SS"}, {"σ", "ς"}, {"Σ", "σ"}}
	for _, pair := range pairs {
		t.Run(pair[0]+"-"+pair[1], func(t *testing.T) {
			left, right := pair[0]+".txt", pair[1]+".txt"
			a, err := windows.UTF16FromString(left)
			require(t, err)
			b, err := windows.UTF16FromString(right)
			require(t, err)
			native, _, callErr := compare.Call(uintptr(unsafe.Pointer(&a[0])), uintptr(len(a)-1), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)-1), 1)
			if native == 0 {
				t.Fatal(callErr)
			}
			alias := native == 2
			if left == "K.txt" && alias {
				t.Fatal("原生 API 意外合并 Kelvin sign")
			}
			t.Logf("CompareStringOrdinal(%q,%q,TRUE): equal=%v", left, right, alias)
			f := newFixture(t, nil)
			require(t, os.WriteFile(filepath.Join(f.work, left), []byte("left-original"), 0600))
			if alias {
				first := f.capture(t, left, "first-change")
				f.capture(t, right, "last-change")
				plan, err := f.s.Preview(t.Context(), f.owner, first.ID, "step")
				require(t, err)
				if len(plan.Files) != 1 {
					t.Fatalf("系统别名未合并：%+v", plan)
				}
				_, err = f.s.Restore(t.Context(), f.owner, f.in(), first.ID, "step", plan.Version)
				require(t, err)
				checkFile(t, filepath.Join(f.work, left), []byte("left-original"))
				checkFile(t, filepath.Join(f.work, right), []byte("left-original"))
			} else {
				require(t, os.WriteFile(filepath.Join(f.work, right), []byte("right-original"), 0600))
				checkFile(t, filepath.Join(f.work, left), []byte("left-original"))
				file, err := os.Open(filepath.Join(f.work, left))
				require(t, err)
				_, boundaryErr := checkHandle(file, filepath.Join(f.work, right))
				require(t, file.Close())
				if !errors.Is(boundaryErr, ErrLink) {
					t.Fatalf("句柄核验误认独立 Unicode 文件为别名：%v", boundaryErr)
				}
				rec, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
					if err := os.WriteFile(filepath.Join(f.work, left), []byte("left-change"), 0600); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(f.work, right), []byte("right-change"), 0600)
				})
				require(t, err)
				plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
				require(t, err)
				if len(plan.Files) != 2 {
					t.Fatalf("系统独立文件被误合并：%+v", plan)
				}
				_, err = f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version)
				require(t, err)
				checkFile(t, filepath.Join(f.work, left), []byte("left-original"))
				checkFile(t, filepath.Join(f.work, right), []byte("right-original"))
				deleted, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "powershell"}, func() error {
					if err := os.Remove(filepath.Join(f.work, left)); err != nil {
						return err
					}
					return os.Remove(filepath.Join(f.work, right))
				})
				require(t, err)
				missing, err := f.s.Preview(t.Context(), f.owner, deleted.ID, "step")
				require(t, err)
				if len(missing.Files) != 2 {
					t.Fatalf("缺失叶子被误合并：%+v", missing)
				}
				_, err = f.s.Restore(t.Context(), f.owner, f.in(), deleted.ID, "step", missing.Version)
				require(t, err)
				checkFile(t, filepath.Join(f.work, left), []byte("left-original"))
				checkFile(t, filepath.Join(f.work, right), []byte("right-original"))
			}
		})
	}
}
