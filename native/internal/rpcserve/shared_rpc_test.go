package rpcserve_test

// 共用场景 testdata/shared/rpc/（规格-Bun后台第1批 第七节）：Go 服务端照场景建库、发请求，结果逐字比 expect；
// SUMIKA_SHARED_UPDATE=1 时把实际结果写回 expect（输入不动）。Bun 的跑法在 server/test/shared-rpc.test.ts，比 README 里写的字段。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/rpcwire"
	"sumika-native/internal/store"
)

type rpcScenario struct {
	Name        string              `json:"name"`
	Doc         string              `json:"doc"`
	SourceTests []string            `json:"source_tests"`
	Characters  []api.CharacterView `json:"-"`
	RawChars    []map[string]string `json:"characters"`
	Rows        map[string][]rpcRow `json:"rows"`
	Calls       []rpcCall           `json:"calls"`
}

type rpcRow struct {
	Table  string         `json:"table"`
	Values map[string]any `json:"values"`
}

type rpcCall struct {
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
	CursorFrom *int            `json:"cursor_from,omitempty"`
	Expect     *rpcExpect      `json:"expect,omitempty"`
}

type rpcExpect struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
}

func sharedRPCDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "shared", "rpc"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func loadRPCScenario(t *testing.T, path string) rpcScenario {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields() // 拼错的字段会悄悄没用：直接报错
	var s rpcScenario
	if err := d.Decode(&s); err != nil {
		t.Fatalf("%s：%v", filepath.Base(path), err)
	}
	for _, c := range s.RawChars {
		for k := range c {
			if k != "id" && k != "name" {
				t.Fatalf("%s：characters 里不认识的字段 %s", s.Name, k)
			}
		}
		s.Characters = append(s.Characters, api.CharacterView{ID: c["id"], Name: c["name"]})
	}
	return s
}

func insertRows(t *testing.T, set *store.Set, rows map[string][]rpcRow) {
	t.Helper()
	platforms := make([]string, 0, len(rows))
	for p := range rows {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	for _, p := range platforms {
		st, ok := set.Platform(p)
		if !ok {
			t.Fatalf("没有平台 %s 的库", p)
		}
		for _, r := range rows[p] {
			cols := make([]string, 0, len(r.Values))
			for c := range r.Values {
				cols = append(cols, c)
			}
			sort.Strings(cols)
			args := make([]any, len(cols))
			for i, c := range cols {
				v := r.Values[c]
				if f, ok := v.(float64); ok {
					v = int64(f)
				}
				args[i] = v
			}
			q := `INSERT INTO ` + r.Table + ` (` + strings.Join(cols, ",") + `) VALUES (` + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `)`
			if _, err := st.Events.Exec(q, args...); err != nil {
				t.Fatalf("插 %s.%s：%v", p, r.Table, err)
			}
		}
	}
}

func TestSharedRPC(t *testing.T) {
	update := os.Getenv("SUMIKA_SHARED_UPDATE") == "1"
	files, _ := filepath.Glob(filepath.Join(sharedRPCDir(t), "*.json"))
	if len(files) < 8 { // 下限：删掉场景文件不能悄悄变绿；真要删就同时改这里和 server/test/shared-rpc.test.ts
		t.Fatalf("testdata/shared/rpc 里只有 %d 个场景，少于 8 个", len(files))
	}
	for _, path := range files {
		s := loadRPCScenario(t, path)
		t.Run(s.Name, func(t *testing.T) {
			if s.Name+".json" != filepath.Base(path) {
				t.Fatalf("场景名 %q 和文件名对不上", s.Name)
			}
			p, err := paths.Init(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			set, err := store.OpenGo(p, "local", "qq")
			if err != nil {
				t.Fatal(err)
			}
			defer set.Close()
			insertRows(t, set, s.Rows)
			svc := desktopread.New(set, s.Characters, nil)
			defer svc.Close()
			st, ct := rpcserve.Pipe()
			go rpcserve.New(&desktopread.Reader{Service: svc}, "shared").Serve(context.Background(), st)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c, err := rpcclient.New(ctx, ct, "shared")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			var results []json.RawMessage
			changed := false
			for i := range s.Calls {
				call := &s.Calls[i]
				params := call.Params
				if call.CursorFrom != nil {
					params = withCursor(t, params, results, *call.CursorFrom)
				}
				var raw json.RawMessage
				got := &rpcExpect{}
				if err := c.Call(ctx, call.Method, params, &raw); err != nil {
					var re *rpcwire.Error
					if !errors.As(err, &re) {
						t.Fatalf("第 %d 个调用不是协议错误：%v", i, err)
					}
					got.Error = &struct {
						Code int `json:"code"`
					}{re.Code}
				} else {
					got.Result = canonical(t, raw)
				}
				results = append(results, raw)
				if update {
					if call.Expect == nil || !sameExpect(call.Expect, got) {
						call.Expect, changed = got, true
					}
					continue
				}
				if call.Expect == nil {
					t.Errorf("第 %d 个调用（%s）没有 expect：先 SUMIKA_SHARED_UPDATE=1 生成、读过再提交", i, call.Method)
					continue
				}
				if !sameExpect(call.Expect, got) {
					w, _ := json.Marshal(call.Expect)
					g, _ := json.Marshal(got)
					t.Errorf("第 %d 个调用（%s）不一样\n要 %s\n得 %s", i, call.Method, w, g)
				}
			}
			if update && changed {
				writeBack(t, path, s)
			}
		})
	}
}

// withCursor 把第 from 个调用结果里的 next_cursor 填进这次的 params.cursor（游标是不透明的，各实现自己的）
func withCursor(t *testing.T, params json.RawMessage, results []json.RawMessage, from int) json.RawMessage {
	t.Helper()
	if from < 0 || from >= len(results) {
		t.Fatalf("cursor_from %d 越界", from)
	}
	var prev struct {
		Value struct {
			NextCursor string `json:"next_cursor"`
		} `json:"value"`
	}
	if err := json.Unmarshal(results[from], &prev); err != nil || prev.Value.NextCursor == "" {
		t.Fatalf("第 %d 个调用的结果里没有 next_cursor", from)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(prev.Value.NextCursor)
	m["cursor"] = b
	out, _ := json.Marshal(m)
	return out
}

// canonical 去掉游标（不透明、各实现不同）再统一缩进，方便人读和比较
func canonical(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	dropCursors(v)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func dropCursors(v any) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["next_cursor"]; ok {
			x["next_cursor"] = "（游标）"
		}
		for _, e := range x {
			dropCursors(e)
		}
	case []any:
		for _, e := range x {
			dropCursors(e)
		}
	}
}

func sameExpect(a, b *rpcExpect) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	var va, vb any
	json.Unmarshal(ja, &va)
	json.Unmarshal(jb, &vb)
	ca, _ := json.Marshal(va)
	cb, _ := json.Marshal(vb)
	return bytes.Equal(ca, cb)
}

// writeBack 只改各调用的 expect，别的字段保持原样和原顺序
func writeBack(t *testing.T, path string, s rpcScenario) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name        json.RawMessage   `json:"name"`
		Doc         json.RawMessage   `json:"doc"`
		SourceTests json.RawMessage   `json:"source_tests"`
		Characters  json.RawMessage   `json:"characters"`
		Rows        json.RawMessage   `json:"rows"`
		Calls       []json.RawMessage `json:"calls"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	calls := make([]json.RawMessage, len(s.Calls))
	for i, c := range s.Calls {
		calls[i], _ = json.Marshal(c)
	}
	doc.Calls = calls
	// 编码器连 RawMessage 一起重新缩进；HTML 转义关掉，中文原样
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(orderedDoc(doc.Name, doc.Doc, doc.SourceTests, doc.Characters, doc.Rows, calls)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// orderedDoc 保持字段顺序：name doc source_tests characters rows calls
func orderedDoc(name, doc, src, chars, rows json.RawMessage, calls []json.RawMessage) any {
	type d struct {
		Name        json.RawMessage   `json:"name"`
		Doc         json.RawMessage   `json:"doc"`
		SourceTests json.RawMessage   `json:"source_tests"`
		Characters  json.RawMessage   `json:"characters"`
		Rows        json.RawMessage   `json:"rows"`
		Calls       []json.RawMessage `json:"calls"`
	}
	return d{name, doc, src, chars, rows, calls}
}
