package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// 止血的隔离（规格-记忆写入上线 W5 验收 5）。条目都是编的，库在临时目录；时间用小整数毫秒，隔离线 5000

const qCut = 5000

type qItem struct {
	id, text, status, confirmed string
	created                     int64
	promoted                    int64 // confirmed_by 是 ai 的：被提升的时间（fact_revisions 里 confirm 的 at）；0 = 不记这条修订
}

// qFacts：隔离线前后、各种放行者的事实。范围都是 owner（主人在场的私聊看得到、群里看不到）
var qFacts = []qItem{
	{"OLD", "旧事实：主人住在北京的老房子", "active", "", 1000, 0},                                // Bun 导入的，线之前
	{"OLDAI", "线之前 AI 提升的：主人养了一只橘猫", "active", "ai", 1500, 1600},                    // 线之前创建、线之前提升：留着
	{"AFTAI", "线之后 AI 提升的：主人爱喝红豆汤圆", "active", "ai", 5000, 5000},                    // 线之后创建：隔离
	{"AFTCAND", "线之后待确认：主人在学钢琴弹奏", "candidate", "", 5200, 0},                        // 线之后的待确认：隔离
	{"AFTBLANK", "线之后没记谁放行：主人去过冰岛旅行", "active", "", 5300, 0},                        // 线之后、confirmed_by 空：隔离（只有 owner 例外）
	{"AFTOWNER", "线之后主人亲口让记的：猫叫小满", "active", "owner", 5400, 0},                     // 线之后，主人放行的：保留
	{"AFTSAID", "线之后 remember 记的（主人原话绑定）：主人爱吃酸辣粉", "active", "owner_said", 5450, 0}, // 线之后，owner_said：保留
	{"LATEAI", "线之前创建、线之后才提升的：主人在学小提琴", "active", "ai", 1800, 5600},                 // 导入的旧候选被整理提升：创建在线之前，也要藏
	{"NOREVAI", "ai 放行的却查不到提升时间：主人会修自行车", "active", "ai", 1900, 0},                  // 查不到就当它晚：隔离（更紧）
}

func qSeed(db *sql.DB) {
	for _, f := range qFacts {
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at, scope) VALUES (?, 'fact', ?, 'owner', 'private', ?, ?, 'owner')`, f.id, f.text, f.status, f.created); err != nil {
			panic(err)
		}
	}
	for _, e := range []struct {
		id, text string
		created  int64
	}{{"EOLD", "旧片段：搬家那天的事", 1200}, {"ENEW", "新片段：整理出来的搬家总结", 5100}} {
		if _, err := db.Exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope) VALUES (?, 'console:suzune', ?, 'private', 1, 2, ?, 'owner')`, e.id, e.text, e.created); err != nil {
			panic(err)
		}
	}
}

// qOpen：迁移过的库（有 confirmed_by），按给定隔离线只读打开
func qOpen(t *testing.T, file string, since int64) *Memory {
	t.Helper()
	m, err := Open(context.Background(), file, nil, nil, Options{Character: "suzune", QuarantineSince: since})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

var (
	ownerRoom = Audience{Channel: "console", Conversation: "console:suzune", Scene: "private", PrivateOK: true}
	otherRoom = Audience{Channel: "qq", Conversation: "qq:group:5001001", Scene: "group"}
)

func qRefs(m *Memory, a Audience, query string) []string {
	var refs []string
	for _, h := range m.Recall(context.Background(), a, Query{Text: query, Mode: ModeExplicit, TopK: 40}).Hits {
		refs = append(refs, h.Ref)
	}
	slices.Sort(refs)
	return refs
}

// qMigrated：qFacts、qSeed 放进迁移过的库，再补上迁移才有的放行者（confirmed_by）和提升修订；extra 在迁移之前再往库里放别的
func qMigrated(t *testing.T, extra func(db *sql.DB)) string {
	t.Helper()
	file := migratedDB(t, func(db *sql.DB) {
		qSeed(db)
		if extra != nil {
			extra(db)
		}
	})
	// confirmed_by 是迁移加的列，种子里写不了，迁移之后再放
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, f := range qFacts {
		if f.confirmed != "" {
			if _, err := db.Exec(`UPDATE facts SET confirmed_by = ? WHERE id = ?`, f.confirmed, f.id); err != nil {
				t.Fatal(err)
			}
		}
		if f.promoted != 0 {
			if _, err := db.Exec(`INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES (?, 'confirm', ?, 'summary', '攒够 3 次不同场合')`, f.id, f.promoted); err != nil {
				t.Fatal(err)
			}
		}
	}
	return file
}

func TestQuarantine(t *testing.T) {
	t.Parallel()
	file := qMigrated(t, nil)
	all := []string{"e:ENEW", "e:EOLD", "f:AFTAI", "f:AFTBLANK", "f:AFTCAND", "f:AFTOWNER", "f:AFTSAID", "f:LATEAI", "f:NOREVAI", "f:OLD", "f:OLDAI"}
	kept := []string{"e:EOLD", "f:AFTOWNER", "f:AFTSAID", "f:OLD", "f:OLDAI"}
	// 一个查询把所有条目都撞上：每条的文字里都有"主人"
	const q = "主人 片段 事实 搬家"

	t.Run("不设隔离：全在", func(t *testing.T) {
		m := qOpen(t, file, 0)
		if got := qRefs(m, ownerRoom, q); !slices.Equal(got, all) {
			t.Fatalf("召回 %v，应该是 %v", got, all)
		}
	})

	t.Run("设了隔离：线之后的整理产物和线之后才提升的不见，主人亲口放行的和线之前的留着", func(t *testing.T) {
		before := fileHash(t, file)
		m := qOpen(t, file, qCut)
		if got := qRefs(m, ownerRoom, q); !slices.Equal(got, kept) {
			t.Fatalf("召回 %v，应该是 %v", got, kept)
		}
		// memory_search 走召回
		res, err := m.Search(context.Background(), ownerRoom, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"红豆汤圆", "钢琴弹奏", "冰岛旅行", "整理出来的搬家总结", "小提琴", "自行车"} {
			if strings.Contains(res.Text, bad) {
				t.Errorf("memory_search 还找得到被隔离的：%s", bad)
			}
		}
		for _, good := range []string{"小满", "北京", "橘猫"} {
			if !strings.Contains(res.Text, good) {
				t.Errorf("memory_search 该找得到 %s：%s", good, res.Text)
			}
		}
		// 给整理模型看的已知事实和候选块
		known, _ := m.knownFacts(Audience{Channel: "console", Conversation: "console:suzune", Scene: "private", PrivateOK: true}, counter{}, nil)
		cands, shown := m.candidateFacts(ownerRoom, counter{}, nil)
		for _, bad := range []string{"红豆汤圆", "冰岛旅行", "小提琴", "自行车"} {
			if strings.Contains(known, bad) {
				t.Errorf("已知事实里还有被隔离的：%s", bad)
			}
		}
		if strings.Contains(cands, "钢琴弹奏") || len(shown) != 0 {
			t.Errorf("候选块里还有被隔离的：%q %v", cands, shown)
		}
		if !strings.Contains(known, "小满") || !strings.Contains(known, "北京") {
			t.Errorf("已知事实里该留着没被隔离的：%s", known)
		}
		// 原始快照（考题、管理页看的）一个都不少
		if n := len(m.Items()); n != len(all) {
			t.Errorf("Items() 是原始快照，该有 %d 条，得到 %d", len(all), n)
		}
		// 库一个字节没改
		m.Close()
		if fileHash(t, file) != before {
			t.Error("库变了")
		}
	})

	t.Run("守卫的禁看集合不隔离：被隔离的待确认事实群里照样不许漏", func(t *testing.T) {
		m := qOpen(t, file, qCut)
		blocked, err := m.CheckOutbound(context.Background(), "我听说线之后待确认：主人在学钢琴弹奏，真不错", otherRoom)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(blocked, "f:AFTCAND") {
			t.Fatalf("被隔离的待确认事实在群里漏出去该被守卫拦住：%v", blocked)
		}
	})

	t.Run("隔离线晚于所有条目：什么都不隔离（只有查不到提升时间的 ai 事实照样藏，库不对劲时按晚算）", func(t *testing.T) {
		m := qOpen(t, file, 99999)
		want := slices.DeleteFunc(slices.Clone(all), func(r string) bool { return r == "f:NOREVAI" })
		if got := qRefs(m, ownerRoom, q); !slices.Equal(got, want) {
			t.Fatalf("召回 %v，应该是 %v", got, want)
		}
	})

	t.Run("隔离线正好在某一条的创建时间上：这一条算线之后", func(t *testing.T) {
		m := qOpen(t, file, 5000)
		if slices.Contains(qRefs(m, ownerRoom, "红豆汤圆"), "f:AFTAI") {
			t.Fatal("创建时间等于隔离线的该被隔离")
		}
		m2 := qOpen(t, file, 5001)
		if !slices.Contains(qRefs(m2, ownerRoom, "红豆汤圆"), "f:AFTAI") {
			t.Fatal("隔离线晚一毫秒就不该隔离")
		}
	})

	t.Run("清空键就恢复", func(t *testing.T) {
		m := qOpen(t, file, qCut)
		if slices.Contains(qRefs(m, ownerRoom, "红豆汤圆"), "f:AFTAI") {
			t.Fatal("设了隔离该藏起来")
		}
		m.Close()
		again := qOpen(t, file, 0)
		if !slices.Contains(qRefs(again, ownerRoom, "红豆汤圆"), "f:AFTAI") {
			t.Fatal("清空以后该恢复")
		}
	})

	t.Run("没迁移的 Bun 库没有 confirmed_by 这一列：照样能读，线之后的全藏起来", func(t *testing.T) {
		t.Parallel()
		bun := newBunDB(t, qSeed)
		m := qOpen(t, bun, qCut)
		got := qRefs(m, ownerRoom, q)
		want := []string{"e:EOLD", "f:LATEAI", "f:NOREVAI", "f:OLD", "f:OLDAI"}
		if !slices.Equal(got, want) {
			t.Fatalf("召回 %v，应该是 %v（没有放行者这一列，线之后的一律藏起来；没有这一列就没有 ai，LATEAI、NOREVAI 创建在线之前所以留着）", got, want)
		}
	})
}

// 回执的三个计数各归各的：有效的事实、待确认的事实、片段。三个数故意各不相同、都不是零（4、2、3），
// 两个分支对调或者少数一类都会对不上。手算：线 5000，被藏的有效事实是 AFTAI、AFTBLANK、LATEAI、NOREVAI（AFTOWNER 是主人放行的留着）；
// 待确认是 AFTCAND、AFTCAND2（OLDCAND 在线之前、没被提升，留着）；片段是 ENEW、ENEW2、ENEW3（EOLD 在线之前留着）
func TestQuarantineReportCountsEachKind(t *testing.T) {
	t.Parallel()
	file := qMigrated(t, func(db *sql.DB) {
		for _, c := range []struct {
			id      string
			created int64
		}{{"AFTCAND2", 5500}, {"OLDCAND", 1100}} {
			if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at, scope) VALUES (?, 'fact', ?, 'owner', 'private', 'candidate', ?, 'owner')`, c.id, "待确认 "+c.id, c.created); err != nil {
				t.Fatal(err)
			}
		}
		for i, id := range []string{"ENEW2", "ENEW3"} {
			if _, err := db.Exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope) VALUES (?, 'console:suzune', ?, 'private', 1, 2, ?, 'owner')`, id, "新片段 "+id, 5200+int64(i)); err != nil {
				t.Fatal(err)
			}
		}
	})
	got := qOpen(t, file, qCut).QuarantineReport()
	if want := (QuarantineReport{Active: true, Since: qCut, Facts: 4, Candidates: 2, Episodes: 3}); got != want {
		t.Fatalf("回执 %+v，应该是 %+v", got, want)
	}
	if off := qOpen(t, file, 0).QuarantineReport(); off != (QuarantineReport{}) {
		t.Fatalf("没设线：回执该是空的，得到 %+v", off)
	}
}

// 提升的时间正好等于隔离线：创建在线之前、被整理提升的时间 = 线，算线之后，要藏（quarantined 里是 ConfirmedAt >= since）；
// 线晚一毫秒（提升在线之前 1 毫秒）就不藏。线之前的 ai 事实 OLDAI 提升在 1600，远离边界，测不到这一毫秒
func TestQuarantinePromotionExactlyAtTheLine(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at, scope) VALUES ('EDGE', 'fact', '线边上提升的：主人在学围棋对局', 'owner', 'private', 'active', 1000, 'owner')`); err != nil {
			t.Fatal(err)
		}
	})
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE facts SET confirmed_by = 'ai' WHERE id = 'EDGE'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES ('EDGE', 'confirm', ?, 'summary', '攒够 3 次不同场合')`, qCut); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if slices.Contains(qRefs(qOpen(t, file, qCut), ownerRoom, "主人 围棋"), "f:EDGE") {
		t.Error("提升的时间正好等于线：算线之后，该被藏起来")
	}
	if !slices.Contains(qRefs(qOpen(t, file, qCut+1), ownerRoom, "主人 围棋"), "f:EDGE") {
		t.Error("线晚一毫秒：提升在线之前，不该藏")
	}
	if !slices.Contains(qRefs(qOpen(t, file, 0), ownerRoom, "主人 围棋"), "f:EDGE") {
		t.Error("对照：没设线时它是有效事实，召回得到")
	}
}

// 写入打开着时，刷新后的快照也带隔离：刚写进去的整理产物（线之后）立刻不被想起，主人 remember 的保留
func TestQuarantineAfterWrites(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, qSeed)
	now := int64(6000)
	m, err := Open(context.Background(), file, nil, nil, Options{Writable: true, Character: "suzune", QuarantineSince: qCut, Now: func() time.Time { return time.UnixMilli(now) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	mustApply(t, m, summary(candidate("主人周末爱去海边散步", "owner", "owner")))
	mustApply(t, m, remember(ownerFact("主人最爱的海边是北戴河")))
	var texts []string
	for _, ref := range qRefs(m, ownerRoom, "海边") {
		texts = append(texts, m.byRef[ref].Text)
	}
	if !slices.Equal(texts, []string{"主人最爱的海边是北戴河"}) {
		t.Fatalf("刚写进去的整理产物该被隔离，remember 的该留着：%v", texts)
	}
}

// 线之前创建的旧候选（Bun 导入的待确认）在线之后被整理攒够证据提升成有效：创建时间还在线之前，只看创建时间的隔离管不到它。
// 打开 autoConfirm 的启动补提升（ConfirmSweep）会把导入的待确认一起数，所以这条路是预期会走到的（规格-记忆写入上线 W3b「新的坑」）
func TestQuarantineCatchesLatePromotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	file := migratedDB(t, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at, scope) VALUES ('LATE', 'fact', '主人在学钢琴弹奏', 'owner', 'private', 'candidate', 1000, 'owner')`); err != nil {
			t.Fatal(err)
		}
	})
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	for i, day := range []string{"2026-10-01", "2026-10-02", "2026-10-03"} {
		if _, err := db.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES ('LATE', ?, 'console:suzune', ?, 'owner', 1, 1000)`, day, i+1); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	open := func(since int64) *Memory {
		m, err := Open(ctx, file, nil, nil, Options{Character: "suzune", Writable: true, SummarizeEnabled: true, AutoConfirm: true, QuarantineSince: since,
			Now: func() time.Time { return time.UnixMilli(6000) }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	// 先不设线：启动补提升把它提成有效（confirmed_by = ai，6000 毫秒提升），创建时间还是 1000，召回得到
	m := open(0)
	res, err := m.ConfirmSweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Confirmed != 1 {
		t.Fatalf("启动补提升该提升 1 条：%+v", res)
	}
	var status, by string
	var created int64
	if err := m.w.db.QueryRow(`SELECT status, coalesce(confirmed_by, ''), created_at FROM facts WHERE id = 'LATE'`).Scan(&status, &by, &created); err != nil {
		t.Fatal(err)
	}
	if status != "active" || by != "ai" || created != 1000 {
		t.Fatalf("前提不成立：status=%s confirmed_by=%s created_at=%d", status, by, created)
	}
	if !slices.Contains(qRefs(m, ownerRoom, "主人 钢琴"), "f:LATE") {
		t.Fatal("对照：没设线时它是有效事实，召回得到")
	}
	m.Close()
	// 出事后把线填在提升之前（5000 < 6000）、重启：它创建在线之前，但是线之后才被整理提升的，要被藏起来；库一个字节不改
	before := fileHash(t, file)
	m2 := open(5000)
	if got := qRefs(m2, ownerRoom, "主人 钢琴"); slices.Contains(got, "f:LATE") {
		t.Fatalf("线之后被整理提升的旧候选还在被召回：%v", got)
	}
	if res, err := m2.Search(ctx, ownerRoom, "主人 钢琴"); err != nil || strings.Contains(res.Text, "钢琴弹奏") {
		t.Fatalf("memory_search 还找得到：%v %q", err, res.Text)
	}
	m2.Close()
	if fileHash(t, file) != before {
		t.Error("库变了")
	}
	// 线填在提升之后（7000 > 6000）：提升在线之前，不藏
	m3 := open(7000)
	if got := qRefs(m3, ownerRoom, "主人 钢琴"); !slices.Contains(got, "f:LATE") {
		t.Fatalf("提升在线之前的不该藏：%v", got)
	}
}
