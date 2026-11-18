package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/embed"
	"sumika-native/internal/embedinputs"
	"sumika-native/internal/memory"
)

// -dump-embed-inputs：用假嵌入器 / 假重排把"评测实际会送出去的字符串"记下来。这里不跑 main（它要真的 data 目录），
// 搭一个编的记忆库，走和 main 一样的 openMemory + evalEmbedder，核对：条目、考题查询、群消息查询分开记，句子由条目切出来，
// 重排的（查询，候选）记下来，重复的只记一次，向量是稳定的单位向量。
func TestDumpRecorderRecordsWhatIsSent(t *testing.T) {
	ctx := context.Background()
	rec := newDumpRecorder(2)
	ev := newEvalEmbedder(rec.Embedder())
	m := openMemory(ctx, rerankDb(t), ev, rec.Reranker(), 40, nil, nil)
	defer m.Close()

	owner := memory.Audience{Channel: "console", Conversation: "console:编的", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
	m.Recall(ctx, owner, memory.Query{Text: "主人养的猫", Mode: memory.ModeExplicit})
	m.Recall(ctx, owner, memory.Query{Text: "主人养的猫", Mode: memory.ModeExplicit}) // 同一个查询第二遍：不重复记
	rec.SetPhase(phaseGroup)
	m.Recall(ctx, owner, memory.Query{Text: "群里有人提到团子"})

	f := rec.File()
	for _, want := range []string{"主人养了一只叫团子的猫", "主人养的那只猫很胖", "主人说过群友的猫叫咪咪"} {
		if !slices.Contains(f.Items, want) {
			t.Errorf("条目里缺了「%s」：%v", want, f.Items)
		}
	}
	if len(f.Items) != 3 {
		t.Errorf("rerankDb 里有 3 条记忆，条目列表有 %d 条：%v", len(f.Items), f.Items)
	}
	if !slices.Contains(f.Queries, "主人养的猫") || slices.Contains(f.Group, "主人养的猫") {
		t.Errorf("考题的查询该在 queries 里：queries=%v group=%v", f.Queries, f.Group)
	}
	if !slices.Contains(f.Group, "群里有人提到团子") || slices.Contains(f.Queries, "群里有人提到团子") {
		t.Errorf("群消息的查询该在 group 里：queries=%v group=%v", f.Queries, f.Group)
	}
	dup := map[string]int{}
	for _, s := range append(append([]string{}, f.Queries...), f.Group...) {
		dup[s]++
	}
	for s, n := range dup {
		if n > 1 {
			t.Errorf("%q 记了 %d 次", s, n)
		}
	}
	var wantSentences []string
	for _, it := range f.Items {
		wantSentences = append(wantSentences, memory.OutboundSentences(it)...)
	}
	if !slices.Equal(f.Sentences, dedupe(wantSentences)) {
		t.Errorf("守卫的句子该是条目经 OutboundSentences 切出来的：%v 对 %v", f.Sentences, dedupe(wantSentences))
	}
	if len(f.Rerank) == 0 || len(f.Rerank) > 2 {
		t.Errorf("重排调用该记下 1～2 次（上限 2），得到 %d 次", len(f.Rerank))
	} else if f.Rerank[0].Query != "主人养的猫" || len(f.Rerank[0].Docs) < 2 {
		t.Errorf("重排该记下查询和不少于两条候选：%+v", f.Rerank[0])
	}

	// 写出去读回来
	path := filepath.Join(t.TempDir(), "dump.json")
	if err := embedinputs.Write(path, f); err != nil {
		t.Fatal(err)
	}
	if back, err := embedinputs.Read(path); err != nil || len(back.Items) != len(f.Items) || len(back.Rerank) != len(f.Rerank) {
		t.Fatalf("读不回来：%v %+v", err, back)
	}
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// 假向量：同一段文字永远是同一个向量、是单位向量、不同的文字不同；空串不是零向量（归一化不会除零）。
func TestFakeVector(t *testing.T) {
	a, b := fakeVector("今天吃什么"), fakeVector("明天吃什么")
	if !slices.Equal(a, fakeVector("今天吃什么")) {
		t.Error("同一段文字两次的向量不一样")
	}
	if slices.Equal(a, b) {
		t.Error("不同的文字给了同一个向量")
	}
	for _, text := range []string{"今天吃什么", "", "a"} {
		v := fakeVector(text)
		if len(v) != dumpDim {
			t.Errorf("%q：维度 %d", text, len(v))
		}
		if d := embed.Dot(v, v); d < 0.999999 || d > 1.000001 {
			t.Errorf("%q：不是单位向量，模长平方 %v", text, d)
		}
	}
}

// 重排只记前 rerankCap 次；记下来的候选是拷贝，之后调用方改了切片也不影响
func TestDumpRerankCapAndCopy(t *testing.T) {
	rec := newDumpRecorder(2)
	rr := rec.Reranker()
	docs := []string{"甲", "乙"}
	for i := 0; i < 5; i++ {
		order, scores, err := rr.Rerank(context.Background(), "查询", docs)
		if err != nil || !slices.Equal(order, []int{0, 1}) || len(scores) != 2 {
			t.Fatalf("假重排该原样返回顺序：%v %v %v", order, scores, err)
		}
	}
	docs[0] = "被改了"
	f := rec.File()
	if len(f.Rerank) != 2 || f.Rerank[0].Docs[0] != "甲" {
		t.Errorf("只记前 2 次、且是拷贝：%+v", f.Rerank)
	}
}

// 命令行上的两道门（main 要真的 data 目录，跑不了整个，所以编出 exe 看退出码）：
// 和 -novec、-embed-url 同时给要拒绝；写到 git 工作树里没被忽略的路径要在碰任何数据之前就拒绝，并且不留文件。
func TestDumpFlagGuards(t *testing.T) {
	if testing.Short() {
		t.Skip("要现编一个 exe，放在每日整套")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("机器上没有 git")
	}
	exe := filepath.Join(t.TempDir(), "memory-eval.exe")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build 失败：%v\n%s", err, out)
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init：%v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir() // 编的、空的：命令在碰它之前就该退出
	run := func(args ...string) (int, string) {
		cmd := exec.Command(exe, append([]string{"-data", dataDir}, args...)...)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	out := filepath.Join(t.TempDir(), "x.json")
	for _, extra := range [][]string{{"-novec"}, {"-embed-url", "http://127.0.0.1:1/v1", "-embed-model", "m"}} {
		if code, msg := run(append([]string{"-dump-embed-inputs", out}, extra...)...); code != 1 || !strings.Contains(msg, "不能和") {
			t.Errorf("%v：退出码 %d，输出 %q", extra, code, msg)
		}
	}
	bad := filepath.Join(repo, "dump.json")
	if code, msg := run("-dump-embed-inputs", bad); code != 1 || !strings.Contains(msg, ".gitignore") {
		t.Errorf("工作树里没被忽略的路径：退出码 %d，输出 %q", code, msg)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Error("拒绝了却留下了文件")
	}
	// 被忽略的路径过得了这一道（之后会因为 data 目录是空的在别处退出，输出里不该再有 .gitignore 的拒绝）
	if _, msg := run("-dump-embed-inputs", filepath.Join(repo, "private", "dump.json")); strings.Contains(msg, ".gitignore") {
		t.Errorf("被忽略的路径也被拒绝了：%q", msg)
	}
}
