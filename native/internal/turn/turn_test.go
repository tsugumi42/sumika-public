package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/goccy/go-yaml"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/llm/openaichat"
	"sumika-native/internal/paths"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// testCase 是 testdata/cases/*.json：和 scripts/go-golden-turn.test.ts 读的是同一份
type testCase struct {
	Now         string            `json:"now"`
	CharacterID string            `json:"characterId"`
	Files       map[string]string `json:"files"`
	Characters  map[string]struct {
		Model string `json:"model"`
	} `json:"characters"`
	Agent struct {
		Thinking struct {
			Local string `json:"local"`
		} `json:"thinking"`
		RecentMessages *int `json:"recentMessages"`
		Context        struct {
			SoftRatio float64 `json:"softRatio"`
		} `json:"context"`
	} `json:"agent"`
	Workdir string            `json:"workdir"`
	Shell   *Shell            `json:"shell"`
	Models  []json.RawMessage `json:"models"`
	Events  []struct {
		Channel      string          `json:"channel"`
		Conversation string          `json:"conversation"`
		ActorKind    string          `json:"actor_kind"`
		ActorID      string          `json:"actor_id"`
		Origin       string          `json:"origin"`
		Kind         string          `json:"kind"`
		Text         string          `json:"text"`
		Segments     json.RawMessage `json:"segments"`
		CreatedAt    int64           `json:"created_at"`
	} `json:"events"`
	Epochs  []map[string]any `json:"epochs"`
	Text    string           `json:"text"`
	Reply   llmtest.Reply    `json:"reply"`
	Replies []llmtest.Reply  `json:"replies"` // 一轮里有几次请求：第 n 次回 replies[n]，没写的用 reply
	// WorkdirFiles：工具场景在临时工作目录里放的文件（路径每次不一样，比之前换成 <WORKDIR>）
	WorkdirFiles map[string]string `json:"workdirFiles"`
	// Tools：工具的设置（现在只用 approvalMode；不写是 manual）
	Tools struct {
		ApprovalMode    string   `json:"approvalMode"`
		ApprovalMinutes *float64 `json:"approvalMinutes"`
	} `json:"tools"`
	// Approvals：审批的回答（2e），每发出一张卡片按先后取一个；wait 是不回答（等它超时）
	Approvals []struct {
		Choice string `json:"choice"`
		Reason string `json:"reason"`
	} `json:"approvals"`
	// Interject：主人中途插话、按停止（2f），和 golden 脚本的一样：delta 是第 request 次请求的第 n 段正文、approval 是第 n 张卡片、
	// tool 是工具 id 开始以后再等 delayMs 毫秒
	Interject []struct {
		On      string `json:"on"`
		Request int    `json:"request"`
		N       int    `json:"n"`
		ID      string `json:"id"`
		DelayMs int    `json:"delayMs"`
		Marker  string `json:"marker"`
		// AfterTool：到点以后先等这个工具（provider 调用 id）落完结果再做，把"停止落在哪个工具之前完成之后"钉死，不靠机器快慢
		AfterTool string `json:"afterTool"`
		Do        string `json:"do"`
		Text      string `json:"text"`
	} `json:"interject"`
	// Next：同一个实例接着说的后面几轮（2g），和 golden 脚本的一样：now 是这一轮的钟，files 是这一轮之前改的数据目录文件，
	// together 是和上一句前后脚发
	Next []struct {
		Text     string            `json:"text"`
		Now      string            `json:"now"`
		Files    map[string]string `json:"files"`
		Together bool              `json:"together"`
		Restart  bool              `json:"restart"` // 发之前先丢掉实例
	} `json:"next"`
	ExpectErrors    []string `json:"expectErrors"`    // Bun 会报的错（几个就是 Go 有几轮返回错误）
	ExpectNoRequest bool     `json:"expectNoRequest"` // 卡坏了之类：不发请求
}

// 出厂提示词原样拷到临时的"项目根"（和 golden 脚本的 factoryRoot 一样）：拼系统提示时换行统一成 LF（prompts.LF），
// golden 不跟着 git 的签出方式变；Windows 上签出的是 CRLF，正好证明统一生效了
func factoryRoot(t *testing.T) string {
	t.Helper()
	factoryOnce.Do(func() {
		repo, err := config.FindRoot(".")
		if err != nil {
			factoryErr = err
			return
		}
		dir, err := os.MkdirTemp("", "turn-factory-")
		if err != nil {
			factoryErr = err
			return
		}
		os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
		entries, _ := os.ReadDir(filepath.Join(repo, "prompts"))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				b, _ := os.ReadFile(filepath.Join(repo, "prompts", e.Name()))
				os.WriteFile(filepath.Join(dir, "prompts", e.Name()), b, 0o644)
			}
		}
		factoryDir = dir
	})
	if factoryErr != nil {
		t.Fatal(factoryErr)
	}
	return factoryDir
}

// 出厂提示词的临时根整个测试进程只拷一次：它只被读（提示词、配置），没有测试往里写；每条测试各拷一遍是 21 个文件的真写盘。
var (
	factoryOnce sync.Once
	factoryDir  string
	factoryErr  error
)

// 假网关：记下原样的请求体，按场景吐思考、文字、用量（和 golden 脚本里的一样）。bodiesMu 管着 bodies（插话要看现在是第几次请求）
func gateway(t *testing.T, c testCase, bodies *[]string, bodiesMu *sync.Mutex) *httptest.Server {
	return memServer(gatewayHandler(t, c, bodies, bodiesMu))
}

// gatewayHandler 是假网关本身（契约测试在外面包一层，收到请求时先看一眼库）：场景的回复照给 llmtest；
// 要停在第 n 段正文上的（停止、中途发新的一句）看场景的 interject，吐完前 n 段就停住，等她那边中止连接
func gatewayHandler(t *testing.T, c testCase, bodies *[]string, bodiesMu *sync.Mutex) http.HandlerFunc {
	return llmtest.Handler(llmtest.Script{Models: c.Models, Reply: c.Reply, Replies: c.Replies, Hold: func(nth int) int {
		hold := -1
		for _, x := range c.Interject {
			if x.On == "delta" && x.Do != "steer" && x.Request == nth {
				hold = x.N
			}
		}
		return hold
	}}, bodies, bodiesMu)
}

// 读一张表成通用的值（数字都是 float64、NULL 是 nil），和 golden 的 JSON 解出来的一样好比
func rows(t *testing.T, db *sql.DB, q string, args ...any) []map[string]any {
	t.Helper()
	rs, err := db.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	out := []map[string]any{}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case int64:
				m[c] = float64(v)
			case []byte:
				m[c] = string(v)
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out
}

// *_json 列解开再比（库里的人按 JSON 读它；两边写 JSON 的转义细节不同不算不一样）
func normalizeJSONCols(rs []map[string]any) {
	for _, r := range rs {
		for k, v := range r {
			if s, ok := v.(string); ok && strings.HasSuffix(k, "_json") {
				var x any
				if json.Unmarshal([]byte(s), &x) == nil {
					r[k] = x
				}
			}
		}
	}
}

func runCase(t *testing.T, name string) {
	t0 := time.Now() // 影子对比的计时：这个场景从这里开始
	var c testCase
	b, err := os.ReadFile(filepath.Join(testdataDir(), "cases", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Requests []map[string]any            `json:"requests"`
		Rows     map[string][]map[string]any `json:"rows"`
		Grants   any                         `json:"grants"`   // 有审批回答的场景：tool-grants.yml 读回来的 grants（没有文件是 null）
		Steers   any                         `json:"steers"`   // 有插话、停止的场景：插话走到了哪几步
		Instance any                         `json:"instance"` // 对拍第一层：跑完以后实例里留着的消息（没有实例是 null）
	}
	gb, err := os.ReadFile(filepath.Join(testdataDir(), "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	// 有意和 Bun 不一样的场景（ADR-183 第 6 条）：intended/ 里登记了 Go 自己的那份（带 adr、why），比它不比 Bun 的；
	// Bun 的录像哪天和登记的一样了就报错，提醒删掉登记。INTENDED=1 时把这一次 Go 的结果写回登记（先手写一份只有 adr、why 的）
	intendedPath := filepath.Join(testdataDir(), "intended", name)
	ib, ierr := os.ReadFile(intendedPath)
	intended := ierr == nil
	if intended && os.Getenv("INTENDED") != "1" {
		type core struct {
			Requests any `json:"requests"`
			Rows     any `json:"rows"`
			Instance any `json:"instance"`
		}
		var reg, bun core
		json.Unmarshal(ib, &reg)
		json.Unmarshal(gb, &bun)
		if reflect.DeepEqual(reg, bun) {
			t.Errorf("%s：Bun 的录像已经和登记的一样了，删掉 testdata/intended/%s", name, name)
		}
		gb = ib
	}
	if err := json.Unmarshal(gb, &golden); err != nil {
		t.Fatal(err)
	}

	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range c.Files {
		f := filepath.Join(p.Root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte(text), 0o644)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, e := range c.Events {
		var seg any
		if len(e.Segments) > 0 && string(e.Segments) != "null" {
			json.Unmarshal(e.Segments, &seg)
		}
		if _, err := st.AppendEvent(store.NewEvent{Channel: e.Channel, Conversation: e.Conversation, ActorKind: e.ActorKind, ActorID: e.ActorID, Origin: e.Origin, Kind: e.Kind, Text: e.Text, Segments: seg, CreatedAt: e.CreatedAt}, []string{c.CharacterID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ep := range c.Epochs {
		cols := []string{"id", "conversation", "character_id", "from_seq", "summary", "kind", "trigger", "tokens_before", "tokens_after", "dropped", "kept", "model", "status", "error", "ms", "created_at"}
		vals := make([]any, len(cols))
		for i, k := range cols {
			vals[i] = ep[k]
		}
		if _, err := st.Events.Exec(`INSERT INTO context_epochs (`+strings.Join(cols, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(cols)-1)+`)`, vals...); err != nil {
			t.Fatal(err)
		}
	}
	var last int64
	st.Events.QueryRow(`SELECT coalesce(max(seq), 0) FROM events`).Scan(&last)

	var bodies []string
	var bodiesMu sync.Mutex
	srv := gateway(t, c, &bodies, &bodiesMu)
	defer srv.Close()
	client := llm.New(llm.Magpie(srv.URL + "/v1"))
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now, err := time.Parse(time.RFC3339, c.Now)
	if err != nil {
		t.Fatal(err)
	}
	thinking := c.Agent.Thinking.Local
	if thinking == "" {
		thinking = string(config.Defaults().Agent.Thinking.Local)
	}
	recent := config.Defaults().Agent.RecentMessages
	if c.Agent.RecentMessages != nil {
		recent = *c.Agent.RecentMessages
	}
	wd, _ := Workdir(c.Workdir)
	maskWd := ""
	if c.WorkdirFiles != nil {
		wd = sameLengthTempDir(t)
		maskWd = wd
		for rel, text := range c.WorkdirFiles {
			p := filepath.Join(wd, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(text), 0o644)
		}
	}
	modelOf := map[string]string{}
	for id, m := range c.Characters {
		modelOf[id] = m.Model
	}
	// 钟每一轮可以换（next 里的 now），几个 goroutine 都在读
	var nowMu sync.Mutex
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	cfg := Config{Root: factoryRoot(t), Data: p, Thinking: thinking, RecentMessages: recent, Workdir: wd, Models: modelOf, Shell: c.Shell, Now: clock, Context: ContextLimits{SoftRatio: c.Agent.Context.SoftRatio},
		// 闲置回收按场景的钟算会和 Bun 对不上：Bun 录的时候回收看真实时间，几秒钟跑完不会放实例（影子对比的场景里两轮之间常隔几个小时）
		IdleAfter: 365 * 24 * time.Hour}
	// 工具执行前那一道关：和 Bun 的 Tools 插件一样（数据目录受保护、后台端口留空、审批模式照场景），自动放行写进这一步
	gate := testGate(p.Root)
	gate.Now = cfg.Now
	if c.Tools.ApprovalMode != "" {
		gate.Mode = c.Tools.ApprovalMode
	}
	WireGateSteps(gate, st, nil)
	if c.Tools.ApprovalMinutes != nil {
		gate.ApprovalMinutes = *c.Tools.ApprovalMinutes
	}
	// 审批：和 Bun 的 Tools 一样先登记、记"等审批"（主人怎么回答在下面，插话、停止的钩子之后）
	approvals := approval.NewApprovals(gate)
	WireApprovalSteps(approvals, st)
	cfg.Gate = gate
	// 主人插话、按停止（2f）：到了场景写的那一刻照做；停止 = 丢掉没送到的插话、取消这一轮（agent.ts stop）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type steerSeen struct {
		Phase   string `json:"phase"`
		Text    string `json:"text"`
		Requeue bool   `json:"requeue,omitempty"`
	}
	// 工具结果落库的信号：interject 的 afterTool 等它，负载再高也是先落完再停
	var finalMu sync.Mutex
	finalized := map[string]chan struct{}{}
	finalSignal := func(id string) chan struct{} {
		finalMu.Lock()
		defer finalMu.Unlock()
		ch, ok := finalized[id]
		if !ok {
			ch = make(chan struct{})
			finalized[id] = ch
		}
		return ch
	}
	cfg.OnToolFinalized = func(_ context.Context, in kernel.Invocation, _ tools.Result) error {
		close(finalSignal(in.ProviderCallID))
		return nil
	}
	var steersMu sync.Mutex
	steers := []steerSeen{}
	cfg.OnSteer = func(e SteerEvent) {
		steersMu.Lock()
		steers = append(steers, steerSeen{e.Phase, e.Text, e.Requeue})
		steersMu.Unlock()
	}
	// 中途发的、停止：不在回调里等（回调在那一轮自己的 goroutine 里），记下来最后一起等
	var hub *Hub
	var pendMu sync.Mutex
	var pending []<-chan Outcome
	act := func(do, text string) {
		switch do {
		case "steer":
			hub.Steer(c.CharacterID, text)
		case "send":
			o := hub.Send(c.CharacterID, text)
			pendMu.Lock()
			pending = append(pending, o)
			pendMu.Unlock()
		default:
			o := make(chan Outcome, 1)
			go func() { hub.Stop(c.CharacterID); o <- Outcome{} }()
			pendMu.Lock()
			pending = append(pending, o)
			pendMu.Unlock()
		}
	}
	deltaReq, deltas := 0, 0
	cfg.OnDelta = func(string) {
		bodiesMu.Lock()
		nth := len(bodies)
		bodiesMu.Unlock()
		if nth != deltaReq {
			deltaReq, deltas = nth, 0
		}
		deltas++
		for _, x := range c.Interject {
			if x.On == "delta" && x.Request == deltaReq && x.N == deltas {
				act(x.Do, x.Text)
			}
		}
	}
	cfg.OnTool = func(id, _ string) {
		for _, x := range c.Interject {
			if x.On != "tool" || x.ID != id {
				continue
			}
			// marker：等工作目录里出现这个文件（命令自己建的）再做（和 golden 脚本一样）；没有就按 delayMs 等
			if x.Marker != "" {
				go func() {
					for !exists(filepath.Join(wd, x.Marker)) {
						time.Sleep(20 * time.Millisecond)
					}
					act(x.Do, x.Text)
				}()
				continue
			}
			time.AfterFunc(time.Duration(x.DelayMs)*time.Millisecond, func() { act(x.Do, x.Text) })
		}
	}
	cards := 0
	answers := c.Approvals
	// 插话和回答属于同一订阅方：先插话再回答，不依赖独立订阅方的运行先后。
	approvals.Subscribe(func(req approval.Request) {
		cards++
		for _, x := range c.Interject {
			if x.On == "approval" && x.N == cards {
				if x.AfterTool != "" {
					select {
					case <-finalSignal(x.AfterTool):
					case <-time.After(30 * time.Second):
						t.Errorf("等不到工具 %s 落完结果", x.AfterTool)
					}
				}
				act(x.Do, x.Text)
			}
		}
		if len(answers) == 0 {
			return
		}
		a := answers[0]
		answers = answers[1:]
		// 和 golden 脚本的 setTimeout(..., 0) 一样晚一拍回答（OnRequest 里不能等这张卡片的结果）
		if a.Choice != "wait" {
			go func() {
				if _, err := approvals.Decide(req.ID, a.Choice, a.Reason); err != nil {
					t.Errorf("回答审批出错：%v", err)
				}
			}()
		}
	}, func(req approval.Request, _ approval.Decision) { hub.ApprovalResolved(req.CharacterID) })
	// 一轮一轮发（和 golden 脚本一样）：上一轮连同中途发的、停止都做完再发下一句；together 的和上一句一起发——
	// 先让第一句的回合在读卡之前停住，等后一句存好再放（Bun 里两句都在第一轮建实例之前存进库）
	hub = NewHub(ctx, cfg, anyConv{st}, client, models)
	if err := hub.Use(memoryDeclsPlugin{}); err != nil {
		t.Fatal(err)
	}
	tReady := time.Now() // 库开好、历史插完、模型列表取到、Hub 建好：能接第一句了
	var turnsMs []float64
	var errs []error
	collect := func(o Outcome) {
		if o.Err != nil {
			errs = append(errs, o.Err)
		}
	}
	type turnIn struct {
		Text, Now string
		Files     map[string]string
		Together  bool
		Restart   bool
	}
	rounds := []turnIn{{Text: c.Text}}
	for _, n := range c.Next {
		rounds = append(rounds, turnIn{n.Text, n.Now, n.Files, n.Together, n.Restart})
	}
	for i := 0; i < len(rounds); i++ {
		tn := rounds[i]
		if tn.Now != "" {
			v, err := time.Parse(time.RFC3339, tn.Now)
			if err != nil {
				t.Fatal(err)
			}
			nowMu.Lock()
			now = v
			nowMu.Unlock()
		}
		for rel, text := range tn.Files {
			os.WriteFile(filepath.Join(p.Root, filepath.FromSlash(rel)), []byte(text), 0o644)
		}
		if tn.Restart {
			hub.Restart(c.CharacterID)
		}
		var gate chan struct{}
		if i+1 < len(rounds) && rounds[i+1].Together {
			gate = make(chan struct{})
			hub.Cfg.BeforeTurn = func() { <-gate }
		}
		tTurn := time.Now()
		group := []<-chan Outcome{hub.Send(c.CharacterID, tn.Text)}
		for i+1 < len(rounds) && rounds[i+1].Together {
			i++
			group = append(group, hub.Send(c.CharacterID, rounds[i].Text))
		}
		if gate != nil {
			close(gate)
		}
		for _, o := range group {
			collect(<-o)
		}
		for {
			pendMu.Lock()
			if len(pending) == 0 {
				pendMu.Unlock()
				break
			}
			o := pending[0]
			pending = pending[1:]
			pendMu.Unlock()
			collect(<-o)
		}
		// 这一轮中途送进去的（插话变成的新一句）都收完了再撤：它们的 respond 一开头就读 h.Cfg，收完结果前改会和它抢（-race 报过）
		hub.Cfg.BeforeTurn = nil
		turnsMs = append(turnsMs, msSince(tTurn))
	}
	// 影子对比（3s）：不判对错，把 Go 的结果写成和 golden 一样的样子，交给 scripts/go-shadow.ts 比
	if os.Getenv("SHADOW") == "1" {
		rss, peak := processMemoryMB()
		measure := map[string]any{"processToCaseMs": float64(t0.Sub(processStart()).Microseconds()) / 1000, "readyMs": float64(tReady.Sub(t0).Microseconds()) / 1000,
			"turnsMs": turnsMs, "rssMB": rss, "peakMB": peak}
		writeShadow(t, name, bodies, gatherRows(t, st, last), hub, c.CharacterID, errs, maskWd, measure)
		return
	}
	if intended && os.Getenv("INTENDED") == "1" {
		writeIntended(t, intendedPath, ib, bodies, gatherRows(t, st, last), hub, c, steers, p, maskWd)
		return
	}
	if len(errs) != len(c.ExpectErrors) {
		t.Fatalf("Bun 报的错是 %v，Go 有 %d 轮返回错误：%v", c.ExpectErrors, len(errs), errs)
	}

	// 回合录像比消息、工具名称和请求选项；说明措辞、声明结构及随之变化的 token 估算不固定成逐字快照。
	want := 1
	if c.Replies != nil {
		want = len(c.Replies)
	}
	if c.ExpectNoRequest {
		want = 0
	}
	// 登记过的场景请求数由登记定（trim：冷启动超硬门槛多一次压缩请求，ADR-377）
	if intended && c.Replies == nil && !c.ExpectNoRequest {
		want = len(golden.Requests)
	}
	if len(bodies) != want || len(golden.Requests) != want {
		t.Fatalf("应该发 %d 次请求：Go 发了 %d 次，golden 里 %d 次", want, len(bodies), len(golden.Requests))
	}
	for n, body := range bodies {
		var got any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		got = mask(got, maskWd)
		r := got.(map[string]any)
		cap := r["max_completion_tokens"].(float64)
		for _, model := range models {
			if model.ID != r["model"] {
				continue
			}
			if cap < 1 || (model.MaxOutputTokens > 0 && cap > float64(model.MaxOutputTokens)) {
				t.Errorf("第 %d 次请求输出预算 %v 越过模型上限 %d", n+1, cap, model.MaxOutputTokens)
			}
			if model.ContextLength > 4096 && cap > float64(model.ContextLength-4096) {
				t.Errorf("第 %d 次请求输出预算 %v 未留上下文余量", n+1, cap)
			}
		}
		if d := diff(fmt.Sprintf("第 %d 次请求", n+1), requestContract(got.(map[string]any)), requestContract(golden.Requests[n])); d != "" {
			t.Errorf("请求行为和回归基线不一样：\n%s", d)
		}
	}

	gotRows := gatherRows(t, st, last)
	// 对拍第一层：实例里留着的消息（和 golden 脚本的 instanceOf 一样拼；只要不发下一轮，历史留错了请求里看不出来）
	var inst any
	if l := hub.liveOf(c.CharacterID); l != nil {
		inst = instanceOf(l.msgs)
	}
	if d := diff("instance", mask(toAnyV(inst), maskWd), golden.Instance); d != "" {
		t.Errorf("实例里的消息和 Bun 不一样：\n%s", d)
	}
	if c.Interject != nil {
		// 两边都先转成通用的值再序列化：键按字母排，字段先后（phase / requeue / text）不影响比较
		gj, _ := json.Marshal(toAnyV(steers))
		wj, _ := json.Marshal(golden.Steers)
		if string(gj) != string(wj) {
			t.Errorf("插话的几个阶段不一样\n Go：%s\nBun：%s", gj, wj)
		}
	}
	if len(golden.Rows) != len(gotRows) {
		t.Fatalf("golden 里有 %d 张表，这里比 %d 张：两边取的表要一样", len(golden.Rows), len(gotRows))
	}
	if c.Approvals != nil {
		var got any
		if b, err := os.ReadFile(filepath.Join(p.ConfigDir(), "tool-grants.yml")); err == nil {
			var raw struct {
				Grants any `yaml:"grants"`
			}
			if err := yaml.Unmarshal(b, &raw); err != nil {
				t.Fatal(err)
			}
			got = raw.Grants
		}
		gj, _ := json.Marshal(mask(toAnyV(got), maskWd))
		wj, _ := json.Marshal(golden.Grants)
		if string(gj) != string(wj) {
			t.Errorf("tool-grants.yml 不一样\n Go：%s\nBun：%s", gj, wj)
		}
	}
	for table, want := range golden.Rows {
		if table == "prompt_sections" {
			continue
		}
		if table == "model_runs" {
			for _, rs := range [][]map[string]any{gotRows[table], want} {
				for _, r := range rs {
					for _, col := range []string{"sections_json", "context_json", "request_json"} {
						delete(r, col)
					}
					// 压缩新铸的快照 id 每次随机（时间部分之外），不固定；预置的快照 id 照旧逐字比
					if r["stage"] == "compact" && r["epoch_id"] != nil {
						r["epoch_id"] = "<新快照>"
					}
				}
			}
		}
		g := gotRows[table]
		for i := range g {
			g[i] = mask(g[i], maskWd).(map[string]any)
		}
		normalizeJSONCols(g)
		normalizeJSONCols(want)
		if d := diff(table, toAny(g), toAny(want)); d != "" {
			t.Errorf("%s 和 Bun 不一样：\n%s", table, d)
		}
	}
}

// requestContract 保留用户/助手/工具消息和工具集合；固定提示词与声明由各自的行为验收负责。
func requestContract(r map[string]any) map[string]any {
	delete(r, "max_completion_tokens")
	messages, _ := r["messages"].([]any)
	for _, raw := range messages {
		m := raw.(map[string]any)
		if m["role"] == "developer" || m["role"] == "system" {
			delete(m, "content")
		}
	}
	decls, _ := r["tools"].([]any)
	for _, raw := range decls {
		d := raw.(map[string]any)
		f := d["function"].(map[string]any)
		d["function"] = map[string]any{"name": f["name"]}
	}
	return r
}

// gatherRows 取库里要和 golden 比的行（和 golden 脚本取的是同样的列）；turn_id 换成按出现先后编的号
// （model_runs 先、steps 后），和 golden 脚本一样：比的是"哪几行是同一轮"
func gatherRows(t *testing.T, st *store.Store, last int64) map[string][]map[string]any {
	// 摘要用于 inbox 幂等，由持久输入测试核对；聊天回归不固定随机 ClientKey 派生的内部摘要。
	// 同钟步骤保留 provider 排序，思考步骤按登记先后；随机后缀不代表正文发生次序。
	gotRows := map[string][]map[string]any{
		"events":       rows(t, st.Events, `SELECT seq, channel, conversation, actor_kind, actor_id, origin, kind, text, nullif(json_remove(segments_json,'$.kernel_input_digest'),'{}') AS segments_json, reply_to, created_at FROM events WHERE seq > ? ORDER BY seq`, last),
		"observations": rows(t, st.Events, `SELECT character_id, event_seq FROM observations WHERE event_seq > ? ORDER BY event_seq, character_id`, last),
		"model_runs": rows(t, st.Trace, `SELECT character_id, conversation, stage, model, started_at, ttft_ms, total_ms, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			reasoning_tokens, turn_id, route_provider, route_model, sections_json, injected_json, draft_json, context_json, epoch_id, rebuild, request_json, error FROM model_runs ORDER BY started_at, rowid`),
		"prompt_sections": rows(t, st.Trace, `SELECT hash, text, first_seen FROM prompt_sections ORDER BY hash`),
		"steps":           rows(t, st.Trace, `SELECT substr(coalesce(provider_call_id,id),1,6) AS id_prefix, character_id, conversation, turn_id, event_seq, kind, name, args_json, preview, full_hash, status, is_error, approval_json, started_at, ended_at, batch FROM steps ORDER BY started_at, coalesce(provider_call_id,substr(id,1,6)), rowid`),
	}
	turns := map[any]string{}
	for _, table := range []string{"model_runs", "steps"} {
		for _, r := range gotRows[table] {
			if id := r["turn_id"]; id != nil {
				if _, ok := turns[id]; !ok {
					turns[id] = fmt.Sprintf("T%d", len(turns)+1)
				}
				r["turn_id"] = turns[id]
			}
		}
	}
	return gotRows
}

// testdataDir 是场景和 golden 在哪：GOLDEN_DIR 换目录（影子对比用 data/dev/shadow/<日期>），不给是 testdata
func testdataDir() string {
	if d := os.Getenv("GOLDEN_DIR"); d != "" {
		return d
	}
	return "testdata"
}

// writeIntended 把 Go 这一次的结果写进登记（和 golden 一样的样子），留着原来的 adr、why
func writeIntended(t *testing.T, path string, old []byte, bodies []string, rs map[string][]map[string]any, hub *Hub, c testCase, steers any, p paths.Paths, maskWd string) {
	var meta struct {
		ADR string `json:"adr"`
		Why string `json:"why"`
	}
	json.Unmarshal(old, &meta)
	if meta.ADR == "" || meta.Why == "" {
		t.Fatalf("%s 要先手写 adr、why", path)
	}
	var reqs []any
	for _, body := range bodies {
		var x any
		json.Unmarshal([]byte(body), &x)
		reqs = append(reqs, mask(x, maskWd))
	}
	for table := range rs {
		for i := range rs[table] {
			rs[table][i] = mask(rs[table][i], maskWd).(map[string]any)
		}
	}
	var inst any
	if l := hub.liveOf(c.CharacterID); l != nil {
		inst = instanceOf(l.msgs)
	}
	out := map[string]any{"adr": meta.ADR, "why": meta.Why, "requests": reqs, "rows": rs, "instance": mask(toAnyV(inst), maskWd)}
	if c.Interject != nil {
		out["steers"] = toAnyV(steers)
	}
	if c.Approvals != nil {
		var grants any
		if b, err := os.ReadFile(filepath.Join(p.ConfigDir(), "tool-grants.yml")); err == nil {
			var raw struct {
				Grants any `yaml:"grants"`
			}
			yaml.Unmarshal(b, &raw)
			grants = raw.Grants
		}
		out["grants"] = mask(toAnyV(grants), maskWd)
	}
	b, err := json.MarshalIndent(toAnyV(out), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeShadow 把 Go 这边的结果写进 <场景目录>/go/<场景>：请求、库里的行、实例里的消息、每轮的错误
func writeShadow(t *testing.T, name string, bodies []string, rs map[string][]map[string]any, hub *Hub, characterID string, errs []error, maskWd string, measure map[string]any) {
	var reqs []any
	for _, body := range bodies {
		var x any
		json.Unmarshal([]byte(body), &x)
		reqs = append(reqs, mask(x, maskWd))
	}
	for table := range rs {
		for i := range rs[table] {
			rs[table][i] = mask(rs[table][i], maskWd).(map[string]any)
		}
	}
	var inst any
	if l := hub.liveOf(characterID); l != nil {
		inst = instanceOf(l.msgs)
	}
	es := []string{}
	for _, e := range errs {
		es = append(es, e.Error())
	}
	out := map[string]any{"requests": reqs, "rows": rs, "instance": mask(toAnyV(inst), maskWd), "shadow": map[string]any{"errors": es, "measure": measure}}
	b, err := json.MarshalIndent(toAnyV(out), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(testdataDir(), "go")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func toAnyV(v any) any {
	b, _ := json.Marshal(v)
	var x any
	json.Unmarshal(b, &x)
	return x
}

func toAny(rs []map[string]any) any {
	b, _ := json.Marshal(rs)
	var x any
	json.Unmarshal(b, &x)
	return x
}

// diff 列出两份解析后的 JSON 哪里不一样（最多 20 处）
func diff(path string, a, b any) string {
	var out []string
	var walk func(p string, x, y any)
	walk = func(p string, x, y any) {
		if len(out) >= 20 {
			return
		}
		switch xv := x.(type) {
		case map[string]any:
			yv, ok := y.(map[string]any)
			if !ok {
				out = append(out, fmt.Sprintf("%s：Go 是对象，Bun 是 %T", p, y))
				return
			}
			keys := map[string]bool{}
			for k := range xv {
				keys[k] = true
			}
			for k := range yv {
				keys[k] = true
			}
			ks := make([]string, 0, len(keys))
			for k := range keys {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				xe, xok := xv[k]
				ye, yok := yv[k]
				switch {
				case !xok:
					out = append(out, fmt.Sprintf("%s.%s：Go 没有，Bun 是 %s", p, k, show(ye)))
				case !yok:
					out = append(out, fmt.Sprintf("%s.%s：Go 多了，值是 %s", p, k, show(xe)))
				default:
					walk(p+"."+k, xe, ye)
				}
			}
		case []any:
			yv, ok := y.([]any)
			if !ok || len(xv) != len(yv) {
				out = append(out, fmt.Sprintf("%s：Go %d 项 %s，Bun %s", p, len(xv), show(xv), show(y)))
				if !ok {
					return
				}
			}
			for i := 0; i < len(xv) && i < len(yv); i++ {
				walk(fmt.Sprintf("%s[%d]", p, i), xv[i], yv[i])
			}
		default:
			if !reflect.DeepEqual(x, y) {
				out = append(out, fmt.Sprintf("%s：\n  Go  %s\n  Bun %s", p, show(x), show(y)))
			}
		}
	}
	walk(path, a, b)
	return strings.Join(out, "\n")
}

func show(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if r := []rune(s); len(r) > 1200 { // 够把一条消息列表的两边都看全；300 会把差在后面的项截掉，CI 日志里就看不出差在哪
		s = string(r[:1200]) + "…"
	}
	return s
}

// memoryDeclsPlugin 只挂记忆的三个工具的声明：Bun 的请求里有它们（记忆的工具排在最前），对拍的场景没有调它们的。
// 真正的注册（带执行）在记忆插件里
type memoryDeclsPlugin struct{}

func (memoryDeclsPlugin) ID() string { return "memory-decls" }

func (memoryDeclsPlugin) Setup(r *Registry) (func(), error) {
	for _, d := range tools.MemoryDecls() {
		if _, err := r.OnTool(Tool{Decl: d, Exec: func(context.Context, ToolCall) (tools.Result, error) {
			return tools.Result{}, errors.New("对拍里不调记忆工具")
		}}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// analyzer 是各场景共用的常驻 pwsh（命令拆开看）
var analyzer = policy.NewAnalyzer()

// testGate 建测试用的审批门：共用解析器，不另起预热。拆命令的时限放长：第一条命令要等 pwsh 冷启动，
// 机器忙时能到十几秒；超了会被当成"看不清"转去问主人，夹具里没人回答，回合就停在那儿等到测试超时。
func testGate(root string) *approval.Gate {
	gate := approval.NewGateWith(root, analyzer)
	gate.AnalyzeBudget = 2 * time.Minute
	return gate
}

func TestSameAsBun(t *testing.T) {
	t.Parallel()
	// -short 下照常跑：对拍管着回收站、“始终允许”这类保护（ADR-225 第 3 条）；整组约 10 秒
	entries, err := os.ReadDir(filepath.Join(testdataDir(), "cases"))
	if err != nil {
		t.Fatal(err)
	}
	// 先把拆命令的 pwsh 起好（和 golden 脚本一样）：不让冷启动算进第一条命令（规格第七节 A35）
	if os.Getenv("SHADOW") != "1" { // 影子对比不调工具，也不让起 pwsh 的这一两秒算进"进程起来"
		analyzer.Analyze("Get-Date", barrierLimit)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			n++
			t.Run(strings.TrimSuffix(e.Name(), ".json"), func(t *testing.T) {
				// 这四个场景里模型看到的工具结果带 Windows 的 CRLF 换行或反斜杠路径（基线是 Bun 在 Windows 上录的），Linux 上结果是 LF 和正斜杠，
				// 对拍比的是逐字的工具结果，不是行为；别的场景照常对拍
				switch strings.TrimSuffix(e.Name(), ".json") {
				case "tool-more", "tool-yolo", "tool-auto", "approve-always":
					if runtime.GOOS != "windows" {
						t.Skip("原因：工具结果的基线带 Windows 的 CRLF 换行或反斜杠路径")
					}
				}
				t.Parallel()
				runCase(t, e.Name())
			})
		}
	}
	if n < 5 && os.Getenv("GOLDEN_DIR") == "" { // 影子对比的场景目录里只有几个角色
		t.Fatalf("场景只有 %d 份", n)
	}
}

// mask 把工作目录的路径换成 <WORKDIR>（和 golden 脚本的 maskWorkdir 一样：原样的、正斜杠的、JSON 里转义过的都换）
func mask(v any, wd string) any {
	if wd == "" {
		return v
	}
	esc, _ := json.Marshal(wd)
	forms := []string{string(esc[1 : len(esc)-1]), wd, strings.ReplaceAll(wd, `\`, "/")}
	// 审批规则的键把路径转成了小写（policy 的规则键），这几种写法的小写形式也要换
	for _, f := range forms[:3] {
		forms = append(forms, strings.ToLower(f))
	}
	var walk func(x any) any
	walk = func(x any) any {
		switch y := x.(type) {
		case string:
			for _, f := range forms {
				y = strings.ReplaceAll(y, f, "<WORKDIR>")
			}
			return y
		case []any:
			for i := range y {
				y[i] = walk(y[i])
			}
			return y
		case map[string]any:
			for k := range y {
				y[k] = walk(y[k])
			}
			return y
		}
		return x
	}
	return walk(v)
}

// sameLengthTempDir 建一个和 golden 脚本的工作目录一样长的临时目录：系统临时目录 + sumika-golden-wd- + 6 个字
// （Bun 的 mkdtemp 就是这样起名的）。路径会出现在工具的报错里，比之前换成了 <WORKDIR>，但上下文明细里的 token 估算按原来的长度算，
// 长度不一样估算就对不上（t.TempDir 起的名字长度不一样）
func sameLengthTempDir(t *testing.T) string {
	t.Helper()
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for try := 0; try < 100; try++ {
		name := make([]byte, 6)
		for i := range name {
			name[i] = letters[rand.IntN(len(letters))]
		}
		dir := filepath.Join(os.TempDir(), "sumika-golden-wd-"+string(name))
		if err := os.Mkdir(dir, 0o755); err == nil {
			t.Cleanup(func() { os.RemoveAll(dir) })
			return dir
		}
	}
	t.Fatal("建不了临时的工作目录")
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// instanceOf 是实例里的消息写成和 golden 脚本一样的样子（系统那条不比）
func instanceOf(msgs []Msg) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, map[string]any{"role": "user", "text": m.Text})
		case "toolResult":
			out = append(out, map[string]any{"role": "toolResult", "id": m.CallID, "text": m.Text})
		case "assistant":
			x := map[string]any{"role": "assistant", "text": m.Text}
			if m.HasThinking {
				x["thinking"] = m.Thinking
			}
			if len(m.Calls) > 0 {
				var cs []map[string]any
				for _, c := range m.Calls {
					cs = append(cs, map[string]any{"id": c.ID, "name": c.Name, "args": jsjson.Stringify(c.Args)})
				}
				x["calls"] = cs
			}
			if m.Stop != "" {
				x["stop"] = m.Stop
			}
			if _, details := openaichat.ReplayParts(m.Replay); len(details) > 0 {
				var d any
				json.Unmarshal([]byte(jsjson.Stringify(jsjson.Value{Kind: jsjson.Array, Arr: details})), &d)
				x["details"] = d
			}
			if u := m.Usage; u != nil && (u.Input != 0 || u.Output != 0 || u.CacheRead != 0 || u.CacheWrite != 0) {
				x["usage"] = map[string]any{"input": u.Input, "output": u.Output, "cacheRead": u.CacheRead, "cacheWrite": u.CacheWrite}
			}
			out = append(out, x)
		}
	}
	return out
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
