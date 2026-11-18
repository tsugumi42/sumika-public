package channel

// 闸门的回放验收（规格-渠道中间件 第十五节第 5 步，规格-群聊闸门 第九节、10.4 第 3 步，主人 2026-10-27 同意用历史记录）：
// 真实群聊记录按时间一条条喂给 Go 版闸门（真的入库、真的流水线、真的闸门），放行就当她 5 秒后回了一句，
// 按小时比「她实际说了几次」和「目标几次」。和 scripts/replay-gate.ts（Bun 版）用同样的口径、同一份副本。
//
// 只读副本、不发任何东西、不调模型。没给 SUMIKA_GATE_REPLAY_DB（主人库 events.db 的副本，不是原文件）就跳过，所以平时的 go test 不碰真实数据。
// 输出只有统计数字和"群 1 / 群 2 / 群 3"，不含群名、昵称、号码、聊天原文。
//
//	SUMIKA_GATE_REPLAY_DB     events.db 的副本（同目录的 -wal、-shm 一起复制）
//	SUMIKA_GATE_REPLAY_ALIAS  角色的名字（Bun 版"叫她名字算叫到"；Go 版走别名表，这里把名字当别名，两边口径一样）
//	SUMIKA_GATE_REPLAY_FREQ   频率，默认 0.3
//	SUMIKA_GATE_REPLAY_JITTER 门槛抖动的幅度，默认 0.03；0 是不抖（和 Bun 版 JITTER=0 逐项比）
//	SUMIKA_GATE_REPLAY_SEED   抖动的随机种子，默认 1
//	SUMIKA_GATE_REPLAY_MODE   compat：照 Bun 回放脚本的口径（@全体算叫到她、burst 放行的不回话、关保险丝），逐项和 Bun 版比；
//	                          strict：按规格（@全体不算、burst 放行的也回话、开保险丝），默认
//	SUMIKA_GATE_REPLAY_GATEMODE 档位：speak（说话，默认）、dry-run（试运行）、record（只记录）
//	SUMIKA_GATE_REPLAY_OUT    把每个群的数写成 JSON 放到这个文件

import (
	"database/sql"
	"encoding/json"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// replayClock 是回放的钟：由回放按事件的时间拨；计时器不自己走（合并一串 @ 由回放按事件时间 FlushBursts）
type replayClock struct{ ms atomic.Int64 }

func (c *replayClock) Now() time.Time { return time.UnixMilli(c.ms.Load()) }
func (c *replayClock) NewTimer(time.Duration) types.Timer {
	return idleTimer{}
}

type idleTimer struct{}

func (idleTimer) C() <-chan time.Time { return nil }
func (idleTimer) Stop() bool          { return true }

// ReplayGroup 是一个群的结果（写进 JSON 的）
type ReplayGroup struct {
	Group    int     `json:"group"`
	Messages int     `json:"messages"`
	Hours    int     `json:"hours"`
	Entered  int     `json:"entered"`
	Forced   int     `json:"forced"`
	Merged   int     `json:"merged"`
	Enter    int     `json:"enter"`
	Skip     int     `json:"skip"`
	Backoff  int     `json:"backoff"`
	Muted    int     `json:"muted"`
	Fuse     int     `json:"fuse"`
	Off      int     `json:"off"`       // 档位是只记录，闸门不看
	AvgEnter float64 `json:"avgEnterP"` // 过门槛放行的那些判断，可能性的平均值
	Judged   int     `json:"judged"`
	SumA     int     `json:"sumA"`
	SumT     float64 `json:"sumT"`
	Ratio    float64 `json:"ratio"`
	Over     int     `json:"over"` // 说到 1.5 倍以上的小时占几成（整数百分比）
	Pass     bool    `json:"pass"`
	// PerHour 每个有目标的小时一行：[判断条数, 其中放行几次, 窗口里预计几次, 目标几次, 过去 60 分钟说了几次]（只有数字）
	PerHour [][5]float64 `json:"perHour,omitempty"`
}

type replayRow struct {
	ID        string
	Seq       int64
	ActorKind string
	ActorID   string
	Origin    string
	Kind      string
	Text      string
	Segs      sql.NullString
	ReplyTo   sql.NullString
	CreatedAt int64
}

// bunSegs 是 Bun 版存的段：{"segs":[{"type":"at","qq":"123"}...],"mentions":[...]}
type bunSegs struct {
	Segs []struct {
		Type string `json:"type"`
		QQ   string `json:"qq"`
		Text string `json:"text"`
	} `json:"segs"`
	Mentions []string `json:"mentions"`
	Notice   string   `json:"notice"`
}

func TestGateReplay(t *testing.T) {
	t.Parallel()
	src := os.Getenv("SUMIKA_GATE_REPLAY_DB")
	if src == "" {
		t.Skip("没给 SUMIKA_GATE_REPLAY_DB（主人库的副本）：不回放")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	num := func(k string, def float64) float64 {
		f, err := strconv.ParseFloat(env(k, ""), 64)
		if err != nil {
			return def
		}
		return f
	}
	freq, jitter, seed := num("SUMIKA_GATE_REPLAY_FREQ", 0.3), num("SUMIKA_GATE_REPLAY_JITTER", 0.03), uint64(num("SUMIKA_GATE_REPLAY_SEED", 1))
	alias := env("SUMIKA_GATE_REPLAY_ALIAS", "")
	compat := env("SUMIKA_GATE_REPLAY_MODE", "strict") == "compat"
	gateMode := env("SUMIKA_GATE_REPLAY_GATEMODE", ModeSpeak)
	if gateMode != ModeSpeak && gateMode != ModeDryRun && gateMode != ModeRecord {
		t.Fatalf("档位 %q 不认识", gateMode)
	}

	// 副本：只读打开
	old, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(src, "\\", "/")+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)

	type grp struct {
		Conv string
		N    int
	}
	var groups []grp
	rs, err := old.Query(`SELECT conversation, count(*) n FROM events WHERE conversation LIKE 'qq:group:%' AND kind = 'message' GROUP BY conversation ORDER BY n DESC LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var g grp
		if err := rs.Scan(&g.Conv, &g.N); err != nil {
			t.Fatal(err)
		}
		groups = append(groups, g)
	}
	rs.Close()
	if len(groups) == 0 {
		t.Fatal("副本里没有 QQ 群")
	}
	var characterID string
	if err := old.QueryRow(`SELECT character_id FROM observations o JOIN events e ON e.seq = o.event_seq WHERE e.conversation LIKE 'qq:group:%' LIMIT 1`).Scan(&characterID); err != nil {
		t.Fatal(err)
	}

	var results []ReplayGroup
	for gi, g := range groups {
		results = append(results, replayOne(t, old, g.Conv, g.N, gi+1, characterID, alias, freq, jitter, seed, compat, gateMode))
	}
	label := "strict（按规格）"
	if compat {
		label = "compat（照 Bun 回放脚本的口径）"
	}
	t.Logf("回放口径 %s；档位 %s；频率 %v，抖动 ±%v，种子 %d", label, gateMode, freq, jitter, seed)
	for _, r := range results {
		t.Logf("群 %d（%d 条消息，%d 个有消息的小时）：判断 被叫到 %d、合进一串 %d、过门槛 %d、没接 %d、退避 %d、频率 0 %d、保险丝 %d、只记录 %d；她进回合 %d 次；合起来说了 %d 次 / 该说 %.0f 次（%.2f 倍），1.5 倍以上的小时 %d%%，过门槛的平均可能性 %.2f，验收 %v",
			r.Group, r.Messages, r.Hours, r.Forced, r.Merged, r.Enter, r.Skip, r.Backoff, r.Muted, r.Fuse, r.Off, r.Entered, r.SumA, r.SumT, r.Ratio, r.Over, r.AvgEnter, r.Pass)
	}
	if out := os.Getenv("SUMIKA_GATE_REPLAY_OUT"); out != "" {
		raw, _ := json.Marshal(results)
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func replayOne(t *testing.T, old *sql.DB, conv string, total, gi int, characterID, alias string, freq, jitter float64, seed uint64, compat bool, gateMode string) ReplayGroup {
	t.Helper()
	const bot, ownerQQ = "90001", "90000"
	// 这个群的全部消息和通知，按时间
	var rows []replayRow
	rs, err := old.Query(`SELECT id, seq, actor_kind, actor_id, COALESCE(origin, ''), kind, text, segments_json, reply_to, created_at FROM events
		WHERE conversation = ? AND kind IN ('message', 'notice') ORDER BY created_at, seq`, conv)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var r replayRow
		if err := rs.Scan(&r.ID, &r.Seq, &r.ActorKind, &r.ActorID, &r.Origin, &r.Kind, &r.Text, &r.Segs, &r.ReplyTo, &r.CreatedAt); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	rs.Close()

	// 她在群里的号：@ 了她的消息（mentions 里有她的角色 id）里，@ 的号出现最多的那个
	herQQ := map[string]int{}
	parse := func(r replayRow) bunSegs {
		var b bunSegs
		if r.Segs.Valid {
			_ = json.Unmarshal([]byte(r.Segs.String), &b)
		}
		return b
	}
	for _, r := range rows {
		b := parse(r)
		hit := false
		for _, m := range b.Mentions {
			hit = hit || m == characterID
		}
		if hit {
			for _, s := range b.Segs {
				if s.Type == "at" && s.QQ != "all" && s.QQ != "" {
					herQQ[s.QQ]++
				}
			}
		}
	}
	her, best := "", 0
	for k, n := range herQQ {
		if n > best {
			her, best = k, n
		}
	}

	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	roster := Roster{Owners: map[string]string{"qq": ownerQQ}, Accounts: []Account{{"qq", bot, "ca"}}}
	cv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: strings.TrimPrefix(conv, "qq:group:")}
	clk := &replayClock{}
	var mu sync.Mutex
	var released []types.Batch
	rnd := rand.New(rand.NewPCG(seed, seed+uint64(gi)))
	jit := jitter
	if jit == 0 {
		jit = -1
	}
	cfg := GateConfig{Stores: store.Single(st), Roster: roster, Mode: func(types.Conversation) string { return gateMode }, Frequency: freq, Jitter: jit, Rand: rnd.Float64, Clock: clk, Loc: time.UTC, NoFuse: compat,
		Release: func(b types.Batch) { mu.Lock(); released = append(released, b); mu.Unlock() }}
	if alias != "" {
		cfg.Aliases = func(string) []string { return []string{alias} }
	}
	gate, err := NewGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	chain, err := NewChain(ChainConfig{Switch: func(string, string, types.Conversation) (bool, bool) { return false, true }})
	if err != nil {
		t.Fatal(err)
	}
	pl, err := NewPipeline(PipelineConfig{Stores: store.Single(st), Roster: roster, Chain: chain, Gate: gate.Handle})
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Close()

	newID := map[string]string{} // 旧事件 id → 新库里的事件 id（撤回通知指向被撤回的那条）
	takeReleased := func() []types.Batch {
		mu.Lock()
		defer mu.Unlock()
		out := released
		released = nil
		return out
	}
	// 她 5 秒后回了一句
	herReply := func(b types.Batch) {
		at := b.Msgs[len(b.Msgs)-1].Time.UnixMilli() + 5000
		if _, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: ActorCharacter, ActorID: "ca", Origin: "character", Kind: "message", Text: "（回放：她回了一句）", CreatedAt: at}, []string{"ca"}); err != nil {
			t.Fatal(err)
		}
		gate.TurnDone("ca", cv, true, 1, time.UnixMilli(at))
	}
	entered := 0
	var pendingBurstUntil int64
	for _, r := range rows {
		clk.ms.Store(r.CreatedAt)
		if pendingBurstUntil != 0 && r.CreatedAt >= pendingBurstUntil {
			gate.FlushBursts()
			pendingBurstUntil = 0
			for _, b := range takeReleased() {
				entered++
				if !compat { // Bun 的回放脚本里 burst 放行的没有"她回了一句"
					herReply(b)
				}
			}
		}
		feedReplayRow(t, st, pl, roster, cv, conv, r, characterID, her, bot, ownerQQ, compat, newID, parse(r))
		for _, b := range takeReleased() {
			entered++
			herReply(b)
		}
		if r.Kind == "message" {
			ds, err := gate.Decisions(cv, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(ds) == 1 && ds[0].Verdict == VerdictMerged && pendingBurstUntil == 0 {
				pendingBurstUntil = r.CreatedAt + 60_000
			}
		}
	}
	gate.FlushBursts()
	for _, b := range takeReleased() {
		entered++
		if !compat {
			herReply(b)
		}
	}

	// 两种比法里的第一种（规格第九节要的）：每个钟头最后一次判断时，过去 60 分钟她说了几次（actual）和那时的目标（target）
	ds, err := st.GateDecisions(conv, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	type hour struct {
		target *float64
		actual *int
		expect float64
		enters int
		msgs   int
	}
	hours := map[int64]*hour{}
	res := ReplayGroup{Group: gi, Messages: total, Entered: entered}
	var sumP float64
	var nEnter int
	for _, d := range ds {
		h := hours[d.At/3_600_000]
		if h == nil {
			h = &hour{}
			hours[d.At/3_600_000] = h
		}
		if d.Target != nil {
			h.target, h.actual = d.Target, d.Actual
			h.expect = *d.Expected
		}
		h.msgs++
		if d.Verdict == VerdictEnter || d.Verdict == VerdictForced {
			h.enters++
		}
		switch d.Verdict {
		case VerdictForced:
			res.Forced++
		case VerdictMerged:
			res.Merged++
		case VerdictEnter:
			res.Enter++
		case VerdictSkip:
			res.Skip++
		case VerdictBackoff:
			res.Backoff++
		case VerdictMuted:
			res.Muted++
		case VerdictFuse:
			res.Fuse++
		case VerdictOff:
			res.Off++
		}
		if d.Verdict == VerdictEnter && d.Probability != nil {
			sumP += *d.Probability
			nEnter++
		}
	}
	res.Hours = len(hours)
	if nEnter > 0 {
		res.AvgEnter = float64(int(sumP/float64(nEnter)*1000+0.5)) / 1000
	}
	var keys []int64
	for k := range hours {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		h := hours[k]
		if h.target != nil {
			res.PerHour = append(res.PerHour, [5]float64{float64(h.msgs), float64(h.enters), float64(int(h.expect*10+0.5)) / 10, float64(int(*h.target*10+0.5)) / 10, float64(*h.actual)})
		}
	}
	var over int
	for _, h := range hours {
		if h.target == nil || *h.target < 1 {
			continue
		}
		res.Judged++
		res.SumA += *h.actual
		res.SumT += *h.target
		if float64(*h.actual) > 1.5**h.target {
			over++
		}
	}
	if res.Judged > 0 {
		res.Ratio = float64(res.SumA) / res.SumT
		res.Over = int(float64(over)*100/float64(res.Judged) + 0.5)
	}
	res.Ratio = float64(int(res.Ratio*1000+0.5)) / 1000
	res.SumT = float64(int(res.SumT*100+0.5)) / 100
	res.Pass = res.Ratio >= 0.85 && res.Ratio <= 1.15 && res.Over <= 20
	if gateMode == ModeRecord {
		if entered != 0 || res.Off == 0 {
			t.Fatalf("群 %d 是只记录档：不该放行也不该有别的判断：%+v", gi, res)
		}
		return res
	}
	if res.Judged == 0 || entered == 0 {
		t.Fatalf("群 %d 回放没有可比的小时或没放行过：%+v", gi, res)
	}
	return res
}

// feedReplayRow 把旧库的一行喂进 Go 版：消息经入库（直接写库，不经 Intake）和流水线，通知里的撤回写成撤回通知
func feedReplayRow(t *testing.T, st *store.Store, pl *Pipeline, roster Roster, cv types.Conversation, conv string, r replayRow, characterID, her, bot, ownerQQ string, compat bool, newID map[string]string, b bunSegs) {
	t.Helper()
	if r.Kind == "notice" {
		if (b.Notice != "group_recall" && b.Notice != "friend_recall") || !r.ReplyTo.Valid {
			return
		}
		target, ok := newID[r.ReplyTo.String]
		if !ok {
			return
		}
		if _, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: ActorHuman, ActorID: r.ActorID, Origin: OriginPeer, Kind: "notice", Text: "",
			Segments: map[string]any{"notice": "recall"}, ReplyTo: &target, CreatedAt: r.CreatedAt}, []string{"ca"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	mentioned := false
	for _, m := range b.Mentions {
		mentioned = mentioned || m == characterID
	}
	var segs []types.Seg
	for _, s := range b.Segs {
		switch {
		case s.Type == "at" && s.QQ == "all":
			if compat { // Bun 的回放脚本把 @全体 也算成叫到她
				mentioned = true
			} else {
				segs = append(segs, types.Seg{Kind: types.SegAt, Target: types.AtAll})
			}
		case s.Type == "at" && s.QQ == her:
			mentioned = true // 她自己的号：下面统一补一个 @ 她
		case s.Type == "at" && s.QQ != "":
			segs = append(segs, types.Seg{Kind: types.SegAt, Target: s.QQ})
		}
	}
	if mentioned {
		segs = append(segs, types.Seg{Kind: types.SegAt, Target: bot})
	}
	if r.ReplyTo.Valid {
		// 引用：旧库只存了被引用那条的事件 id；新库里每条事件的平台消息号就用旧的事件 id，引用的是她说过的话就算叫到她
		segs = append(segs, types.Seg{Kind: types.SegReply, Target: r.ReplyTo.String})
	}
	segs = append(segs, types.Seg{Kind: types.SegText, Text: r.Text})

	if r.ActorKind == "character" {
		// 以前她的号（MaiBot 时期、真人手动）说的：不算她说的（Bun 回放脚本里也是 external）
		res, err := st.AppendInbound(store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: ActorCharacter, ActorID: "ca", Origin: OriginExternal, Kind: "message", Text: r.Text,
			Segments: map[string]any{"segs": segs}, CreatedAt: r.CreatedAt}, PlatformID: r.ID, Observers: []string{"ca"}})
		if err != nil {
			t.Fatal(err)
		}
		newID[r.ID] = res.EventID
		return
	}
	from := types.User{ID: strings.TrimPrefix(r.ActorID, "qq:")}
	if r.ActorKind == "owner" {
		from.ID = ownerQQ
	}
	var replyPlatform string
	if r.ReplyTo.Valid {
		replyPlatform = r.ReplyTo.String
	}
	res, err := st.AppendInbound(store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: r.ActorKind, ActorID: r.ActorID, Origin: r.Origin, Kind: "message", Text: r.Text,
		Segments: map[string]any{"segs": segs, "from": from}, CreatedAt: r.CreatedAt}, PlatformID: r.ID, ReplyPlatformID: replyPlatform, Observers: []string{"ca"}})
	if err != nil {
		t.Fatal(err)
	}
	newID[r.ID] = res.EventID
	pl.OnStored(Stored{
		Inbound:   types.Inbound{Kind: types.InboundMessage, Account: bot, Conv: cv, PlatformID: r.ID, From: from, Time: time.UnixMilli(r.CreatedAt), Segs: segs},
		Character: "ca", Actor: Actor{Kind: r.ActorKind, ID: r.ActorID}, Origin: r.Origin, Seq: res.Seq, EventID: res.EventID, Observed: true,
	})
}
