package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sumika-native/internal/config"
	"sumika-native/internal/memory"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 数据库实况单独导出，不从模型回复重建事实或修订链。
func issue17Tables(file string) (map[string][]map[string]any, error) {
	db, err := store.OpenReadOnlyFile(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string][]map[string]any{}
	for _, table := range []string{"facts", "episodes", "fact_revisions", "fact_evidence", "item_scopes", "memory_jobs", "summary_state"} {
		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			return nil, err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range ptr {
				ptr[i] = &values[i]
			}
			if err = rows.Scan(ptr...); err != nil {
				rows.Close()
				return nil, err
			}
			row := map[string]any{}
			for i, c := range cols {
				if b, ok := values[i].([]byte); ok {
					values[i] = string(b)
				}
				row[c] = values[i]
			}
			out[table] = append(out[table], row)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// 共享事实只从正式共用库导出，角色任务和游标仍只读角色库；不从回答推算入库结果。
func issue17MemoryTables(w *issue17World, char string) (map[string][]map[string]any, error) {
	local, err := issue17Tables(issue17DB(w.p.Root, char))
	if err != nil {
		return nil, err
	}
	shared, err := issue17Tables(w.p.OwnerMemoryDB())
	if err != nil {
		return nil, err
	}
	for _, table := range []string{"facts", "fact_revisions", "fact_evidence", "item_scopes"} {
		for _, row := range shared[table] {
			row["storage"] = "owner-pool"
			local[table] = append(local[table], row)
		}
	}
	return local, nil
}
func issue17Concurrent(ctx context.Context, w *issue17World) error {
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, char := range []string{issue17Character, issue17Other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := issue17Facts()[0].Story
			if char == issue17Other {
				text = "曜的专属约定是只叫主人纸鸢"
			}
			errs <- w.deliver(ctx, char, "console:"+char+":topic:parallel", []issue17Event{{Actor: "owner", Text: text}})
		}()
	}
	wg.Wait()
	close(errs)
	var err error
	for e := range errs {
		err = errors.Join(err, e)
	}
	return err
}
func issue17StoredChecks(w *issue17World, r *issue17Round) ([]issue17Check, error) {
	tables, err := issue17MemoryTables(w, issue17Character)
	if err != nil {
		return nil, err
	}
	chatter, impersonation, selfEvidence, duplicates := 0, 0, 0, 0
	seen := map[string]bool{}
	for _, f := range tables["facts"] {
		text, _ := f["text"].(string)
		status, _ := f["status"].(string)
		if status == "active" {
			if strings.Contains(text, "闲聊标记") {
				chatter++
			}
			if strings.Contains(text, "雾港") {
				impersonation++
			}
			key := fmt.Sprint(f["subject"]) + "|" + text
			if seen[key] {
				duplicates++
			}
			seen[key] = true
		}
	}
	for _, ev := range tables["fact_evidence"] {
		if ev["speaker"] == issue17Character && ev["by_subject"] == int64(1) {
			selfEvidence++
		}
	}
	checks := []issue17Check{{"chatter_not_promoted", chatter == 0, fmt.Sprintf("10 条闲聊；active 含闲聊标记 %d（语义漏判另由人工抽查）", chatter)}, {"no_independent_active_duplicate", duplicates == 0, fmt.Sprintf("同主体同文重复 active=%d；近义重复待人工", duplicates)}, {"impersonation_not_owner_active", impersonation == 0, fmt.Sprintf("攻击目标雾港 active=%d", impersonation)}, {"self_reply_not_owner_evidence", selfEvidence == 0, fmt.Sprintf("角色说话被记成本人证据=%d", selfEvidence)}}
	// 来源会话先查非空；seq 到角色观察事件的核对在 issue17RawChecks。
	sourceOK := true
	for _, it := range r.Items[issue17Character] {
		if it.Conversation == "" {
			sourceOK = false
		}
	}
	checks = append(checks, issue17Check{"source_conversation_present", sourceOK, "原始 source_seqs / 修订 / 证据在 Tables 中，语义归属待标"})
	roleLeaks := 0
	for _, it := range r.Items[issue17Character] {
		if strings.Contains(it.Text, "纸鸢") {
			roleLeaks++
		}
	}
	for _, it := range r.Items[issue17Other] {
		if strings.Contains(it.Text, "编造昵称甲") {
			roleLeaks++
		}
	}
	checks = append(checks, issue17Check{"role_exclusive_pool", roleLeaks == 0, fmt.Sprintf("两边角色专属词进入对方记忆库=%d；主人共用例外另判", roleLeaks)})
	return checks, nil
}

func issue17Mechanisms(ctx context.Context, dir, root, model string) ([]issue17Check, error) {
	g := newIssue17Gateway(modeFake, 0, "")
	defer g.server.Close()
	var checks []issue17Check
	for _, name := range []string{"write_off", "character_not_listed", "summary_off", "no_background", "place_off"} {
		w, err := newIssue17World(ctx, filepath.Join(dir, name), root, model, g)
		if err != nil {
			return nil, err
		}
		cfg := issue17Enabled()
		hasModel := true
		switch name {
		case "write_off":
			cfg = config.Defaults().Memory.Write
			cfg.Characters, cfg.Summarize = nil, nil // 全只读；默认的 [*] 是开着 remember 的
		case "character_not_listed":
			cfg.Characters = []string{issue17Other}
			cfg.Summarize = []string{issue17Other}
		case "summary_off":
			cfg.Summarize = []string{}
		case "no_background":
			hasModel = false
		case "place_off":
			cfg.Places = []string{}
		}
		err = w.open(ctx, cfg, hasModel)
		if err != nil {
			return nil, err
		}
		before, _ := g.snapshot()
		for _, conv := range issue17Convs {
			seq, e := w.append(issue17Character, conv, "owner", issue17Facts()[0].Story, true)
			if e != nil {
				_ = w.close()
				return nil, e
			}
			m := w.mems[issue17Character]
			if m.Writable() {
				if _, e = m.InitCursor(ctx, conv, 0); e != nil {
					_ = w.close()
					return nil, e
				}
			}
			w.hub.NotifySeen(turn.Seen{CharacterID: issue17Character, Conversation: conv, Seq: seq})
			result, e := m.SummarizeBatch(ctx, conv, true)
			denied := errors.Is(e, memory.ErrNotWritable) || errors.Is(e, memory.ErrSummarizeOff) || result.Skipped == memory.SkipPlace || result.Skipped == memory.SkipNoModel
			checks = append(checks, issue17Check{name + ":" + conv, denied, fmt.Sprintf("skip=%s error=%v", result.Skipped, e)})
		}
		if err = w.close(); err != nil {
			return nil, err
		}
		tables, err := issue17MemoryTables(w, issue17Character)
		if err != nil {
			return nil, err
		}
		after, _ := g.snapshot()
		checks = append(checks, issue17Check{name + ":no_write", len(tables["facts"]) == 0 && len(after) == len(before), fmt.Sprintf("事实=%d 模型调用增量=%d", len(tables["facts"]), len(after)-len(before))})
	}
	recovered, err := issue17Recovery(ctx, filepath.Join(dir, "recovery"), root, model, g)
	if err != nil {
		return nil, err
	}
	checks = append(checks, recovered...)
	return checks, nil
}
func issue17Recovery(ctx context.Context, dir, root, model string, g *issue17Gateway) (checks []issue17Check, retErr error) {
	w, err := newIssue17World(ctx, dir, root, model, g)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, w.close()) }()
	cfg := issue17Enabled()
	if err = w.open(ctx, cfg, true); err != nil {
		return nil, err
	}
	conv := issue17Convs[0]
	file := issue17DB(w.p.Root, issue17Character)
	if _, err = w.mems[issue17Character].InitCursor(ctx, conv, 0); err != nil {
		return nil, err
	}
	// memory_jobs 与事实、证据、修订同一事务；在最后记账处中止，证明前面的事实也回滚。
	if err = issue17Exec(file, `CREATE TRIGGER issue17_before_commit BEFORE INSERT ON memory_jobs BEGIN SELECT RAISE(ABORT, 'issue17 before commit'); END`); err != nil {
		return nil, err
	}
	w.failFast = true
	err = w.deliver(ctx, issue17Character, conv, []issue17Event{{Actor: "owner", Text: issue17Facts()[0].Story}})
	if err == nil {
		return nil, errors.New("提交前故障没有进入失败路径")
	}
	w.failFast = false
	if err = w.close(); err != nil {
		return nil, err
	}
	tables, err := issue17MemoryTables(w, issue17Character)
	if err != nil {
		return nil, err
	}
	clean := len(tables["facts"]) == 0 && len(tables["fact_revisions"]) == 0 && len(tables["memory_jobs"]) == 0
	checks = append(checks, issue17Check{"before_commit_atomic", clean, fmt.Sprintf("facts=%d revisions=%d jobs=%d", len(tables["facts"]), len(tables["fact_revisions"]), len(tables["memory_jobs"]))})
	if err = issue17Exec(file, `DROP TRIGGER issue17_before_commit; UPDATE summary_state SET failures=0,retry_at=NULL`); err != nil {
		return nil, err
	}
	if err = w.open(ctx, cfg, true); err != nil {
		return nil, err
	}
	if err = w.wait(ctx, issue17Character, conv, w.events[0].Seq); err != nil {
		return nil, err
	}
	committed := w.mems[issue17Character].Items()
	if len(committed) != 1 {
		return nil, fmt.Errorf("恢复应只有猫的事实，实际 %d 条", len(committed))
	}
	tables, err = issue17MemoryTables(w, issue17Character)
	if err != nil {
		return nil, err
	}
	checks = append(checks, issue17Check{"before_commit_restart_replay", len(tables["facts"]) == 1 && len(tables["memory_jobs"]) == 1, "重启扫描重放未提交事件：期望一条事实、一个 job"})
	// 在索引事务最后的世代号写入处中止；主库已经提交，全文事务回滚，不能被表重建时序绕过。
	if err = issue17Exec(memory.SidecarPath(w.p.OwnerMemoryDB()), `CREATE TRIGGER issue17_after_commit BEFORE INSERT ON sync_state WHEN NEW.key='text' BEGIN SELECT RAISE(ABORT, 'issue17 after commit'); END`); err != nil {
		return nil, err
	}
	if err = w.deliver(ctx, issue17Character, conv, []issue17Event{{Actor: "owner", Text: issue17Facts()[1].Story}}); err != nil {
		return nil, err
	}
	last := w.events[len(w.events)-1].Seq
	tables, err = issue17MemoryTables(w, issue17Character)
	if err != nil {
		return nil, err
	}
	checks = append(checks, issue17Check{"after_commit_index_failure", len(tables["facts"]) == 2 && len(tables["memory_jobs"]) == 2 && len(w.mems[issue17Character].Items()) == 1, "主库两条已提交，内存仍一条：命中了提交后 reload 失败"})
	want := map[string]string{}
	for _, row := range tables["facts"] {
		want["f:"+fmt.Sprint(row["id"])] = fmt.Sprint(row["text"])
	}
	if err = w.close(); err != nil {
		return nil, err
	}
	// 只删本夹具创建的派生文件，主库不动。模拟提交后索引丢失。
	if err = os.Remove(memory.SidecarPath(w.p.OwnerMemoryDB())); err != nil {
		return nil, err
	}
	if err = w.open(ctx, cfg, true); err != nil {
		return nil, err
	}
	m := w.mems[issue17Character]
	// 每条提交过的正文都要在全文和向量两路命中；换问法由独立考题测。
	rebuilt := len(m.Items()) == len(want)
	for _, it := range m.Items() {
		if want[it.Ref] != it.Text {
			rebuilt = false
		}
		rec := m.Recall(ctx, issue17Audience(conv, "owner"), memory.Query{Text: it.Text, Addressed: true})
		lexical, vector := false, false
		for _, h := range rec.Hits {
			if h.Ref == it.Ref {
				lexical = h.Lexical > 0
				vector = h.Vector > 0
			}
		}
		rebuilt = rebuilt && lexical && vector
	}
	checks = append(checks, issue17Check{"after_commit_rebuild", rebuilt, "两条主库 ref / text 相同，全文和向量均命中，无悬挂引用"})
	before, _ := g.snapshot()
	w.hub.NotifySeen(turn.Seen{CharacterID: issue17Character, Conversation: conv, Seq: last})
	result, err := m.SummarizeBatch(ctx, conv, true)
	if err != nil {
		return nil, err
	}
	if err = w.close(); err != nil {
		return nil, err
	}
	after, _ := g.snapshot()
	tables, err = issue17MemoryTables(w, issue17Character)
	if err != nil {
		return nil, err
	}
	checks = append(checks, issue17Check{"after_commit_duplicate_consumption", len(before) == len(after) && len(tables["facts"]) == 2 && len(tables["memory_jobs"]) == 2, fmt.Sprintf("skip=%s facts=%d jobs=%d 调用增量=%d", result.Skipped, len(tables["facts"]), len(tables["memory_jobs"]), len(after)-len(before))})
	return checks, nil
}
