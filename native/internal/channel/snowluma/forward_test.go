package snowluma

import (
	"strings"
	"testing"
)

func TestForwardFrameTranslation(t *testing.T) {
	raw := `{"retcode":0,"status":"ok","data":{"messages":[{"user_id":200,"group_id":300,"time":100,"sender":{"nickname":"编的昵称","card":"编\u202e的名片"},"message":[{"type":"text","data":{"text":"前\u0000话\ufffc"}},{"type":"reply","data":{"id":"1","message":[{"type":"text","data":{"text":"编的引用"}}]}},{"type":"forward","data":{"id":"inner"}}]},{"sender":null,"message":42}]}}`
	tree, err := forwardFrame([]byte(raw), "outer")
	if err != nil || len(tree.Nodes) != 2 || !tree.Group {
		t.Fatalf("树：%+v %v", tree, err)
	}
	n := tree.Nodes[0]
	if n.UID != "200" || n.Name != "编的名片" || n.Time != 100 || n.Segs[0].Text != "前话" || n.Segs[1].Raw != "编的引用" || n.Segs[2].Ref != "inner" {
		t.Fatalf("节点：%+v", n)
	}
	if tree.Nodes[1].Name != "" || len(tree.Nodes[1].Segs) != 0 || tree.Nodes[1].Time != 0 {
		t.Fatalf("缺字段节点：%+v", tree.Nodes[1])
	}
	for _, value := range []string{`null`, `true`, `{}`, `[]`, `"0"`, `0`, `-1`} {
		badUID := `{"retcode":0,"data":{"messages":[{"user_id":` + value + `,"message":[]}]}}`
		v, e := forwardFrame([]byte(badUID), "outer")
		if e != nil || v.Nodes[0].UID != "" {
			t.Fatalf("非法号 %s：%+v %v", value, v, e)
		}
	}
	for _, bad := range []string{`null`, `42`, `"x"`, `{"retcode":0,"data":{"messages":{}}}`, `{"retcode":0,"data":{"messages":[]}}`, `{"retcode":100,"status":"failed","message":"payload is empty"}`} {
		if _, err := forwardFrame([]byte(bad), "outer"); err == nil {
			t.Errorf("非法回包成功：%s", bad)
		}
	}
	raw = `{"retcode":0,"data":{"messages":[` + strings.TrimSuffix(strings.Repeat(`{"message":[]},`, 201), ",") + `]}}`
	tree, err = forwardFrame([]byte(raw), "outer")
	if err != nil || len(tree.Nodes) != 200 || tree.Missing != 1 {
		t.Fatalf("翻译前节点上限：%d %d %v", len(tree.Nodes), tree.Missing, err)
	}
}
