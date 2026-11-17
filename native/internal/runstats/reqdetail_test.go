package runstats

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 请求详情：段正文从 prompt_sections 按哈希取，memory 段算注入，工具表来自 context_json。
func TestRequestDetailReadsSectionsAndTools(t *testing.T) {
	db, file := tempTrace(t)
	for _, s := range [][2]string{{"h-core", "编造的核心规矩"}, {"h-mem", "编造的记忆参考"}} {
		if _, err := db.Exec(`INSERT INTO prompt_sections (hash, text, first_seen) VALUES (?, ?, 1)`, s[0], s[1]); err != nil {
			t.Fatal(err)
		}
	}
	sections := `[{"id":"core","source":"文件甲","chars":7,"hash":"h-core"},{"id":"memory","source":"记忆","chars":7,"hash":"h-mem"},{"id":"scene-x","source":"丙","chars":3,"hash":"h-gone"}]`
	cx := `{"estimate":900,"breakdown":{"window":8000,"parts":{"tools":{"tokens":30,"items":[{"name":"编造工具","tokens":20},{"name":"另一个","tokens":10}]}}}}`
	if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, ttft_ms, input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, turn_id, sections_json, injected_json, context_json)
		VALUES ('r1','c1','console:c1','reply','编造模型',1000,500,80,10,70,20,5,'t1',?,'["额外条目"]',?)`, sections, cx); err != nil {
		t.Fatal(err)
	}
	d, err := openService(t, file).RequestDetail(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.System) != 2 || d.System[0].Text != "编造的核心规矩" || !d.System[1].Missing {
		t.Fatalf("系统提示词按哈希取正文，取不到标缺失：%+v", d.System)
	}
	if len(d.Injected) != 2 || d.Injected[0].Text != "编造的记忆参考" || d.Injected[1].Text != "额外条目" {
		t.Fatalf("memory 段和 injected_json 算注入：%+v", d.Injected)
	}
	if len(d.Tools) != 2 || d.Tools[0].Name != "编造工具" || d.ToolTokens != 30 || d.Window != 8000 {
		t.Fatalf("工具表和窗口来自 context_json：%+v", d)
	}
	if d.Input != 10 || d.CacheRead != 70 || d.CacheWrite != 20 || d.Output != 5 || d.TTFTMs != 80 {
		t.Fatalf("用量：%+v", d)
	}
	if len(d.ParseErr) != 0 || d.RequestRaw != "" {
		t.Fatalf("没有解析问题，request_json 为空：%+v", d)
	}
}

// 各列解析不了：原文保留、写明原因，不报错也不崩；id 不存在回 ErrNoRequest。
func TestRequestDetailBadJSONKeepsRaw(t *testing.T) {
	db, file := tempTrace(t)
	if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, sections_json, injected_json, context_json, request_json)
		VALUES ('r1','c1','console:c1','reply','编造模型',1000,'不是 JSON','{"不是":"数组"}','{坏的','原样的请求')`); err != nil {
		t.Fatal(err)
	}
	svc := openService(t, file)
	d, err := svc.RequestDetail(context.Background(), "r1")
	if err != nil {
		t.Fatalf("解析失败不该报错：%v", err)
	}
	if d.SectionsRaw != "不是 JSON" || d.InjectedRaw != `{"不是":"数组"}` || d.ContextRaw != "{坏的" || d.RequestRaw != "原样的请求" {
		t.Fatalf("原文要留着：%+v", d)
	}
	if len(d.ParseErr) != 3 || !strings.Contains(d.ParseErr[0], "sections_json") {
		t.Fatalf("每一列的问题都要写明：%v", d.ParseErr)
	}
	if _, err := svc.RequestDetail(context.Background(), "不存在"); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("找不到该回 ErrNoRequest：%v", err)
	}
	if _, err := svc.RequestDetail(context.Background(), ""); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("空 id：%v", err)
	}
}

// 列表查询带上请求 id，点开时才能按它取。
func TestConvTurnsCarriesRequestID(t *testing.T) {
	db, file := tempTrace(t)
	if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, turn_id, sections_json, injected_json)
		VALUES ('r9','c1','console:c1','reply','编造模型',1000,'t1','[]','[]')`); err != nil {
		t.Fatal(err)
	}
	rep, err := openService(t, file).ConvTurns(context.Background(), "console:c1", 0)
	if err != nil || len(rep.Turns) != 1 || rep.Turns[0].Requests[0].ID != "r9" {
		t.Fatalf("%v %+v", err, rep)
	}
}
