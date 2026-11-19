package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDesktopResourceRoot(t *testing.T) {
	for _, name := range []string{"missing", "empty", "whitespace", "bom", "bom-empty", "absolute", "relative", "parent", "nonexistent", "file", "no-marker", "marker-directory", "multiple", "flag", "upward", "fallback"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			exe := filepath.Join(base, "bin")
			target := filepath.Join(base, "中文 空格 # % &")
			for _, dir := range []string{exe, target} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			put := func(file, text string) {
				t.Helper()
				if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			marker := func(dir string) { put(filepath.Join(dir, "sumika.example.yml"), "# fixture") }
			marker(target)
			file := filepath.Join(exe, "sumika.root")
			want, source, flag, errorLine := exe, "sumika.root", "", ""
			switch name {
			case "missing":
				marker(exe)
				source = "往上找"
			case "empty":
				put(file, "")
				marker(base)
			case "whitespace":
				put(file, " \t\r\n \n")
				marker(base)
			case "bom":
				put(file, "\ufeff\r\n  "+target+" \r\n\n")
				want = target
			case "bom-empty":
				put(file, "\ufeff \r\n")
			case "absolute":
				put(file, target)
				want = target
			case "relative":
				put(file, "../"+filepath.Base(target))
				want = target
			case "parent":
				marker(base)
				put(file, "..")
				want = base
			case "nonexistent":
				put(file, "\nmissing")
				errorLine = "第 2 行"
			case "file":
				put(filepath.Join(exe, "plain"), "x")
				put(file, "plain")
				errorLine = "第 1 行"
			case "no-marker":
				put(file, ".")
				errorLine = "第 1 行"
			case "marker-directory":
				if err := os.Mkdir(filepath.Join(exe, "sumika.example.yml"), 0o755); err != nil {
					t.Fatal(err)
				}
				put(file, ".")
				errorLine = "第 1 行"
			case "multiple":
				put(file, "\n"+target+"\n\nother")
				errorLine = "第 4 行"
			case "flag":
				put(file, "bad\nsecond")
				flag, want, source = target, target, "命令行 -root"
			case "upward":
				marker(base)
				want, source = base, "往上找"
			case "fallback":
				source = "exe 旁边"
			}
			if errorLine != "" {
				marker(base)
			} // 有上级可用也不能吞掉显式文件错误。
			t.Chdir(t.TempDir())
			got, err := ResourceRootFrom(flag, exe)
			if errorLine != "" {
				if err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), errorLine) {
					t.Fatalf("应报告文件与行号 %s：%+v %v", errorLine, got, err)
				}
				return
			}
			if err != nil || got.Dir != want || got.Source != source {
				t.Fatalf("got %+v %v; want %s (%s)", got, err, want, source)
			}
		})
	}
}

func TestRootFileWrite(t *testing.T) {
	t.Parallel()
	exe := t.TempDir()
	target := filepath.Join(exe, "root")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sumika.example.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(exe, "sumika.root")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"missing", "root/sumika.example.yml", ".", "root\nsecond"} {
		if err := WriteRootFile(exe, text); err == nil {
			t.Fatalf("坏路径被保存：%q", text)
		}
		if b, _ := os.ReadFile(file); string(b) != "original" {
			t.Fatalf("失败破坏旧文件：%q", b)
		}
	}
	if err := WriteRootFile(exe, "\ufeff \n root \r\n"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "root\n" {
		t.Fatalf("保存内容 %q %v", b, err)
	}
	loc, err := ResourceRootFrom("", exe)
	if err != nil || loc.Dir != target {
		t.Fatalf("重启解析：%+v %v", loc, err)
	}
	if runtime.GOOS == "windows" {
		reader, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		closed := make(chan struct{})
		go func() {
			time.Sleep(100 * time.Millisecond)
			reader.Close()
			close(closed)
		}()
		err = WriteRootFile(exe, "root")
		<-closed
		if err != nil {
			t.Fatalf("文件短时占用后应能保存：%v", err)
		}
	}
	for _, text := range []string{"", " \t\n", "\ufeff\r\n"} {
		if err := WriteRootFile(exe, text); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(file); err != nil || len(b) != 0 {
			t.Fatalf("空内容没有规范为空文件：%q %v", b, err)
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteRootFile(exe, "root"); err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("改名失败没有如实返回：%v", err)
	}
	entries, err := os.ReadDir(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "sumika.root.tmp-") {
			t.Fatalf("留下临时文件 %s", entry.Name())
		}
	}
}
