package skills

import (
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

func argsJSON(t *testing.T, raw string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 实测：模型按提示只带 cursor 翻页，或什么参数都不给，旧代码都回"skill 参数分支错误"，技能目录翻不了页。
func TestParseRequestListingBranches(t *testing.T) {
	for _, tc := range []struct {
		args    string
		listing bool
		name    string
	}{
		{`{}`, true, ""},
		{`{"cursor":"c1"}`, true, ""},
		{`{"cursor":"c1","limit":15}`, true, ""},
		{`{"query":""}`, true, ""},
		{`{"query":"git"}`, true, ""},
		{`{"name":"media-grab"}`, false, "media-grab"},
		{`{"name":"media-grab","resource":"references/a.md"}`, false, "media-grab"},
	} {
		q, err := parseRequest(argsJSON(t, tc.args))
		if err != nil || q.Listing != tc.listing || q.Name != tc.name {
			t.Fatalf("%s：%+v %v", tc.args, q, err)
		}
	}
	// name 和 query 同时给仍然是错的，交给目录自己拒绝
	q, err := parseRequest(argsJSON(t, `{"name":"x","query":"y"}`))
	if err != nil || !q.Listing || q.Name == "" {
		t.Fatalf("name+query：%+v %v", q, err)
	}
	for _, bad := range []string{`{"limit":0}`, `{"cursor":1}`, `{"nope":"x"}`} {
		if _, err := parseRequest(argsJSON(t, bad)); err == nil {
			t.Fatalf("%s 应被拒绝", bad)
		}
	}
}

func TestDeclarationExplainsPaging(t *testing.T) {
	for _, want := range []string{"NextCursor", "cursor", "name"} {
		if !strings.Contains(Declaration.Description, want) {
			t.Fatalf("说明里缺 %s", want)
		}
	}
	if strings.Contains(Declaration.Parameters, "oneOf") {
		t.Fatal("空参数、只带 cursor 都是合法的目录查询，不该再声明 oneOf")
	}
}
