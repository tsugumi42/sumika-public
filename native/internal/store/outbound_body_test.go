package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
)

func TestMediaHistoryCutover(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := p.ChatDB("qq")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(db, "migrations/chat", 9); err != nil {
		t.Fatal(err)
	}
	old := `{"parts":[{"kind":"text","text":"旧的正文","quote":"-71"},{"kind":"forward","nodes":[{"name":"编的作者","text":"节点正文"}]},{"kind":"image","ref":"编的旧图"}]}`
	if _, err := db.Exec(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,created_at) VALUES('old','qq','qq:group:90002','character','ca','message','旧的正文',?,1)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO outbound_plans(id,character_id,conversation,account,origin,kind,state,created_at) VALUES('old-plan','ca','qq:group:90002','90001','character','send','done',1); INSERT INTO outbound_parts(plan_id,idx,kind,text,state) VALUES('old-plan',0,'text','旧的正文','confirmed')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "migrations/chat", 0); err != nil {
		t.Fatal(err)
	}
	var raw, kind, text string
	if err := db.QueryRow(`SELECT segments_json FROM events WHERE id='old'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Parts []struct {
			Kind, Quote string
			Body        []types.BodySegment
			Nodes       []types.ForwardNode
		}
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Parts) != 3 || env.Parts[0].Kind != types.PartMessage || env.Parts[0].Quote != "-71" || types.BodyTextOf(env.Parts[0].Body) != "旧的正文" {
		t.Fatalf("正文或引用迁移损坏：%s", raw)
	}
	if len(env.Parts[1].Nodes) != 1 || types.BodyTextOf(env.Parts[1].Nodes[0].Body) != "节点正文" {
		t.Fatalf("节点迁移损坏：%s", raw)
	}
	if env.Parts[2].Body[0].Media.Available() {
		t.Fatal("旧图句柄不能恢复成可发送资源")
	}
	if err := db.QueryRow(`SELECT kind,text FROM outbound_parts WHERE plan_id='old-plan'`).Scan(&kind, &text); err != nil {
		t.Fatal(err)
	}
	var body []types.BodySegment
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatal(err)
	}
	if kind != types.PartMessage || types.BodyTextOf(body) != "旧的正文" {
		t.Fatal("计划段未切换", kind, text)
	}
	t.Logf("历史升级：事件=1，段=3，计划=1，原始JSON=%d字节，升级JSON=%d字节", len(old), len(raw))
}
