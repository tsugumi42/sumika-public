package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// GM7 验收 7（编的库上的版本）：同一份代码、同一份库，Go 引擎和 Python 引擎各算一遍向量，每道题召回出来的名单（含顺序）逐题相同。
// 库是现编的 28 条私事，题是现编的 40 道（28 道短关键词、12 道换说法），不读真实库。env 门着，没设就跳过并写明缺什么：
//
//	SUMIKA_TEST_ORT            官方发布包里的 onnxruntime.dll（scripts/fetch-ort.ts 取回的 vendor/ort/1.24.4/onnxruntime.dll）的绝对路径
//	SUMIKA_TEST_MODELS_ROOT    模型根目录
//	SUMIKA_TEST_PYTHON         按已删的 tools/local-models/requirements.txt（ADR-389）用 uv 建的独立环境里的 python.exe
//	SUMIKA_TEST_SERVER         tools/local-models/server.py 的绝对路径（这两个文件已删，要从 git 历史 76f0488faa6a 及以前取回）

var realFacts = []string{
	"阿青周四下午三点要去蓝纸诊所复查牙齿", "阿青把备用电池收在蓝柜最底层的抽屉里", "阿青养了一只叫团子的橘猫，最怕打雷", "阿青下个月十号要搬到河东路的新房子",
	"阿青对花生过敏，吃到会起红疹", "阿青的备用钥匙放在阳台第二个花盆底下", "阿青最近在学吉他，每天晚上练半小时", "阿青打算年底去北海道滑雪，已经订好了机票",
	"阿青的妹妹叫小满，在外地读大学", "阿青周末固定去河边的球场打羽毛球", "阿青上周弄丢了一把深蓝色的长柄雨伞", "阿青正在偷偷准备给小满的生日礼物，是一条手织围巾",
	"阿青的笔记本电脑摔过一次，右下角有裂缝", "阿青最讨厌香菜，点外卖总要备注不要香菜", "阿青每个月最后一个周五要写月报，写到很晚", "阿青的外套口袋里常年放着一包薄荷糖",
	"阿青申请了下半年的进修名额，结果月底公布", "阿青家楼下新开了一家咖啡店，叫做半糖", "阿青最近睡得很浅，医生建议睡前别看手机", "阿青把旧相机卖给了同事老陈，卖了八百块",
	"阿青的自行车前轮漏气，打算周六拿去修", "阿青答应了帮邻居照看三天的鱼缸", "阿青在读一本讲潮汐的科普书，已经读到第四章", "阿青每天早上七点二十出门，坐十二路公交",
	"阿青在攒钱买一台二手的投影仪", "阿青下周二要给客户做一次线上演示", "阿青收到了一张老同学婚礼的请柬，十一月二十日举行", "阿青最近迷上了做手冲咖啡，买了一套新壶",
}

var realShort = []string{"牙", "电池", "猫", "搬家", "花生", "钥匙", "吉他", "滑雪", "小满", "羽毛球", "雨伞", "围巾", "电脑", "香菜", "月报", "薄荷糖", "进修", "咖啡店", "睡眠", "相机", "自行车", "鱼缸", "潮汐", "公交", "投影仪", "演示", "婚礼", "手冲"}

var realParaphrase = []string{
	"周四下午他要去哪看牙", "那几节电池他放哪了", "他家的猫怕什么", "他什么时候搬到新房子", "他吃什么会过敏", "备用的钥匙压在什么地方",
	"他在练什么乐器", "年底他打算去哪玩", "他妹妹在哪读书", "周末他一般去哪运动", "他丢了什么东西", "他在给妹妹准备什么礼物",
}

// libraryFile 造一份 Bun 版 memory.db 的表（只建 loadItems 读到的列）：28 条 owner 范围的事实
func libraryFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建 loadItems 要读的几列，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	for i, text := range realFacts {
		if _, err := db.Exec(`INSERT INTO facts VALUES (?, 'fact', 'active', ?, 'owner', 'owner', '', ?, ?, NULL, NULL)`, strconv.Itoa(i+1), text, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

type recallResult struct {
	cands, hits []string
	sims        []float64
	took        time.Duration // 这道题明确回忆（取全部候选）用了多久，含现算查询向量
}

// recallAll 用一个引擎把库打开（Open 期间算条目的向量），每道题明确回忆一遍（取全部候选的名单和向量余弦），再日常召回一遍（真给了的名单）
func recallAll(t *testing.T, ev *evalEmbedder, questions []string) []recallResult {
	t.Helper()
	m := openMemory(context.Background(), libraryFile(t), ev, nil, 0, nil, nil)
	defer m.Close()
	owner := memory.Audience{Channel: "console", Conversation: "console:eval", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
	out := make([]recallResult, len(questions))
	for i, q := range questions {
		t0 := time.Now()
		r := m.Recall(context.Background(), owner, memory.Query{Text: q, Mode: memory.ModeExplicit})
		out[i].took = time.Since(t0)
		for _, h := range r.Candidates {
			out[i].cands = append(out[i].cands, h.Ref)
			out[i].sims = append(out[i].sims, h.Sim)
		}
		for _, h := range m.Recall(context.Background(), owner, memory.Query{Text: q}).Hits {
			out[i].hits = append(out[i].hits, h.Ref)
		}
	}
	return out
}

// tookPercentiles：每题明确回忆耗时的 P50 和 P95（最近邻法，不插值）
func tookPercentiles(rs []recallResult) (p50, p95 time.Duration) {
	ds := make([]time.Duration, len(rs))
	for i, r := range rs {
		ds[i] = r.took
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	at := func(p float64) time.Duration {
		if len(ds) == 0 {
			return 0
		}
		return ds[min(len(ds)-1, int(math.Ceil(p*float64(len(ds))))-1)]
	}
	return at(0.50), at(0.95)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRealEnginesSameRecallLists(t *testing.T) {
	var missing []string
	env := map[string]string{}
	for _, k := range []string{"SUMIKA_TEST_ORT", "SUMIKA_TEST_MODELS_ROOT", "SUMIKA_TEST_PYTHON", "SUMIKA_TEST_SERVER"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Skipf("缺环境变量：%v", missing)
	}
	questions := append(append([]string{}, realShort...), realParaphrase...)

	// Go 引擎：embed.Local 起编出来的真 sumika-models（和后台同一条命令行）
	exe := filepath.Join(t.TempDir(), embed.ModelsExeName())
	if b, err := exec.Command("go", "build", "-o", exe, "sumika-native/cmd/sumika-models").CombinedOutput(); err != nil {
		t.Fatalf("go build sumika-models：%v\n%s", err, b)
	}
	cfg := embed.LocalConfig{Backend: embed.BackendGoModels, Model: "gte-multilingual-base", Exe: exe, ModelsRoot: env["SUMIKA_TEST_MODELS_ROOT"], OrtLibrary: env["SUMIKA_TEST_ORT"], MaxLineTokens: embed.GoModelsMaxLineTokens}
	l := embed.NewLocal(cfg, t.Logf)
	l.Start()
	defer l.Stop()
	if !l.Wait(120 * time.Second) {
		s, why := l.Status()
		t.Fatalf("Go 引擎没起来：%s %s", s, why)
	}
	goRes := recallAll(t, newEvalEmbedder(l), questions)
	l.Stop() // 一次只跑一个模型进程

	// Python 引擎：手起 server.py（用独立环境的 python；靠环境变量 PYTHONDONTWRITEBYTECODE=1 不让它写 .pyc），Connect 过去，查询和条目分别带 input_type
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	token := fmt.Sprintf("py-%d-%d", os.Getpid(), time.Now().UnixNano())
	cmd := exec.Command(env["SUMIKA_TEST_PYTHON"], "-X", "utf8", env["SUMIKA_TEST_SERVER"], "--port", strconv.Itoa(port), "--threads", "2")
	cmd.Env = append(os.Environ(), "SUMIKA_EMBED_TOKEN="+token, "PYTHONDONTWRITEBYTECODE=1", "PYTHONIOENCODING=utf-8")
	var stderr bytes.Buffer
	cmd.Stderr, cmd.Stdout = &stderr, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	defer func() {
		select {
		case <-exited:
		default:
			cmd.Process.Kill() // 只按自己起的 PID 结束
			<-exited
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := false
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && !ready; time.Sleep(100 * time.Millisecond) {
		req, _ := http.NewRequest(http.MethodGet, base+"/health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if resp, err := (&http.Client{Timeout: time.Second}).Do(req); err == nil {
			resp.Body.Close()
			ready = resp.StatusCode == 200
		}
	}
	if !ready {
		t.Fatalf("Python 引擎 90 秒没就绪：\n%s", stderr.String())
	}
	t.Setenv("SUMIKA_EMBED_TOKEN", token)
	pyRes := recallAll(t, newEvalEmbedder(embed.Connect(base+"/v1", "gte-multilingual-base", 5*time.Minute)), questions)

	same, sameHits, worstSim, anyCands := 0, 0, 0.0, 0
	for i, q := range questions {
		if equalStrings(goRes[i].cands, pyRes[i].cands) {
			same++
		} else {
			t.Errorf("题 %d（%d 字）候选名单不同：Go %v / Python %v", i+1, len([]rune(q)), goRes[i].cands, pyRes[i].cands)
		}
		if equalStrings(goRes[i].hits, pyRes[i].hits) {
			sameHits++
		} else {
			t.Errorf("题 %d 日常真给了的名单不同：Go %v / Python %v", i+1, goRes[i].hits, pyRes[i].hits)
		}
		anyCands += len(goRes[i].cands)
		for k := range goRes[i].sims {
			if k < len(pyRes[i].sims) {
				worstSim = math.Max(worstSim, math.Abs(goRes[i].sims[k]-pyRes[i].sims[k]))
			}
		}
	}
	t.Logf("编的库 28 条、题 %d 道：明确回忆的候选名单（含顺序）逐题相同 %d/%d，日常给了的名单逐题相同 %d/%d；候选共 %d 条，向量余弦的最大差 %.3e",
		len(questions), same, len(questions), sameHits, len(questions), anyCands, worstSim)
	goP50, goP95 := tookPercentiles(goRes)
	pyP50, pyP95 := tookPercentiles(pyRes)
	t.Logf("明确回忆每题耗时（含现算查询向量，编的库 28 条，%d 题；不是真实库的数）：Go P50 %v / P95 %v，Python P50 %v / P95 %v", len(questions), goP50, goP95, pyP50, pyP95)
	if anyCands == 0 {
		t.Fatal("一条候选都没有：测的不是召回")
	}
}
