package turn

// 规格-运行页 第七节：回合写 model_runs 时带上失败原因类别（error_class）。
// 走真实的回合循环和库，旧代码不写 error_class 时这里会红；这里只看写进库的那一列，分类规则本身在 internal/llm 测。

import (
	"net/http"
	"testing"

	"sumika-native/internal/llm"
)

// classRows 是 model_runs 一行一行的 (stage, error_class)；没有类别的是空串，NULL 和空串不分（下面单独验成功行是 NULL）
func (h *harness) classRows() [][2]string {
	rows, err := h.st.Trace.Query(`SELECT stage, coalesce(error_class, ''), error IS NOT NULL FROM model_runs ORDER BY rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var stage, class string
		var failed bool
		if err := rows.Scan(&stage, &class, &failed); err != nil {
			h.t.Fatal(err)
		}
		if failed && class == "" {
			h.t.Errorf("%s 行有 error 却没有 error_class", stage)
		}
		if !failed && class != "" {
			h.t.Errorf("%s 行没出错却带着类别 %q", stage, class)
		}
		out = append(out, [2]string{stage, class})
	}
	return out
}

// failedAttempt（要重来的那几次）和 endStep（最后一次）都要写类别；成功的最后一行是空
func TestRunErrorClassRetryAndFinal(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		switch n {
		case 1:
			http.Error(w, `{"error":{"message":"上游忙"}}`, http.StatusInternalServerError)
			return true
		case 2:
			w.Header().Set("Retry-After-Ms", "50")
			http.Error(w, "慢一点", http.StatusTooManyRequests)
			return true
		}
		return false
	}})
	h.send("你好")
	got := h.classRows()
	want := [][2]string{{"reply-retry", llm.ClassServerError}, {"reply-retry", llm.ClassRateLimited}, {"reply", ""}}
	if len(got) != len(want) {
		t.Fatalf("行数不对：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 行：%v，应是 %v", i, got[i], want[i])
		}
	}
}

// 不再重来而报出去的错：最后那一行（endStep）带类别，且和 failedAttempt 的是同一套
func TestRunErrorClassFinalFailure(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		status int
		want   string
	}{
		{"请求不对", 400, llm.ClassBadRequest},
		{"没授权", 401, llm.ClassAuth},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
				http.Error(w, "不行", c.status)
				return true
			}})
			<-h.hub.Send(h.c.CharacterID, "你好")
			got := h.classRows()
			if len(got) != 1 || got[0] != [2]string{"reply", c.want} {
				t.Fatalf("应只有一行 reply/%s：%v", c.want, got)
			}
		})
	}
	t.Run("内容过滤", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[{"content":[],"finish":"content_filter",` + usage + `}]`,
			intercept: func(int, string, http.ResponseWriter, *http.Request) bool { return false }})
		<-h.hub.Send(h.c.CharacterID, "你好")
		got := h.classRows()
		if len(got) != 1 || got[0][1] != llm.ClassContentFilter {
			t.Fatalf("内容过滤：%v", got)
		}
	})
}

// 压缩（compact.go）：摘要请求本身出错，那一行带类别；摘要成功的那一行类别为空。
// 先让回复因上下文超长被拒，触发压缩，再让摘要请求被 400 拒绝
func TestRunErrorClassCompact(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `]`, mutate: smallKeep,
		intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
			switch n {
			case 1:
				http.Error(w, overflowText, 400)
				return true
			case 2:
				http.Error(w, `{"error":{"message":"摘要请求被拒"}}`, 400)
				return true
			}
			return false
		}})
	h.seedRounds(8)
	seq := h.storeOwner("现在这句")
	<-h.hub.Respond(consoleInput(h.c.CharacterID, seq, "现在这句"))
	var compactClass string
	for _, r := range h.classRows() {
		if r[0] == "compact" {
			compactClass = r[1]
		}
	}
	if compactClass != llm.ClassBadRequest {
		t.Fatalf("摘要请求被 400 拒绝，compact 行的类别应是 bad_request：%q（%v）", compactClass, h.classRows())
	}
}
