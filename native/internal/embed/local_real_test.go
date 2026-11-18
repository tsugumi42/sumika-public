package embed

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 真 exe、真 dll、真模型经 Local 的测试（规格-Go本地模型 GM7 验收 8、9）。用环境变量门着，没设就跳过并写明缺什么：
//
//	SUMIKA_TEST_ORT            官方发布包里的 onnxruntime.dll（scripts/fetch-ort.ts 取回的 vendor/ort/1.24.4/onnxruntime.dll）的绝对路径，在 ort 清单里
//	SUMIKA_TEST_MODELS_ROOT    模型根目录（如 <模型根>），只读
//	SUMIKA_TEST_MEASURE=1      另外跑 TestLocalRealBatchTiming（一批 8 条、64 条的墙钟，吃 CPU，一次只跑这一样）
//
// 每个测试把 cmd/sumika-models 真的编成 exe 再起；句子都是编的。

func realGates(t *testing.T) (ort, root string) {
	t.Helper()
	ort, root = os.Getenv("SUMIKA_TEST_ORT"), os.Getenv("SUMIKA_TEST_MODELS_ROOT")
	var missing []string
	if ort == "" {
		missing = append(missing, "SUMIKA_TEST_ORT（onnxruntime.dll 的绝对路径）")
	}
	if root == "" {
		missing = append(missing, "SUMIKA_TEST_MODELS_ROOT（模型根目录）")
	}
	if len(missing) > 0 {
		t.Skip("缺环境变量：" + strings.Join(missing, "、"))
	}
	return ort, root
}

func buildModelsExe(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), ModelsExeName())
	if b, err := exec.Command("go", "build", "-o", out, "sumika-native/cmd/sumika-models").CombinedOutput(); err != nil {
		t.Fatalf("go build sumika-models 失败：%v\n%s", err, b)
	}
	return out
}

const (
	realGTE = "gte-multilingual-base"
	realBGE = "bge-reranker-v2-m3"
)

// 编的句子：长短都有，中文为主，夹英文和数字
var realSentences = []string{
	"阿青周四下午要去蓝纸诊所复查牙齿",
	"团子最怕打雷，一响就钻到柜子底下",
	"备用电池放在蓝柜最下面那一层",
	"今天晚饭吃面条，加了一个荷包蛋",
	"Meeting moved to 3pm, 请提前十分钟到",
	"她说下个月想去海边住几天，顺便把相机带上",
	"嗯",
	"编的项目代号叫 Aurora-7，预算是 12.5 万",
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func maxDiff(a, b []float32) float64 {
	var d float64
	for i := range a {
		d = math.Max(d, math.Abs(float64(a[i])-float64(b[i])))
	}
	return d
}

// startRealLocal 用生产的配置（LocalConfigFrom 同一条路径的字段）经 Local 起真 exe；返回就绪用的秒数（Local 日志里那一行的口径）
func startRealLocal(t *testing.T, exe, ort, root, rerank string) (*Local, time.Duration) {
	t.Helper()
	cfg := LocalConfig{Backend: BackendGoModels, Model: realGTE, Exe: exe, ModelsRoot: root, OrtLibrary: ort, RerankModel: rerank, MaxLineTokens: GoModelsMaxLineTokens}
	l := NewLocal(cfg, t.Logf)
	t0 := time.Now()
	l.Start()
	t.Cleanup(l.Stop)
	if !l.Wait(120 * time.Second) {
		s, why := l.Status()
		t.Fatalf("真 exe 没就绪：%s %s", s, why)
	}
	return l, time.Since(t0)
}

// GM7 验收 8：同一批句子经 Local 和手起的 Connect，向量一致（余弦 1.0，最大分量差在噪声底以内：同一个 dll、同参数，GM6 量到 0）；
// 同一份服务（Local 自己那一份）用另一个客户端问，逐位相同；Local 的 Rerank 走 lazy 点名的 bge；退出以后没有残留进程
func TestLocalRealExe(t *testing.T) {
	ort, root := realGates(t)
	exe := buildModelsExe(t)
	l, cold := startRealLocal(t, exe, ort, root, realBGE)
	t.Logf("冷启动到就绪（Local：起进程、/health、/v1/models、预热嵌入）：%.2f 秒", cold.Seconds())
	ctx := context.Background()

	viaLocal, err := l.Embed(ctx, realSentences)
	if err != nil {
		t.Fatal(err)
	}
	// 同一份服务，另一个客户端（Local 的地址和令牌是私有的，包内测试直接取）：逐位相同
	l.mu.Lock()
	src := l.client.src
	l.mu.Unlock()
	same, err := NewClient(src, realGTE).Embed(ctx, realSentences)
	if err != nil {
		t.Fatal(err)
	}
	for i := range realSentences {
		if cosine(viaLocal[i], same[i]) < 1-1e-9 || maxDiff(viaLocal[i], same[i]) != 0 {
			t.Fatalf("同一份服务两个客户端问出来该逐位相同：第 %d 句最大差 %g", i, maxDiff(viaLocal[i], same[i]))
		}
	}

	// 手起的另一份：命令行和 Local 一样，令牌走环境变量，stdin 管道开着
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	token := fmt.Sprintf("hand-%d-%d", os.Getpid(), time.Now().UnixNano()) // 每次不同的测试令牌
	cmd := exec.Command(exe, "--port", strconv.Itoa(port), "--models", realGTE, "--max-loaded", "2", "--idle-unload", "0", "--threads", "2",
		"--models-root", root, "--ort-library", ort, "--exit-on-stdin-eof", "--max-line-tokens", strconv.Itoa(GoModelsMaxLineTokens))
	cmd.Env = append(os.Environ(), "SUMIKA_EMBED_TOKEN="+token)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			cmd.Process.Kill() // 只按自己起的 PID 结束
			<-exited
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if st, _, err := httpGet(ctx, base+"/health", token); err == nil && st == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("手起的 sumika-models 60 秒没就绪：\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Setenv("SUMIKA_EMBED_TOKEN", token) // Connect 从这个环境变量取令牌
	viaConnect, err := Connect(base+"/v1", realGTE, time.Minute).Embed(ctx, realSentences)
	if err != nil {
		t.Fatal(err)
	}
	worstCos, worstDiff := 1.0, 0.0
	for i := range realSentences {
		worstCos = math.Min(worstCos, cosine(viaLocal[i], viaConnect[i]))
		worstDiff = math.Max(worstDiff, maxDiff(viaLocal[i], viaConnect[i]))
	}
	t.Logf("经 Local 和手起的 Connect，%d 句向量：最小余弦 %.9f，最大分量差 %.3e", len(realSentences), worstCos, worstDiff)
	if worstCos < 1-1e-6 || worstDiff > 1e-6 {
		t.Fatalf("两份服务的向量该一致（同一个 dll、同参数）：最小余弦 %.9f，最大分量差 %.3e", worstCos, worstDiff)
	}
	// input_type 真有区别吗：gte 没有查询前缀，query 和 document 的向量一样（登记表里 gte 无前缀）；这里只验证两种都能问通
	for _, kind := range []Type{TypeQuery, TypeDocument} {
		v, err := l.EmbedTyped(ctx, kind, realSentences[:2])
		if err != nil || len(v) != 2 || len(v[0]) != 768 {
			t.Fatalf("EmbedTyped(%s)：%v 维度 %d", kind, err, len(v[0]))
		}
	}

	// 重排：Local 点名的 bge（--lazy-models）
	docs := []string{"牙科复查约在周四下午三点", "今天的晚饭吃面条", "猫最怕打雷"}
	start := time.Now()
	order, scores, err := l.Rerank(ctx, "阿青什么时候去看牙", docs)
	if err != nil {
		t.Fatalf("Rerank：%v", err)
	}
	t.Logf("重排 3 对：%.0f 毫秒，次序 %v 分数 %.4f", float64(time.Since(start).Milliseconds()), order, scores)
	if len(order) != 3 || order[0] != 0 {
		t.Fatalf("和问题相关的那条该排第一：%v %v", order, scores)
	}

	// 退出以后没有残留：Local.Stop 杀进程树；手起的这份关掉 stdin 它自己退（--exit-on-stdin-eof）
	l.Stop()
	stdin.Close()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatalf("关了 stdin 以后 20 秒手起的 sumika-models（PID %d）还没退", pid)
	}
	if st, _ := l.Status(); st == StatusReady {
		t.Fatalf("Stop 以后状态不该还是就绪：%s", st)
	}
}

// GM7 验收 9：补向量一批 8 条（生产的批量）、64 条（参照）的墙钟，各量三遍取中位；一次只跑这一样（SUMIKA_TEST_MEASURE=1）。
// 句子是编的、长度像聊天记录（20～60 字）。补整个库的预计墙钟 = ⌈条数 ÷ 8⌉ × 一批 8 条的中位数，条数在切之前由主会话从真实库副本量出来报给主人
func TestLocalRealBatchTiming(t *testing.T) {
	ort, root := realGates(t)
	if os.Getenv("SUMIKA_TEST_MEASURE") != "1" {
		t.Skip("缺环境变量：SUMIKA_TEST_MEASURE=1（量速度，吃 CPU，一次只跑这一样）")
	}
	exe := buildModelsExe(t)
	l, cold := startRealLocal(t, exe, ort, root, "")
	t.Logf("冷启动到就绪：%.2f 秒", cold.Seconds())
	ctx := context.Background()
	// 服务端每个模型有 256 条的 LRU 缓存（和 Python 版一样）：同样的句子第二次问是命中，不是在算。所以每次问的句子都带一个不重样的编号
	salt := 0
	mk := func(n int) []string {
		salt++
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("第%d-%d条编的记忆：阿青%s去%s%s，%s", salt, i+1,
				[]string{"周一", "周二", "周三", "周四", "周五", "周末"}[i%6], []string{"蓝纸诊所", "东门的面馆", "图书馆三楼", "河边的球场"}[i%4],
				[]string{"复查", "吃饭", "借书", "打球"}[i%4], strings.Repeat("顺便带上了相机和备用电池", 1+i%3))
		}
		return out
	}
	median := func(n int) time.Duration {
		var ds []time.Duration
		for k := 0; k < 3; k++ {
			t0 := time.Now()
			if _, err := l.EmbedTyped(ctx, TypeDocument, mk(n)); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(t0))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[1]
	}
	if _, err := l.EmbedTyped(ctx, TypeDocument, mk(8)); err != nil { // 第一次问要 ORT 初始化各算子，不算
		t.Fatal(err)
	}
	if r, err := l.EmbedTyped(ctx, TypeDocument, mk(64)); err != nil || len(r) != 64 { // 64 条的形状也先热一遍
		t.Fatal(err)
	}
	m8, m64 := median(8), median(64)
	perBatch := float64(m8.Milliseconds())
	t.Logf("一批 8 条（生产的补向量批量）中位数 %d 毫秒；一批 64 条（参照）中位数 %d 毫秒", m8.Milliseconds(), m64.Milliseconds())
	for _, n := range []int{100, 600, 1000, 3000} {
		t.Logf("补齐 %d 条的预计墙钟：%d 批 × %.0f 毫秒 ≈ %.1f 秒", n, (n+7)/8, perBatch, float64((n+7)/8)*perBatch/1000)
	}
	// 重算整个库不该比 Python 的 64 条一批慢到离谱：规格第五节 64 条一批 ≤ 3.4 秒
	if m64 > 3400*time.Millisecond {
		t.Errorf("64 条一批 %v，超过规格第五节的 3.4 秒", m64)
	}
}

// 真 sumika-models 的确定性错误经 Local 都是"退出码 2、不再重试、第一行带它的原因"：模型根目录不存在、dll 不是 dll、
// dll 的哈希不在已知表里（Windows 自带的那份 onnxruntime.dll 就是，规格 2.3）。守卫随之 literal-pending（app 层的 TestGoModelsDownChain 走过）
func TestLocalRealExeConfigErrors(t *testing.T) {
	ort, root := realGates(t)
	exe := buildModelsExe(t)
	notDLL := filepath.Join(t.TempDir(), "onnxruntime.dll")
	if err := os.WriteFile(notDLL, []byte("这不是 dll"), 0o644); err != nil {
		t.Fatal(err)
	}
	system := filepath.Join(os.Getenv("SystemRoot"), "System32", "onnxruntime.dll")
	for _, tc := range []struct {
		name, ort, root, want string
	}{
		{"模型根目录不存在", ort, filepath.Join(t.TempDir(), "没有的模型根"), "模型"},
		{"dll 不是 dll", notDLL, root, "动态库"},
		{"dll 的哈希不在已知表里（Windows 自带的）", system, root, "动态库"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ort == system {
				if _, err := os.Stat(system); err != nil {
					t.Skip("这台机器上没有 System32 里的 onnxruntime.dll")
				}
			}
			cfg := LocalConfig{Backend: BackendGoModels, Model: realGTE, Exe: exe, ModelsRoot: tc.root, OrtLibrary: tc.ort, MaxLineTokens: GoModelsMaxLineTokens}
			l := NewLocal(cfg, t.Logf)
			l.Start()
			defer l.Stop()
			var why string
			if !waitFor(60*time.Second, func() bool {
				var s Status
				s, why = l.Status()
				return s == StatusDown && strings.Contains(why, "不再重试")
			}) {
				s, why := l.Status()
				t.Fatalf("该是 down 且说不再重试：%s %q", s, why)
			}
			first, _, _ := strings.Cut(why, "\n")
			t.Logf("第一行：%s", first)
			if !strings.Contains(first, tc.want) || !strings.Contains(first, "退出码 2") {
				t.Fatalf("第一行该带 sumika-models 的错误内容和退出码 2：%q", first)
			}
			if l.Wait(time.Second) {
				t.Fatal("放弃以后不该等到就绪")
			}
		})
	}
}
