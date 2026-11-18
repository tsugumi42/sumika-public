package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sumika-native/internal/llm"
)

// serveEmbeddings 照 OpenAI 的格式回：第 i 句的向量是 [字数, i+1]，故意倒着排 index，看客户端会不会按 index 排回来
func serveEmbeddings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json") // 真的服务（FastAPI）会带；不带的话 SDK 不认
	type item struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	data := []item{}
	for i := len(body.Input) - 1; i >= 0; i-- {
		data = append(data, item{"embedding", i, []float64{float64(len([]rune(body.Input[i]))), float64(i + 1)}})
	}
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": body.Model, "usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1}})
}

func TestClientOrdersByIndexAndUnit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "wrong", http.StatusNotFound)
			return
		}
		serveEmbeddings(w, r)
	}))
	defer srv.Close()
	c := NewClient(llm.Source{Name: "假的", BaseURL: srv.URL + "/v1/", APIKey: "k"}, "m")
	got, err := c.Embed(context.Background(), []string{"一二三", "一"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[[3 1] [1 2]]" {
		t.Fatalf("没按 index 排回原来的顺序：%v", got)
	}
	u := Unit([]float32{3, 4})
	if math.Abs(float64(u[0])-0.6) > 1e-6 || math.Abs(float64(Dot(u, u))-1) > 1e-6 {
		t.Fatalf("归一化不对：%v", u)
	}
	if z := Unit([]float32{0, 0}); z[0] != 0 || z[1] != 0 {
		t.Fatalf("全零向量要原样返回：%v", z)
	}
}

// Embed 的请求体里不带 input_type；EmbedTyped 才带，model 两边都照给的写
func TestInputTypeOnlyWhenAsked(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("请求体读不出来：%v", err)
			return
		}
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"model":"m"}`)
	}))
	defer srv.Close()
	c := NewClient(llm.Source{Name: "假的", BaseURL: srv.URL + "/v1/", APIKey: "编的令牌"}, "编的模型")
	ctx := context.Background()
	if _, err := c.Embed(ctx, []string{"编的一句"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedTyped(ctx, TypeQuery, []string{"编的一句"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedTyped(ctx, TypeDocument, []string{"编的一句"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := bodies[0]["input_type"]; ok {
		t.Fatalf("Embed 不该带 input_type：%v", v)
	}
	if bodies[1]["input_type"] != "query" || bodies[2]["input_type"] != "document" {
		t.Fatalf("EmbedTyped 没把 input_type 带对：%v / %v", bodies[1]["input_type"], bodies[2]["input_type"])
	}
	for i, b := range bodies {
		if b["model"] != "编的模型" {
			t.Fatalf("第 %d 次请求的 model 不对：%v", i+1, b["model"])
		}
	}
	if _, err := c.EmbedTyped(ctx, Type("编的"), []string{"编的一句"}); err == nil {
		t.Fatal("input_type 只有 query / document 两种，别的要报错")
	}
}

func TestLocalNotConfiguredStaysOff(t *testing.T) {
	l := NewLocal(LocalConfig{}, nil)
	l.Start()
	if s, why := l.Status(); s != StatusOff || why == "" {
		t.Fatalf("没配全应该停在 off 并写明原因：%s %q", s, why)
	}
	if _, err := l.Embed(context.Background(), []string{"x"}); err != ErrUnavailable {
		t.Fatalf("没起来时要返回 ErrUnavailable：%v", err)
	}
	if l.Wait(10 * time.Millisecond) {
		t.Fatal("没配全不该等到就绪")
	}
}

// Connect 带的令牌照服务的约定读 SUMIKA_EMBED_TOKEN（规格-本地模型服务第三节：不对就 401）
func TestConnectSendsEnvToken(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"model":"m"}`)
	}))
	defer srv.Close()
	t.Setenv("SUMIKA_EMBED_TOKEN", "编的令牌")
	if _, err := Connect(srv.URL+"/v1", "编的模型", 0).Embed(context.Background(), []string{"编的一句"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUMIKA_EMBED_TOKEN", "")
	if _, err := Connect(srv.URL+"/v1", "编的模型", 0).Embed(context.Background(), []string{"编的一句"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Bearer 编的令牌" || got[1] != "Bearer local" {
		t.Fatalf("Connect 带的令牌不对：%q（要先是环境变量里的，没设时是占位的 local）", got)
	}
}

// Connect 的 timeout 是一次嵌入请求的上限（-embed-timeout）：本地服务一次前向只放一条，一批 64 条要好几分钟。
// 慢服务超过上限就报错；给够了就等得到；零值退回默认的 30 秒——不是"不等"
func TestConnectTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"model":"m"}`)
	}))
	defer srv.Close()
	ctx := context.Background()
	if _, err := Connect(srv.URL+"/v1", "编的模型", 50*time.Millisecond).Embed(ctx, []string{"编的一句"}); err == nil {
		t.Fatal("上限 50 毫秒，300 毫秒才回的服务应该报错")
	}
	if _, err := Connect(srv.URL+"/v1", "编的模型", 5*time.Second).Embed(ctx, []string{"编的一句"}); err != nil {
		t.Fatalf("上限 5 秒应该等得到 300 毫秒：%v", err)
	}
	if _, err := Connect(srv.URL+"/v1", "编的模型", 0).Embed(ctx, []string{"编的一句"}); err != nil {
		t.Fatalf("零值要退回默认的 30 秒（当成不等就会超时）：%v", err)
	}
}
