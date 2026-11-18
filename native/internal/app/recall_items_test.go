package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"sumika-native/internal/turn"
)

// trace 里存的是 turn.RecallItem 的 JSON，重启后按 api.RecallItem 读：两边字段对不上就会静默丢分数。
func TestRecallItemStoredShapeMatchesAPI(t *testing.T) {
	t.Parallel()
	in := []turn.RecallItem{{Text: "编造", Kind: "fact", Source: "原话", CreatedAt: 7, Pinned: true, Hearsay: true, Score: 1.5, Rerank: 2.5, Sim: 3.5, Lexical: 4, Vector: 5, Previous: []turn.RecallPrior{{Text: "旧编造", CreatedAt: 3}}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var stored []map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	live := apiRecall(in)
	back, _ := json.Marshal(live)
	var liveMap []map[string]any
	_ = json.Unmarshal(back, &liveMap)
	if !reflect.DeepEqual(stored, liveMap) { // 每个字段都填了非零值：任何一个字段名对不上都会在这里露出来
		t.Fatalf("存库的形状和界面类型不一致：\n存 %v\n读 %v", stored, liveMap)
	}
}
