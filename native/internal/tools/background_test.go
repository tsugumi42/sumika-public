package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/kernel"
)

// 每个测试用自己的会话身份，互不看见；结束时只收自己起的，不碰别的测试的后台进程。
func bgScope(t *testing.T, conversation string) (context.Context, string) {
	t.Helper()
	dir := t.TempDir() // 先建目录再登记清理：清理后进先出，进程要在目录删除之前停掉
	ctx := kernel.WithInvocation(context.Background(), kernel.Invocation{Scope: kernel.Scope{CharacterID: t.Name(), Conversation: conversation}})
	t.Cleanup(func() {
		bg.mu.Lock()
		mine, _, _ := bg.forOwner(bgOwner(ctx))
		bg.mu.Unlock()
		for _, p := range mine {
			if p.running() {
				p.stop()
			}
		}
	})
	return ctx, dir
}

func bgArgs(t *testing.T, json string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(json)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func startBg(t *testing.T, ctx context.Context, dir, command string) string {
	t.Helper()
	r, err := Execute(ctx, dir, "powershell", bgArgs(t, `{"purpose":"测试","command":`+jsjson.Quote(command)+`,"background":true}`))
	if err != nil || r.IsError {
		t.Fatalf("起后台命令：%+v %v", r, err)
	}
	i := strings.Index(r.Text, "bg-")
	if i < 0 || !strings.Contains(r.Text, "后台进程已启动") {
		t.Fatalf("没返回编号：%q", r.Text)
	}
	id := r.Text[i:]
	return id[:strings.IndexAny(id, "（ ")]
}

func execBg(t *testing.T, ctx context.Context, tool, args string) Result {
	t.Helper()
	r, err := Execute(ctx, t.TempDir(), tool, bgArgs(t, args))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// 轮询到输出里出现 want 为止（每次带 wait:1）；返回这几次取到的全部新增文字
func pollUntil(t *testing.T, ctx context.Context, id, want string) string {
	t.Helper()
	var all strings.Builder
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r := execBg(t, ctx, "process_output", `{"id":"`+id+`","wait":1}`)
		if r.IsError {
			t.Fatalf("取输出出错：%s", r.Text)
		}
		all.WriteString(r.Text)
		if strings.Contains(all.String(), want) {
			return all.String()
		}
	}
	t.Fatalf("60 秒内没等到 %q：%s", want, all.String())
	return ""
}

func TestBackgroundOutputTwiceThenStop(t *testing.T) {
	if testing.Short() {
		t.Skip("真实 pwsh 后台任务的全流程，放在每日整套")
	}
	ctx, dir := bgScope(t, "c1")
	id := startBg(t, ctx, dir, `$i=0; while($true){ $i++; "tick $i"; Start-Sleep -Milliseconds 100 }`)
	first := pollUntil(t, ctx, id, "tick 3")
	if !strings.Contains(first, "运行中") || !strings.Contains(first, "tick 1\n") && !strings.Contains(first, "tick 1\r\n") {
		t.Fatalf("第一次取：%q", first)
	}
	second := pollUntil(t, ctx, id, "tick 9")
	if strings.Contains(second, "tick 1\n") || strings.Contains(second, "tick 1\r\n") || strings.Contains(second, "tick 2\n") || strings.Contains(second, "tick 2\r\n") {
		t.Fatalf("第二次只该给新增的部分，却带着第一次的：%q", second)
	}
	stop := execBg(t, ctx, "process_stop", `{"id":"`+id+`"}`)
	if stop.IsError || !strings.Contains(stop.Text, "已结束") || !strings.Contains(stop.Text, "被停掉") {
		t.Fatalf("停：%+v", stop)
	}
	bg.mu.Lock()
	mine, running, _ := bg.forOwner(bgOwner(ctx))
	bg.mu.Unlock()
	if running != 0 || len(mine) != 1 || mine[0].running() {
		t.Fatalf("停掉以后不该还在跑：%d", running)
	}
	again := execBg(t, ctx, "process_output", `{"id":"`+id+`"}`)
	if again.IsError || !strings.Contains(again.Text, "已结束") || !strings.Contains(again.Text, "没有新内容") {
		t.Fatalf("停掉后再取应该报已结束：%+v", again)
	}
	// 已经结束的再停：不报错，说明不用停
	if r := execBg(t, ctx, "process_stop", `{"id":"`+id+`"}`); r.IsError || !strings.Contains(r.Text, "不用再停") {
		t.Fatalf("重复停：%+v", r)
	}
}

func TestBackgroundWaitReturnsAtExit(t *testing.T) {
	ctx, dir := bgScope(t, "c1")
	id := startBg(t, ctx, dir, `"hello"; exit 3`)
	start := time.Now()
	r := execBg(t, ctx, "process_output", `{"id":"`+id+`","wait":60}`)
	if time.Since(start) > 50*time.Second || !strings.Contains(r.Text, "hello") || !strings.Contains(r.Text, "已结束，退出码 3") {
		t.Fatalf("wait 应该在结束时提前返回：%v %q", time.Since(start), r.Text)
	}
}

func TestBackgroundLimitAndOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("真实 pwsh 后台任务的全流程，放在每日整套")
	}
	a, dir := bgScope(t, "a")
	b, _ := bgScope(t, "b")
	ids := make([]string, 0, bgMaxRunning)
	for i := 0; i < bgMaxRunning; i++ {
		ids = append(ids, startBg(t, a, dir, `Start-Sleep -Seconds 300`))
	}
	r := execBg(t, a, "powershell", `{"purpose":"测试","command":"Start-Sleep 300","background":true}`)
	if !r.IsError || !strings.Contains(r.Text, "上限 8") || !strings.Contains(r.Text, "process_stop") || !strings.Contains(r.Text, ids[0]) {
		t.Fatalf("第 9 个应该拒绝并说明原因：%+v", r)
	}
	// 别的对话不受影响，也看不到、停不掉这个对话的编号
	other := startBg(t, b, dir, `Start-Sleep -Seconds 300`)
	if r := execBg(t, b, "process_stop", `{"id":"`+ids[0]+`"}`); !r.IsError || !strings.Contains(r.Text, "没有编号") || strings.Contains(r.Text, ids[1]) {
		t.Fatalf("别的对话不该碰到：%+v", r)
	}
	// 停掉一个，名额就回来
	execBg(t, a, "process_stop", `{"id":"`+ids[0]+`"}`)
	startBg(t, a, dir, `Start-Sleep -Seconds 300`)
	if r := execBg(t, b, "process_output", `{"id":"`+other+`"}`); r.IsError {
		t.Fatalf("自己的编号取不到：%+v", r)
	}
	// 缺 id、乱写的编号：报错里列出现有的
	if r := execBg(t, a, "process_output", `{}`); !r.IsError || !strings.Contains(r.Text, ids[1]) {
		t.Fatalf("缺 id：%+v", r)
	}
}

// 程序退出时收掉所有：进程树真的退了（它写的文件不再增长），不只是记录改了
func TestStopAllBackground(t *testing.T) {
	ctx, dir := bgScope(t, "c1")
	marker := filepath.Join(dir, "m.txt")
	startBg(t, ctx, dir, `while($true){ Add-Content -Path `+jsjson.Quote(marker)+` -Value x; Start-Sleep -Milliseconds 50 }`)
	startBg(t, ctx, dir, `Start-Sleep -Seconds 300`)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if fi, err := os.Stat(marker); err == nil && fi.Size() > 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台命令没有开始写文件")
		}
		time.Sleep(50 * time.Millisecond)
	}
	StopAllBackground()
	bg.mu.Lock()
	_, running, _ := bg.forOwner(bgOwner(ctx))
	bg.mu.Unlock()
	if running != 0 {
		t.Fatalf("退出清理后还有 %d 个在跑", running)
	}
	fi, _ := os.Stat(marker)
	size := fi.Size()
	time.Sleep(500 * time.Millisecond)
	if fi, _ = os.Stat(marker); fi.Size() != size {
		t.Fatalf("进程树没真停：文件还在增长 %d → %d", size, fi.Size())
	}
}

// 缓冲只留最后 1MB：丢最旧的、从字的开头切、取输出时说明丢了多少
func TestBackgroundBufferDropsOldest(t *testing.T) {
	p := &bgProc{done: make(chan struct{})}
	p.dec = jsstr.NewDecoder()
	chunk := strings.Repeat("字符", 1000) + "\n" // 6001 字节，3 字节一个字，切点会落在字中间
	total := 0
	for total < 3*bgBufferMax {
		p.Write([]byte(chunk))
		total += len(chunk)
	}
	if len(p.text) > bgBufferMax || p.base != total-len(p.text) || !utf8.ValidString(p.text) {
		t.Fatalf("缓冲：len=%d base=%d total=%d valid=%v", len(p.text), p.base, total, utf8.ValidString(p.text))
	}
	text, note := p.take()
	if !strings.Contains(note, "已经丢掉") || !strings.Contains(note, "1.0MB") || text == "" {
		t.Fatalf("取输出：note=%q len(text)=%d", note, len(text))
	}
	if text2, note2 := p.take(); text2 != "" || note2 != "" {
		t.Fatalf("取完再取不该有东西：%q %q", text2, note2)
	}
}

// 整条路径：真命令输出超过 1MB，等结束后取，开头被截掉并标明，末尾还在
func TestBackgroundBufferEndToEnd(t *testing.T) {
	ctx, dir := bgScope(t, "c1")
	id := startBg(t, ctx, dir, `$s='x'*1000; 1..2500 | ForEach-Object { "line $_ $s" }`)
	r := execBg(t, ctx, "process_output", `{"id":"`+id+`","wait":120}`)
	if !strings.Contains(r.Text, "已经丢掉，没法再取") || !strings.Contains(r.Text, "line 2500 ") || strings.Contains(r.Text, "line 1 x") || !strings.Contains(r.Text, "已结束，退出码 0") {
		head := r.Text
		if len(head) > 300 {
			head = head[:300]
		}
		t.Fatalf("截断标明不对：%q", head)
	}
	bg.mu.Lock()
	mine, _, _ := bg.forOwner(bgOwner(ctx))
	bg.mu.Unlock()
	if len(mine[0].text) > bgBufferMax {
		t.Fatalf("缓冲超过上限：%d", len(mine[0].text))
	}
}

// 命令自己的 timeout 对后台命令是"到点停"，不是默认 120 秒
func TestBackgroundTimeoutArg(t *testing.T) {
	ctx, _ := bgScope(t, "c1")
	r := execBg(t, ctx, "powershell", `{"purpose":"测试","command":"Start-Sleep 300","background":true,"timeout":3}`)
	id := r.Text[strings.Index(r.Text, "bg-"):]
	id = id[:strings.IndexAny(id, "（ ")]
	out := execBg(t, ctx, "process_output", `{"id":"`+id+`","wait":60}`)
	if !strings.Contains(out.Text, "到了 timeout 被停掉") {
		t.Fatalf("%q", out.Text)
	}
}

// 起命令的 ctx 取消（这一轮停下）不能带走后台命令
func TestBackgroundSurvivesStartContext(t *testing.T) {
	base, dir := bgScope(t, "c1")
	ctx, cancel := context.WithCancel(base)
	id := startBg(t, ctx, dir, `Start-Sleep -Seconds 300`)
	cancel()
	time.Sleep(300 * time.Millisecond)
	if r := execBg(t, base, "process_output", `{"id":"`+id+`"}`); !strings.Contains(r.Text, "运行中") {
		t.Fatalf("ctx 取消后后台命令不该停：%q", r.Text)
	}
}
