// memory-eval 用 Go 版的记忆召回答记忆考题（规格-记忆-v2 第十一节、13.1 第 ⑤ 块），和 sumika-bun 仓库的 scripts/memory-eval/run.ts（Bun 版的基线）同一份题、同样的指标：
//
//	召回：目标条目在前 1 / 5 / 8 / 20 的有几道（"排名"：明确回忆、不受门控）、日常那样过门控限字数以后真的给了的有几道，按条目种类分开；
//	寒暄：一两个字的短消息按它所在会话的场合查，有几道给了东西、平均给多少字；
//	越权：同一道题在本地群聊、QQ 群两个场合查，给出来的条目里范围不在可见集合里的有几条（硬条件 0）；
//	耗时：P50 / P95。
//
// 只读：把角色的 memory.db 和 events.db 拷进临时目录再用；旁边的 memory-v2.db 建在临时目录里（SidecarPath 跟着那份副本走，每次运行都是新建的，
// 换了模型维度不会拿上一次的向量来比）。嵌入默认读 sumika.yml 的 embed 一节（config 包，和后台装配同一处解析）自己起一份子进程：
// 起 sumika-models.exe（规格-Go本地模型 GM7；modelsExe 没写就要给 -models-exe，这里没有资源根）；
// 给了 -embed-url 就连一个已经在跑的本地模型服务（规格-本地模型服务，手起的 sumika-models 也一样），不起子进程，查询和条目分别带 input_type=query / document。
// 重排默认不开；给了 -rerank-url / -rerank-model 就在合并出 -rerank-k 条以后调一次本地模型服务的 /v1/rerank
//
//	go run ./cmd/memory-eval -data <仓库>\data
//	go run ./cmd/memory-eval -data … -embed-url http://127.0.0.1:11440/v1 -embed-model qwen3-embedding-0.6b
//	go run ./cmd/memory-eval -data …                       （sumika.yml 里 embed.backend: go-models，modelsExe 写了）
//	go run ./cmd/memory-eval -data … -models-exe D:\x\sumika-models.exe -ort-library D:\x\onnxruntime.dll
//	go run ./cmd/memory-eval -data … -embed-url … -embed-model … -rerank-url … -rerank-model bge-reranker-v2-m3
//	go run ./cmd/memory-eval -data … -exam <考题.json> -novec
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/config"
	"sumika-native/internal/embed"
	"sumika-native/internal/embedinputs"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/store/memsource"
)

type exam struct {
	Character string `json:"character"`
	Items     []struct {
		Ref          string `json:"ref"`
		Kind         string `json:"kind"`
		Status       string `json:"status"`
		Conversation string `json:"conversation"`
		Noise        bool   `json:"noise"`
	} `json:"items"`
	Questions []struct {
		Q            string   `json:"q"`
		Refs         []string `json:"refs"`
		Type         string   `json:"type"`
		Conversation string   `json:"conversation"`
	} `json:"questions"`
}

func main() {
	dataDir := flag.String("data", "", "主工作区的 data 目录（只从这里拷库）")
	examFile := flag.String("exam", "", "考题 JSON；不给就用 data/dev/memory-eval/ 下最新的 *-v0-*.json")
	noVec := flag.Bool("novec", false, "不用向量，只用全文")
	embedURL := flag.String("embed-url", "", "已经在跑的 OpenAI 兼容嵌入服务的 base（含 /v1，如 http://127.0.0.1:11440/v1）；给了就不起本机子进程")
	embedModel := flag.String("embed-model", "", "上面那个服务里的模型 id（如 qwen3-embedding-0.6b；照 /v1/models）")
	embedTimeout := flag.Duration("embed-timeout", 10*time.Minute, "连 -embed-url 那个服务时一次嵌入请求最多等多久。本地服务一次前向只放一条，一批 64 条在慢模型上要几分钟（pplx 实测 192 秒）；超时走人服务那边也会停")
	rerankURL := flag.String("rerank-url", "", "已经在跑的本地模型服务做重排的 base（含 /v1，如 http://127.0.0.1:11440/v1）；给了就连它重排，不给就不重排")
	rerankModel := flag.String("rerank-model", "", "上面那个服务里的重排模型 id（如 bge-reranker-v2-m3；照 /v1/models）")
	rerankK := flag.Int("rerank-k", memory.RerankK, "合并出前多少条交给重排（起步 40，只改顺序，后面的照原样接上）")
	topK := flag.Int("topk", 0, "明确回忆取前多少条给出来：线上 memory_search 带 TopK 12，走 Hits 的另一条分支；重排 A/B 要用同一条分支就给 12。0 = 不带（评测原来的筛选分支，不变）")
	tag := flag.String("tag", "go-v2", "结果文件名里的标签")
	modelsExe := flag.String("models-exe", "", "sumika-models.exe 的绝对路径，盖过 sumika.yml 的 embed.modelsExe（只有 embed.backend 是 go-models 才用；两处都空就起不来，评测没有资源根可以找默认的）")
	ortLibrary := flag.String("ort-library", "", "onnxruntime.dll 的绝对路径，盖过 embed.ortLibrary（go-models）；空 = 取 sumika-models.exe 所在目录里的")
	modelsRoot := flag.String("models-root", "", "模型根目录的绝对路径，盖过 embed.modelsRoot（go-models）")
	groupN := flag.Int("group", 300, "从事件库副本抽多少条真实的 QQ 群消息（长短都有）照日常那样想一遍")
	dumpPath := flag.String("dump-embed-inputs", "", "不连任何模型，用假嵌入器和假重排把跑一遍会送给它们的字符串（条目、查询、群消息、守卫的句子、重排的查询和候选）原样写到这个文件，给 models-parity 的真实样本用；"+
		"文件里是真实记忆和聊天的原文，只许写在临时目录或被 git 忽略的路径，用完自己删")
	flag.Parse()
	if *dataDir == "" {
		fail("要给 -data（主工作区的 data 目录）")
	}
	if *rerankURL != "" && *rerankModel == "" {
		fail("-rerank-url 要配 -rerank-model（模型的 id 照本地模型服务的 /v1/models）")
	}
	if *rerankURL == "" && *rerankModel != "" {
		fail("-rerank-model 要配 -rerank-url（重排连的是已经在跑的服务）")
	}
	var rec *dumpRecorder
	if *dumpPath != "" {
		if *embedURL != "" || *rerankURL != "" || *noVec {
			fail("-dump-embed-inputs 用的是假嵌入器和假重排，不能和 -embed-url、-rerank-url、-novec 一起给")
		}
		if err := embedinputs.CheckPrivatePath(*dumpPath); err != nil {
			fail(err.Error())
		}
		rec = newDumpRecorder(maxDumpRerank)
		// 这一趟的召回数字是假向量算出来的，没有意义：别让它们打到终端上被当成真的（stderr 的进度行照旧）
		realStdout := os.Stdout
		if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			os.Stdout = null
			defer func() { os.Stdout = realStdout; null.Close() }()
		}
	}
	evalDir := filepath.Join(*dataDir, "dev", "memory-eval")
	if *examFile == "" {
		ms, _ := filepath.Glob(filepath.Join(evalDir, "*-v0-*.json"))
		sort.Strings(ms)
		if len(ms) == 0 {
			fail("没有考题，先在 sumika-bun 仓库里跑 bun scripts/memory-eval/make.ts（2026-11-01 分仓以后出题的脚本在那边）")
		}
		*examFile = ms[len(ms)-1]
	}
	var ex exam
	b, err := os.ReadFile(*examFile)
	must(err)
	must(json.Unmarshal(b, &ex))

	tmp, err := os.MkdirTemp("", "sumika-go-memeval-")
	must(err)
	defer os.RemoveAll(tmp)
	// 路径一律经 paths（只读打开数据目录的身份证，不写）：Bun 的库是所有平台一个 events.db（paths.BunEventsDB，ADR-199）
	dp, err := paths.Open(*dataDir)
	must(err)
	memFile := filepath.Join(tmp, "memory.db")
	copyDb(dp.MemoryDB(ex.Character), memFile)
	sharedFile := filepath.Join(tmp, filepath.Base(dp.OwnerMemoryDB()))
	sharedSource := dp.OwnerMemoryDB()
	if _, err := os.Stat(sharedSource); err == nil {
		copyDb(sharedSource, sharedFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		must(err)
	}
	evFile := filepath.Join(tmp, "events.db")
	copyDb(dp.BunEventsDB(), evFile)
	roster, err := memsource.ReadQQPeople(evFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读不了 qq_people，别名表留空：", err)
	}
	people := roster.People

	ctx := context.Background()
	var ev *evalEmbedder
	if *noVec && *embedURL != "" {
		fail("给了 -embed-url 又给了 -novec：不知道要哪个")
	}
	if rec != nil {
		ev = newEvalEmbedder(rec.Embedder())
	} else if !*noVec {
		var stop func()
		ev, stop = setUpEmbedder(embedSetup{url: *embedURL, model: *embedModel, timeout: *embedTimeout, dataDir: *dataDir,
			modelsExe: *modelsExe, ortLibrary: *ortLibrary, modelsRoot: *modelsRoot}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
		defer stop()
	}
	var er *evalReranker
	var rr memory.Reranker
	if *rerankURL != "" {
		er = newEvalReranker(embed.RerankConnect(*rerankURL, *rerankModel, *embedTimeout), *rerankModel)
		rr = er
	}
	if rec != nil {
		rr = rec.Reranker()
	}
	t0 := time.Now()
	var poolEmbed embed.Embedder
	if ev != nil {
		poolEmbed = ev
	}
	pool, err := memory.OpenOwnerPool(ctx, sharedFile, poolEmbed, people, memory.Options{})
	must(err)
	if pool != nil {
		defer pool.Close()
	}
	m := openMemory(ctx, memFile, ev, rr, *rerankK, people, pool)
	defer m.Close()
	// 每次召回都从这儿走：记下重排没排成、退回原顺序的原因（「顺序不对」只有这儿看得见）
	skipped := map[string]int{}
	recall := func(a memory.Audience, q memory.Query) memory.Recalled {
		r := m.Recall(ctx, a, withTopK(q, *topK))
		if r.RerankSkipped != "" {
			skipped[r.RerankSkipped]++
		}
		return r
	}
	fmt.Fprintf(os.Stderr, "打开、补齐索引用了 %d ms\n", time.Since(t0).Milliseconds())

	class := map[string]string{}
	for _, it := range ex.Items {
		switch {
		case it.Kind == "episode" && it.Noise:
			class[it.Ref] = "片段（自称闲聊）"
		case it.Kind == "episode":
			class[it.Ref] = "片段"
		case it.Status == "candidate":
			class[it.Ref] = "待确认"
		default:
			class[it.Ref] = "有效事实"
		}
	}
	owner := memory.Audience{Channel: "console", Conversation: "console:" + ex.Character, Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
	localGroup := memory.Audience{Channel: "console", Conversation: "console:group:eval", Scene: "group", PersonIDs: []string{"owner"}, PrivateOK: true}
	groupConv := "qq:group:0"
	for _, it := range ex.Items {
		if strings.HasPrefix(it.Conversation, "qq:group:") {
			groupConv = it.Conversation
			break
		}
	}
	qqGroup := memory.Audience{Channel: "qq", Conversation: groupConv, Scene: "group"}
	audienceOf := func(conv string) memory.Audience {
		switch {
		case strings.HasPrefix(conv, "qq:group:"):
			return memory.Audience{Channel: "qq", Conversation: conv, Scene: "group"}
		case strings.HasPrefix(conv, "qq:private:"):
			parts := strings.Split(conv, ":")
			return memory.Audience{Channel: "qq", Conversation: conv, Scene: "private", PersonIDs: []string{parts[len(parts)-1]}}
		case strings.HasPrefix(conv, "console:group"):
			a := localGroup
			a.Conversation = conv
			return a
		}
		return owner
	}

	type row struct {
		Q, Type, Target, Class string
		Rank                   int
		Given                  bool
	}
	var rows []row
	var ms []float64
	leaks, leakChecks := 0, 0
	// 主人问过后群友再问：同一个 QQ 群、同一句话，主人本人问和群友问各查一次（确定性，不调模型）
	askedGroup := qqGroup
	askedGroup.OwnerAsked, askedGroup.PersonIDs = true, []string{"owner"}
	peerGroup := qqGroup
	peerGroup.PersonIDs = []string{"qq:evalpeer"}
	oaN, oaOwnerLines, oaUnmarked, oaPeerRecalled, oaDisclosureGaps := 0, 0, 0, 0, 0
	chitN, chitGave, chitChars := 0, 0, 0
	for _, q := range ex.Questions {
		if q.Type == "chitchat" {
			r := recall(audienceOf(q.Conversation), memory.Query{Text: q.Q})
			chitN++
			if len(r.Hits) > 0 {
				chitGave++
				for _, h := range r.Hits {
					chitChars += len([]rune(h.Text))
				}
			}
			continue
		}
		target := q.Refs[0]
		t := time.Now()
		ranked := recall(owner, memory.Query{Text: q.Q, Mode: memory.ModeExplicit})
		ms = append(ms, float64(time.Since(t).Microseconds())/1000)
		rank := 0
		for i, h := range ranked.Candidates {
			if h.Ref == target {
				rank = i + 1
				break
			}
		}
		daily := recall(owner, memory.Query{Text: q.Q})
		given := false
		for _, h := range daily.Hits {
			given = given || h.Ref == target
		}
		rows = append(rows, row{q.Q, q.Type, target, class[target], rank, given})
		{
			oaN++
			asked := recall(askedGroup, memory.Query{Text: q.Q, Mode: memory.ModeExplicit})
			ownerRefs := map[string]bool{}
			nOwner := 0
			for _, h := range append(append([]memory.Hit{}, asked.Pinned...), asked.Hits...) {
				if h.OwnerOnly {
					ownerRefs[h.Ref] = true
					nOwner++
				}
			}
			oaOwnerLines += nOwner
			// 标记文字抄 memory/render.go 的 ownerMarkText；Render 出来带标记的行数要等于 OwnerOnly 的条数
			marked := 0
			for _, l := range memory.Render(asked, time.Now()).Lines(true) {
				if strings.Contains(l, "所有者私下说的，他问才答：") {
					marked++
				}
			}
			oaUnmarked += max(0, nOwner-marked)
			peer := recall(peerGroup, memory.Query{Text: q.Q, Mode: memory.ModeExplicit})
			for _, h := range append(append(append([]memory.Hit{}, peer.Pinned...), peer.Hits...), peer.Candidates...) {
				if ownerRefs[h.Ref] {
					oaPeerRecalled++
					blocked, err := m.CheckOutbound(ctx, h.Text, peerGroup)
					must(err)
					if len(blocked) == 0 {
						oaDisclosureGaps++
					}
				}
			}
		}
		for _, a := range []memory.Audience{localGroup, qqGroup} {
			allowed := memory.AllowedScopes(a)
			for _, h := range recall(a, memory.Query{Text: q.Q, Mode: memory.ModeExplicit}).Hits {
				leakChecks++
				if !memory.CanSee(h.Scopes, allowed) {
					leaks++
				}
			}
		}
	}

	pct := func(a, b int) string {
		if b == 0 {
			return "0/0"
		}
		return fmt.Sprintf("%d/%d（%d%%）", a, b, (100*a+b/2)/b)
	}
	nQ := map[string]int{}
	for _, r := range rows {
		nQ[r.Type]++
	}
	embedLine := "不用（只用全文）"
	if ev != nil {
		embedLine = ev.head()
	}
	rerankLine := "不用"
	if er != nil {
		rerankLine = fmt.Sprintf("%s（看前 %d 条）", er.head(), *rerankK)
	}
	fmt.Printf("\n考题 %s\n嵌入：%s\n重排：%s\n题目：换说法 %d、短的 %d、寒暄 %d\n", *examFile, embedLine, rerankLine, nQ["paraphrase"], nQ["short"], chitN)
	summary := map[string]any{}
	for _, cls := range []string{"有效事实", "待确认", "片段", "片段（自称闲聊）"} {
		for _, typ := range []string{"paraphrase", "short"} {
			var rs []row
			for _, r := range rows {
				if r.Class == cls && r.Type == typ {
					rs = append(rs, r)
				}
			}
			if len(rs) == 0 {
				continue
			}
			at := func(k int) (n int) {
				for _, r := range rs {
					if r.Rank > 0 && r.Rank <= k {
						n++
					}
				}
				return
			}
			given := 0
			for _, r := range rs {
				if r.Given {
					given++
				}
			}
			label := "换说法"
			if typ == "short" {
				label = "短的　"
			}
			fmt.Printf("%s %s  前1 %s  前5 %s  前8 %s  前20 %s  日常真的给了 %s\n", padCJK(cls, 8), label, pct(at(1), len(rs)), pct(at(5), len(rs)), pct(at(8), len(rs)), pct(at(20), len(rs)), pct(given, len(rs)))
			summary[cls+"/"+typ] = map[string]int{"题数": len(rs), "前1": at(1), "前5": at(5), "前8": at(8), "前20": at(20), "日常真的给了": given}
		}
	}
	sort.Float64s(ms)
	p := func(x float64) float64 {
		if len(ms) == 0 {
			return 0
		}
		return ms[min(len(ms)-1, int(float64(len(ms))*x))]
	}
	avg := 0
	if chitGave > 0 {
		avg = chitChars / chitGave
	}
	fmt.Printf("寒暄：给了东西的 %s，给了的平均 %d 字\n", pct(chitGave, chitN), avg)
	fmt.Printf("主人问过后群友再问：%d 题，主人问时私事 %d 条、缺披露提醒 %d 条；群友再问召回 %d 条，字面披露检查缺口 %d 条（硬条件 0）\n", oaN, oaOwnerLines, oaUnmarked, oaPeerRecalled, oaDisclosureGaps)
	fmt.Printf("越权：%d 条（查了 %d 条结果；用的是召回自己那个可见集合函数，只算自检，真正的越权测试在 internal/memory 的单元测试里）\n", leaks, leakChecks)

	// 真实的 QQ 群消息（长短都有）：照日常那样想一遍，量"每条给了多少字"，和 Bun v1 的实测（697 次回复 672 次给了、平均 740 字）同一种量法
	if rec != nil {
		rec.SetPhase(phaseGroup)
	}
	msgs := groupMessages(evFile, ex.Character, *groupN)
	gGave, gChars, gAlias := 0, 0, 0
	gReasons := map[string]int{}
	for _, gm := range msgs {
		r := recall(memory.Audience{Channel: "qq", Conversation: gm.conv, Scene: "group"}, memory.Query{Text: gm.text})
		if r.Gated {
			gReasons["拦下"]++
		} else {
			gReasons["放行"+r.GateReason]++
		}
		if len(r.Hits) > 0 {
			gGave++
			for _, h := range r.Hits {
				gChars += len([]rune(h.Text))
				if h.Alias {
					gAlias++
				}
			}
		}
	}
	if rec != nil {
		finishDump(rec, *dumpPath, len(msgs))
		return
	}
	fmt.Printf("真实 QQ 群消息 %d 条（长短都有）：给了记忆的 %s，平均每条 %d 字（给了的那些平均 %d 字）；别名带进来的条目 %d 条；门控 %v\n",
		len(msgs), pct(gGave, len(msgs)), gChars/max(len(msgs), 1), gChars/max(gGave, 1), gAlias, gReasons)
	aliasReport(m, evFile, ex.Character)
	fmt.Printf("耗时（一次召回，含嵌入）：P50 %.0f ms、P95 %.0f ms\n", p(0.5), p(0.95))
	if ev != nil {
		fmt.Printf("嵌入（整个跑完）：%s\n", ev.line())
	}
	if er != nil {
		fmt.Printf("重排（整个跑完）：%s；没排成退回原顺序 %s\n", er.line(), skipText(skipped))
	}

	out := filepath.Join(evalDir, "runs", time.Now().Format("2006-01-02-15-04-05")+"-"+*tag+map[bool]string{true: "-novec", false: ""}[*noVec]+".json")
	os.MkdirAll(filepath.Dir(out), 0o755)
	embedStats := map[string]any{"model": "不用（只用全文）"}
	if ev != nil {
		embedStats = ev.stats()
	}
	rerankStats := map[string]any{"model": "不用"}
	if er != nil {
		rerankStats = er.stats()
		rerankStats["k"] = *rerankK
	}
	j, _ := json.MarshalIndent(map[string]any{"exam": *examFile, "tag": *tag, "vec": !*noVec, "embed": embedStats, "rerank": rerankStats, "rerankSkipped": skipped, "summary": summary, "chit": map[string]int{"n": chitN, "gave": chitGave, "chars": chitChars}, "leaks": leaks, "ownerAsked": map[string]int{"n": oaN, "ownerLines": oaOwnerLines, "unmarked": oaUnmarked, "peerRecalled": oaPeerRecalled, "disclosureGaps": oaDisclosureGaps}, "leakChecks": leakChecks, "p50": p(0.5), "p95": p(0.95), "rows": rows}, "", " ")
	must(os.WriteFile(out, j, 0o644))
	fmt.Printf("明细写到 %s\n", out)
}

type groupMsg struct{ conv, text string }

// groupMessages 从事件库副本里抽她看到过的、真人发的 QQ 群消息（最近 6000 条里按间隔取，长短都有）
func groupMessages(evFile, char string, n int) []groupMsg {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(evFile)+"?mode=ro")
	must(err)
	defer db.Close()
	rows, err := db.Query(`SELECT e.conversation, e.text FROM events e JOIN observations o ON o.event_seq = e.seq AND o.character_id = ?
		WHERE e.kind = 'message' AND e.actor_kind IN ('owner', 'peer', 'human') AND e.conversation LIKE 'qq:group:%' ORDER BY e.seq DESC LIMIT 6000`, char)
	must(err)
	defer rows.Close()
	var all []groupMsg
	for rows.Next() {
		var g groupMsg
		rows.Scan(&g.conv, &g.text)
		all = append(all, g)
	}
	if len(all) <= n {
		return all
	}
	out := make([]groupMsg, n)
	for i := range out {
		out[i] = all[i*len(all)/n]
	}
	return out
}

// aliasReport 别名会不会误触发：几个字的别名各有多少，最近 6000 条群消息里含任一别名的有几条，撞得最多的几个（只写字数和次数，不打名字）
func aliasReport(m *memory.Memory, evFile, char string) {
	msgs := groupMessages(evFile, char, 6000)
	hits := map[string]int{}
	anyHit := 0
	for _, g := range msgs {
		hs := m.AliasesIn(g.text)
		if len(hs) > 0 {
			anyHit++
		}
		for _, a := range hs {
			hits[a]++
		}
	}
	type kv struct {
		k string
		v int
	}
	var top []kv
	for k, v := range hits {
		top = append(top, kv{k, v})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
	var tops []string
	for _, t := range top[:min(len(top), 12)] {
		tops = append(tops, fmt.Sprintf("%d字×%d", len([]rune(t.k)), t.v))
	}
	fmt.Printf("别名：%v；最近 %d 条群消息里含任一别名的 %d 条（%.1f%%）；撞得最多的：%s\n", m.AliasStats(), len(msgs), anyHit, 100*float64(anyHit)/float64(max(len(msgs), 1)), strings.Join(tops, "、"))
}

func padCJK(s string, n int) string {
	if k := len([]rune(s)); k < n {
		return s + strings.Repeat("　", n-k)
	}
	return s
}

// skipText 是「重排没排成、这一轮退回原顺序」那一段：一共几次，后面按原因分开（超时、出错、顺序不对）
func skipText(m map[string]int) string {
	n := 0
	for _, v := range m {
		n += v
	}
	if n == 0 {
		return "0 次"
	}
	var why []string
	for _, k := range []string{"超时", "出错", "顺序不对", "未就绪"} {
		if v := m[k]; v > 0 {
			why = append(why, fmt.Sprintf("%s %d", k, v))
		}
	}
	return fmt.Sprintf("%d 次（%s）", n, strings.Join(why, "、"))
}

// copyDb 连 -wal、-shm 一起拷（真实的库只被拷贝、不被打开）
func copyDb(from, to string) {
	for _, ext := range []string{"", "-wal", "-shm"} {
		src, err := os.Open(from + ext)
		if err != nil {
			if ext == "" {
				fail("拷不了 " + from + "：" + err.Error())
			}
			continue
		}
		dst, err := os.Create(to + ext)
		must(err)
		_, err = io.Copy(dst, src)
		must(err)
		src.Close()
		dst.Close()
	}
}

// embedSetup 是这次怎么算向量的参数：url 非空就连外面的服务；否则按 dataDir 里的 sumika.yml 起本机子进程，
// modelsExe、ortLibrary、modelsRoot 补或盖配置里的
type embedSetup struct {
	url, model                        string
	timeout                           time.Duration // 连外面服务时一次嵌入请求最多等多久（-embed-timeout）
	dataDir                           string
	modelsExe, ortLibrary, modelsRoot string
}

// setUpEmbedder 决定这次怎么算向量：给了 url 就连已经在跑的服务（不起子进程、不读 sumika.yml），
// timeout 是这个服务的嵌入请求上限（-embed-timeout；本机子进程那条路照 NewClient 的 30 秒）；
// 否则照 sumika.yml 的 embed 一节自己起一份子进程（embedLocalConfig）。stop 只管子进程那条路，连着外面服务的没事可做
func setUpEmbedder(o embedSetup, logf func(string, ...any)) (e *evalEmbedder, stop func()) {
	if o.url != "" {
		if o.model == "" {
			fail("-embed-url 要配 -embed-model（模型的 id 照本地模型服务的 /v1/models）")
		}
		return newEvalEmbedder(embed.Connect(o.url, o.model, o.timeout)), func() {}
	}
	cfg, err := embedLocalConfig(o)
	must(err)
	l := embed.NewLocal(cfg, logf)
	l.Start()
	if !l.Wait(180 * time.Second) {
		l.Stop()
		s, why := l.Status()
		fail(fmt.Sprintf("嵌入没起来：%s %s", s, why))
	}
	return newEvalEmbedder(l), l.Stop
}

// embedLocalConfig 是不带 -embed-url 时本机子进程的配置：读 sumika.yml 的 embed 一节（config 包，和后台装配同一处解析和检查），
// 再由 embed.LocalConfigFrom 变成 LocalConfig；modelsRoot 空和相对路径都按数据目录解析。
// 评测没有资源根，所以 sumika-models.exe 要么写在 embed.modelsExe，要么给 -models-exe；命令行的三个路径盖过配置，不盖空的
func embedLocalConfig(o embedSetup) (embed.LocalConfig, error) {
	c, err := config.LoadIn("", o.dataDir)
	if err != nil {
		return embed.LocalConfig{}, fmt.Errorf("读配置：%w", err)
	}
	if c.Source == "" {
		return embed.LocalConfig{}, fmt.Errorf("在 %s 里找不到 config/sumika.yml（SUMIKA_CONFIG 也没设）：没有配置就不知道嵌入怎么起，要么给 -embed-url", o.dataDir)
	}
	cfg := embed.LocalConfigFrom(c.Embed, c.Memory.Rerank.Model, "", o.dataDir)
	// legacy / 不认识的后端：LocalConfigFrom 不给 go-models 拼路径，命令行也不该把路径塞进来（Start 只报原因，用不到）
	if cfg.Backend != embed.BackendGoModels {
		return cfg, nil
	}
	for _, ov := range []struct {
		val string
		to  *string
	}{{o.modelsExe, &cfg.Exe}, {o.ortLibrary, &cfg.OrtLibrary}, {o.modelsRoot, &cfg.ModelsRoot}} {
		if ov.val != "" {
			abs, err := filepath.Abs(ov.val)
			if err != nil {
				return embed.LocalConfig{}, err
			}
			*ov.to = abs
		}
	}
	return cfg, nil
}

// openMemory 只读打开记忆库。Open 里那一次补向量算的是条目的向量（input_type=document），
// 返回以后 memory-eval 只查不写，之后的嵌入都是查询（query）：只读打开时没有写入那一侧，也就没有补向量的协程。
// rr 为空就是不重排，rerankK 为零用 memory.RerankK（40）
func openMemory(ctx context.Context, memFile string, ev *evalEmbedder, rr memory.Reranker, rerankK int, people []memory.Person, pool *memory.Memory) *memory.Memory {
	var e embed.Embedder
	if ev != nil {
		e = ev
	}
	m, err := memory.Open(ctx, memFile, e, people, memory.Options{Reranker: rr, RerankK: rerankK, Shared: pool})
	must(err)
	if ev != nil {
		ev.setQuery()
	}
	return m
}

func must(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// withTopK 给明确回忆的查询带上 -topk：线上 memory_search 带 TopK 12，走 Recall 里 Hits 的另一条分支；
// 重排 A/B 要走同一条分支才比得准。0 或者不是明确回忆的查询原样返回（评测原来的筛选分支不变）
func withTopK(q memory.Query, topK int) memory.Query {
	if q.Mode == memory.ModeExplicit && topK > 0 {
		q.TopK = topK
	}
	return q
}
