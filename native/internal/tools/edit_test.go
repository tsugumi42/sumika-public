package tools

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
)

// 期望值来自 pi 的真 edit 工具（scripts/go-golden-editdiff.ts），详情每个字段逐字比。
func TestEditDetailsSameAsPi(t *testing.T) {
	var cases []struct {
		Name, Before, Path, Text string
		Edits                    json.RawMessage
		Missing, IsError         bool
		After                    *string
		Details                  *editDetails
	}
	readJSON(t, "testdata/editdiff.json", &cases)
	checked := 0
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checked++
			dir := t.TempDir()
			path := filepath.Join(dir, c.Path)
			if !c.Missing {
				if err := os.WriteFile(path, []byte(c.Before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			args, err := jsjson.Parse(`{"path":` + jsjson.Quote(c.Path) + `,"edits":` + string(c.Edits) + `}`)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Execute(context.Background(), dir, "edit", args)
			if err != nil || r.Text != c.Text || r.IsError != c.IsError {
				t.Fatalf("模型结果变了：%+v，应该 %q（出错 %v），%v", r, c.Text, c.IsError, err)
			}
			if c.Details == nil {
				if r.Details != nil {
					t.Fatalf("失败不能带成功的详情：%+v", r.Details)
				}
			} else {
				got, ok := r.Details.(editDetails)
				if !ok || got != *c.Details {
					t.Errorf("详情和 pi 不一样\n Go：%#v\n pi：%#v", r.Details, *c.Details)
				}
			}
			got, err := os.ReadFile(path)
			if c.After == nil {
				if !os.IsNotExist(err) {
					t.Fatalf("edit 不应创建文件：%q，%v", got, err)
				}
			} else if err != nil || string(got) != *c.After {
				t.Errorf("文件内容不同：%q，应该 %q，%v", got, *c.After, err)
			}
		})
	}
	if !t.Failed() {
		t.Logf("逐字对照 %d 组 pi edit 结果", checked)
	}
}

// editBarrier 是等屏障、等收尾的兜底时限，只防真卡住：差分函数、写函数都卡到测试放行，
// 工具在等它们的改法怎么等都等不到，照样红；低优先级跑在忙机器上，协程停一两秒很常见（2026-11-09 合并队列 1 秒红过）。
const editBarrier = time.Minute

func TestEditDiffCancelledAfterWrite(t *testing.T) {
	// 本测试量取消后的返回，不把测试输出管道的阻塞算进工具时限。
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := jsjson.Parse(`{"path":"a.txt","edits":[{"oldText":"before","newText":"after"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := generateEditDetails
	generateEditDetails = func(_, _, _ string) editDetails {
		close(entered)
		<-release
		defer close(finished)
		return editDetails{Diff: "迟到的详情"}
	}
	defer func() {
		close(release)
		generateEditDetails = original
		select {
		case <-finished:
		case <-time.After(editBarrier):
			t.Error("差分函数没有收尾")
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan Result, 1)
	go func() {
		r, err := Execute(ctx, dir, "edit", args)
		if err != nil {
			r = Result{Text: err.Error(), IsError: true}
		}
		result <- r
	}()
	select {
	case <-entered:
	case <-time.After(editBarrier):
		t.Fatal("没有进入写入后的差分计算")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("差分卡住时文件应该已经改好：%q，%v", got, err)
	}
	cancel()
	select {
	case r := <-result:
		if r.IsError || r.Text != "Successfully replaced 1 block(s) in a.txt." || r.Details != nil {
			t.Fatalf("写入后的差分取消应成功且不带详情：%+v", r)
		}
	case <-time.After(editBarrier):
		t.Fatal("差分取消以后工具仍在等它，成功结果没有返回")
	}
}

func TestEditWriteCrossesDeadline(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "写入成功"
		if fail {
			name = "写入失败"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "a.txt")
			if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			args, err := jsjson.Parse(`{"path":"a.txt","edits":[{"oldText":"before","newText":"after"}]}`)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			original := writeEditFile
			writeEditFile = func(path string, data []byte, mode os.FileMode) error {
				close(entered)
				<-release
				if fail {
					return os.ErrPermission
				}
				return original(path, data, mode)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			committed := &EditCommit{}
			ctx = WithEditCommit(ctx, committed)
			result := make(chan Result, 1)
			go func() {
				r, err := Execute(ctx, dir, "edit", args)
				if err != nil {
					r = Result{Text: err.Error(), IsError: true}
				}
				result <- r
			}()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				select {
				case <-result:
				case <-time.After(editBarrier):
					t.Error("写函数放行后 edit 没有收尾")
				}
				writeEditFile = original
			}()
			select {
			case <-entered:
			case <-time.After(editBarrier):
				t.Fatal("没有进入受控写函数")
			}
			type outcome struct {
				r  Result
				ok bool
			}
			expired, deadline := make(chan struct{}), make(chan outcome, 1)
			go func() {
				close(expired)
				r, ok := committed.Finish(cancel)
				deadline <- outcome{r, ok}
			}()
			<-expired
			select {
			case x := <-deadline:
				t.Fatalf("WriteFile 还没返回，到点就提前定下结果了：%+v", x)
			case <-time.After(50 * time.Millisecond):
			}
			close(release)
			select {
			case x := <-deadline:
				if x.ok != !fail || (!fail && (x.r.IsError || x.r.Text != "Successfully replaced 1 block(s) in a.txt." || x.r.Details != nil)) {
					t.Fatalf("跨过时限的写入成功状态不对：%+v，写入失败 %v", x, fail)
				}
			case <-time.After(editBarrier):
				t.Fatal("WriteFile 返回以后，到点侧仍没拿到结果")
			}
			got, err := os.ReadFile(path)
			want := "after\n"
			if fail {
				want = "before\n"
			}
			if err != nil || string(got) != want || ctx.Err() == nil {
				t.Fatalf("文件或取消状态不对：文件 %q，应该 %q，读取错误 %v，取消 %v", got, want, err, ctx.Err())
			}
		})
	}
}

func TestEditDeadlineDoesNotWaitForFileQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := jsjson.Parse(`{"path":"a.txt","edits":[{"oldText":"before","newText":"after"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := &EditCommit{}
	ctx = WithEditCommit(ctx, committed)
	result := make(chan Result, 1)
	// 文件队列还没放行时到点；返回以后再放行，不能有迟到的写入。
	_, err = withFileMutation(path, func() (string, error) {
		go func() {
			r, err := Execute(ctx, dir, "edit", args)
			if err != nil {
				r = Result{Text: err.Error(), IsError: true}
			}
			result <- r
		}()
		finished := make(chan bool, 1)
		go func() {
			_, ok := committed.Finish(cancel)
			finished <- ok
		}()
		select {
		case ok := <-finished:
			if ok {
				t.Error("还没开始写就报成了成功")
			}
		case <-time.After(editBarrier):
			t.Error("到点侧在等文件队列")
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		if !r.IsError || r.Text != "Operation aborted" {
			t.Fatalf("到点后放行的 edit 应该取消：%+v", r)
		}
	case <-time.After(editBarrier):
		t.Fatal("文件队列放行后 edit 没有收尾")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "before\n" {
		t.Fatalf("到点后不能迟到改文件：%q，%v", got, err)
	}
}
