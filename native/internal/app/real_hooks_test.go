package app

// 真实模型试验（规格-用户钩子 第十二节第 9 条）：只在设了 SUMIKA_REAL_HOOKS_OUT（输出目录）时跑，经本机 Magpie 发真请求；
// 数据目录和工作目录全在临时目录，不碰真实 data/ 和运行中的后台。
// 看两件事：
//  1. 模型用 edit 改了 Go 文件以后，PostToolUse 的 gofmt 真跑了，结果进了下一次请求；
//  2. 回合要结束时 Stop 的 go vet 没过，模型接着修，修好后结束（续跑次数有上限）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const realHooksModel = "auto/deepseek-flash"

func TestRealModelUserHooks(t *testing.T) {
	t.Parallel()
	out := os.Getenv("SUMIKA_REAL_HOOKS_OUT")
	if out == "" {
		t.Skip("设 SUMIKA_REAL_HOOKS_OUT 才跑真实模型试验")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("http://127.0.0.1:3425")
	var mu sync.Mutex
	var bodies []string
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = target.Host
	}}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			mu.Lock()
			bodies = append(bodies, string(body))
			mu.Unlock()
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			r.URL.Path = "/v1" + r.URL.Path
		}
		r.Host = target.Host
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()

	type scenario struct {
		name, hooks, prompt string
		files               map[string]string
		check               func(t *testing.T, bodies []string, work string)
	}
	scenarios := []scenario{
		{
			name:  "posttooluse-gofmt",
			hooks: `{"hooks":{"PostToolUse":[{"matcher":"Edit|Write","paths":["**/*.go"],"hooks":[{"type":"command","command":"gofmt -l \"$env:SUMIKA_FILE_PATH\"","timeout":30}]}]}}`,
			files: map[string]string{
				"go.mod":  "module hookdemo\n\ngo 1.22\n",
				"main.go": "package main\n\nfunc add(a int,b int) int {return a+b}\n\nfunc main() { _ = add(1, 2) }\n",
			},
			prompt: "工作目录里有 main.go。请用 edit 工具把函数名 add 改成 sum（定义和调用都改，其余一个字都别动，也别运行 gofmt），改完告诉我。",
			check: func(t *testing.T, bodies []string, work string) {
				var seen bool
				for _, b := range bodies {
					if strings.Contains(b, "用户钩子 PostToolUse") && strings.Contains(b, "main.go") {
						seen = true
					}
				}
				if !seen {
					t.Error("gofmt 的结果没进任何一次后续请求")
				}
			},
		},
		{
			name:  "stop-govet",
			hooks: `{"hooks":{"Stop":[{"paths":["**/*.go"],"hooks":[{"type":"command","command":"go vet ./...","onFailure":"block","timeout":120}]}]}}`,
			files: map[string]string{
				"go.mod": "module hookdemo\n\ngo 1.22\n",
				// 故意留一个 vet 过不了的编译错误（x 声明了没用）：模型只被要求加一行，不告诉它就不会去改
				"main.go": "package main\n\nfunc main() {\n\tx := 1\n}\n",
			},
			prompt: "请用 edit 工具在 main.go 的 main 函数最后加一行 println(\"hi\")，只加这一行，改完就告诉我做完了。",
			check: func(t *testing.T, bodies []string, work string) {
				var fed bool
				for _, b := range bodies {
					if strings.Contains(b, "用户配置的 Stop 钩子的反馈") && strings.Contains(b, "declared and not used") {
						fed = true
					}
				}
				if !fed {
					t.Error("go vet 的报错没有作为 Stop 钩子反馈送回给模型")
				}
				if len(bodies) < 3 {
					t.Errorf("只请求了 %d 次：模型没有因为钩子继续", len(bodies))
				}
				final, _ := os.ReadFile(filepath.Join(work, "main.go"))
				if !strings.Contains(string(final), "println") {
					t.Errorf("模型没有完成原来的改动：%s", final)
				}
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			mu.Lock()
			bodies = nil
			mu.Unlock()
			e := newEnv(t)
			e.llm = front
			work := t.TempDir()
			for name, content := range sc.files {
				if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			e.extraConfig = "tools:\n  workdir: \"" + filepath.ToSlash(work) + "\"\n"
			e.writeConfig(testToken)
			cfgPath := filepath.Join(e.data, "config", "sumika.yml")
			b, _ := os.ReadFile(cfgPath)
			os.WriteFile(cfgPath, []byte(strings.ReplaceAll(string(b), testModel, realHooksModel)), 0o644)
			if err := os.WriteFile(filepath.Join(e.data, "config", "hooks.json"), []byte(sc.hooks), 0o644); err != nil {
				t.Fatal(err)
			}
			a := e.open()
			d := a.Desktop()
			defer func() { d.Close(); a.Close() }()
			stop := make(chan struct{})
			defer close(stop)
			go func() { // 工具要审批：试验里一律放行（钩子本身不经审批）
				for {
					select {
					case <-stop:
						return
					case <-time.After(200 * time.Millisecond):
					}
					for _, s := range a.approvals.Snapshot() {
						if s.Decision == nil {
							a.approvals.Decide(s.Request.ID, "once", "")
						}
					}
				}
			}()
			start := time.Now()
			if err := d.Reader.SendLocal(t.Context(), localKey("console:"+testChar), sc.prompt, nil); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			got := append([]string(nil), bodies...)
			mu.Unlock()
			for i, body := range got {
				os.WriteFile(filepath.Join(out, fmt.Sprintf("%s-request-%d.json", sc.name, i+1)), []byte(body), 0o644)
			}
			st, _ := a.set.ForConversation("console:" + testChar)
			var reply string
			st.Events.QueryRow(`SELECT group_concat(text, ' || ') FROM events WHERE conversation=? AND actor_kind<>'owner' AND kind='message'`, "console:"+testChar).Scan(&reply)
			rows, _ := a.set.TraceStore().Trace.Query(`SELECT outcome, coalesce(class,''), coalesce(detail,'') FROM task_runs WHERE module='hooks' ORDER BY id`)
			var runs []string
			for rows != nil && rows.Next() {
				var o, c, dt string
				rows.Scan(&o, &c, &dt)
				runs = append(runs, o+" · "+c+" · "+dt)
			}
			if rows != nil {
				rows.Close()
			}
			final, _ := os.ReadFile(filepath.Join(work, "main.go"))
			summary, _ := json.MarshalIndent(map[string]any{"model": realHooksModel, "requests": len(got), "seconds": int(time.Since(start).Seconds()),
				"reply": reply, "hookRuns": runs, "finalMainGo": string(final)}, "", "  ")
			os.WriteFile(filepath.Join(out, sc.name+"-summary.json"), summary, 0o644)
			e.logMu.Lock()
			os.WriteFile(filepath.Join(out, sc.name+"-log.txt"), []byte(strings.Join(e.logs, "\n")), 0o644)
			e.logMu.Unlock()
			t.Logf("%s", summary)
			if len(runs) == 0 {
				t.Error("task_runs 里没有 hooks 的运行记录")
			}
			if stops := strings.Count(strings.Join(runs, ";"), "Stop · "); stops > 4 {
				t.Errorf("Stop 钩子跑了 %d 次：续跑上限 3 没拦住", stops)
			}
			sc.check(t, got, work)
		})
	}
}
