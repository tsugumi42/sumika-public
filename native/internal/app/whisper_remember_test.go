package app

// 悄悄话处理那一轮里的 remember（规格-群聊引用和路由 7.3、8.3）：她能把主人交代的事记下来，
// 记忆条目带来源（悄悄话批号、来源会话、被引事件 id），记忆页和召回时看得出“这是从某次悄悄话来的”。
// 号、角色、话都是编的；数据目录只用 t.TempDir()；不连真协议端，不发任何真实消息。

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/memory"
	"sumika-native/internal/whisper"
)

func TestWhisperRememberCarriesSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	file := goMemoryDB(t, e, testChar)
	var whisperCalls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("记好了。")
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			if strings.Contains(string(raw), "只有你看得到") && whisperCalls.Add(1) == 1 {
				reply = llmtest.Call("w-mem", "remember", `{"kind":"fact","text":"主人喜欢蓝色纸鹤","source_quote":"蓝色纸鹤","temporal":"stable"}`)
			}
		}
		llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Reply: reply}, &e.bodies, &e.mu)(w, r)
	}))
	defer gateway.Close()
	e.llm = gateway
	e.configure("memory:\n  write:\n    characters: [" + testChar + "]\n")
	a := e.open()
	d := a.Desktop()
	defer func() {
		d.Close()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()
	conv := "qq:group:515151"
	id := seedQQ(t, a, conv, "群友编的一句：他们都说蓝色纸鹤好看")
	key := qqGroupKey(conv)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	rec, err := d.Reader.SendWhisper(ctx, key, []api.LocalPart{wquote(key, id, "我喜欢蓝色纸鹤，记一下", testChar)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.whisper.Wait(ctx, rec.Batch)
	if err != nil || len(got) != 1 || got[0].State != whisper.StateDone {
		t.Fatalf("应处理完：%v %v", got, err)
	}
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var text, whisperJSON string
	if err := db.QueryRow(`SELECT text, coalesce(whisper_json,'') FROM facts WHERE text LIKE '%蓝色纸鹤%'`).Scan(&text, &whisperJSON); err != nil {
		t.Fatalf("没写进记忆（remember 在悄悄话那一轮没成）：%v", err)
	}
	var src memory.WhisperSource
	if err := json.Unmarshal([]byte(whisperJSON), &src); err != nil {
		t.Fatalf("来源不是合法 JSON：%q", whisperJSON)
	}
	if src.Batch != rec.Batch || src.Source != conv || len(src.Quotes) != 1 || src.Quotes[0] != id {
		t.Fatalf("来源对不上：%+v，要 批=%s 来源会话=%s 被引=%s", src, rec.Batch, conv, id)
	}
}
