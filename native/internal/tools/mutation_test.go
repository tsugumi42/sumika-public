package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/jsjson"
)

func editArgs(path, oldText, newText string) jsjson.Value {
	v, err := jsjson.Parse(fmt.Sprintf(`{"path":%s,"edits":[{"oldText":%s,"newText":%s}]}`, jsjson.Quote(path), jsjson.Quote(oldText), jsjson.Quote(newText)))
	if err != nil {
		panic(err)
	}
	return v
}

// 几个会话同时改同一个文件：一个一个来，一次都不丢（以前各自读到旧内容再先后覆盖）
func TestEditSameFileQueued(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	os.WriteFile(f, []byte("END\n"), 0o644)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _ := Execute(context.Background(), dir, "edit", editArgs("a.txt", "END", fmt.Sprintf("line %d\nEND", i)))
			if r.IsError {
				t.Errorf("第 %d 个：%s", i, r.Text)
			}
		}(i)
	}
	wg.Wait()
	b, _ := os.ReadFile(f)
	if got := strings.Count(string(b), "line "); got != n {
		t.Fatalf("改了 %d 次，文件里只剩 %d 次", n, got)
	}
}

// 排着队的时候被停下：轮到它时先看一眼，不再写（write.js 的 throwIfAborted）
func TestWriteAbortedWhileQueued(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	hold, holding := make(chan struct{}), make(chan struct{})
	go withFileMutation(f, func() (string, error) { close(holding); <-hold; return "", nil })
	<-holding
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result)
	go func() {
		args, _ := jsjson.Parse(`{"path":"a.txt","content":"x"}`)
		r, _ := Execute(ctx, dir, "write", args)
		done <- r
	}()
	// 等它真的排上队（队里两个：拿着的和等着的）再停，不然入口那一道就拦下了，测不到排队以后的那次检查
	for {
		mutMu.Lock()
		q := mutQueues[mutationKey(f)]
		n := 0
		if q != nil {
			n = q.users
		}
		mutMu.Unlock()
		if n == 2 {
			break
		}
		runtime.Gosched()
	}
	cancel()
	close(hold)
	r := <-done
	if !r.IsError || r.Text != "Operation aborted" {
		t.Fatalf("排队时被停下应该报 Operation aborted，得到 %+v", r)
	}
	if _, err := os.Stat(f); err == nil {
		t.Fatal("被停下了还写了文件")
	}
}
