package app

// 真实模型试验：只在设了 SUMIKA_REAL_IMAGES_OUT（输出目录）时跑，经本机 Magpie 发真请求；数据全在临时目录。
// 看两件事：私聊直给的图确实进了请求、read 读到的图确实进了请求。模型不能看图的分支由 TestPrivateDirectImageRouting 用编的模型覆盖（免费模型额度常被用完，不适合当真实试验）。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func wordsPNG(t *testing.T, word string) []byte {
	t.Helper()
	small := image.NewNRGBA(image.Rect(0, 0, 120, 20))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: small, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(4, 14)}
	d.DrawString(word)
	big := image.NewNRGBA(image.Rect(0, 0, 1200, 200))
	draw.NearestNeighbor.Scale(big, big.Bounds(), small, small.Bounds(), draw.Src, nil)
	var b bytes.Buffer
	if err := png.Encode(&b, big); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type recordedCall struct {
	Model      string   `json:"model"`
	ImageCount int      `json:"imageCount"`
	ImageSHA   []string `json:"imageSha256"`
	ImageBytes []int    `json:"imageBytes"`
	Messages   int      `json:"messages"`
}

func TestRealModelImages(t *testing.T) {
	t.Parallel()
	out := os.Getenv("SUMIKA_REAL_IMAGES_OUT")
	if out == "" {
		t.Skip("设 SUMIKA_REAL_IMAGES_OUT 才跑真实模型试验")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("http://127.0.0.1:3425")
	var mu sync.Mutex
	var calls []recordedCall
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target) // Rewrite 模式不加 X-Forwarded-For，Magpie 会把带这个头的请求当成别的机器
		pr.Out.Host = target.Host
	}}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			t.Logf("上游 %d %s：%s", resp.StatusCode, resp.Request.URL, b)
			resp.Body = io.NopCloser(bytes.NewReader(b))
		}
		return nil
	}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			var req struct {
				Model    string `json:"model"`
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			json.Unmarshal(body, &req)
			c := recordedCall{Model: req.Model, Messages: len(req.Messages)}
			for _, m := range req.Messages {
				var parts []struct {
					Type     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				}
				if json.Unmarshal(m.Content, &parts) != nil {
					continue
				}
				for _, p := range parts {
					if p.Type == "image_url" {
						sum := sha256.Sum256([]byte(p.ImageURL.URL))
						c.ImageCount++
						c.ImageSHA = append(c.ImageSHA, hex.EncodeToString(sum[:8]))
						c.ImageBytes = append(c.ImageBytes, len(p.ImageURL.URL))
					}
				}
			}
			mu.Lock()
			calls = append(calls, c)
			mu.Unlock()
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			r.URL.Path = "/v1" + r.URL.Path
		}
		r.Host = target.Host
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()

	run := func(name, model, extra string, steps func(t *testing.T, e *testEnv, a *App, d *Desktop)) {
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			calls = nil
			mu.Unlock()
			e := newEnv(t)
			e.llm = front
			work := t.TempDir()
			e.extraConfig = "tools:\n  workdir: \"" + filepath.ToSlash(work) + "\"\n" + extra
			e.writeConfig(testToken)
			cfgPath := filepath.Join(e.data, "config", "sumika.yml")
			b, _ := os.ReadFile(cfgPath)
			os.WriteFile(cfgPath, []byte(strings.ReplaceAll(string(b), testModel, model)), 0o644)
			os.WriteFile(filepath.Join(work, "note.png"), wordsPNG(t, "CRYSTAL-5518"), 0o644)
			a := e.open()
			d := a.Desktop()
			defer func() { d.Close(); a.Close() }()
			// 工具要审批：试验里一律放行
			stop := make(chan struct{})
			defer close(stop)
			go func() {
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
			steps(t, e, a, d)
			mu.Lock()
			rec, _ := json.MarshalIndent(calls, "", "  ")
			mu.Unlock()
			os.WriteFile(filepath.Join(out, name+"-requests.json"), rec, 0o644)
			e.logMu.Lock()
			os.WriteFile(filepath.Join(out, name+"-log.txt"), []byte(strings.Join(e.logs, "\n")), 0o644)
			e.logMu.Unlock()
			st, _ := a.set.ForConversation("console:" + testChar)
			var reply string
			st.Events.QueryRow(`SELECT group_concat(text, ' || ') FROM events WHERE conversation=? AND actor_kind<>'owner' AND kind='message'`, "console:"+testChar).Scan(&reply)
			var in, cr int64
			tr := a.set.TraceStore().Trace
			tr.QueryRow(`SELECT coalesce(sum(input_tokens),0), coalesce(sum(cache_read_tokens),0) FROM model_runs`).Scan(&in, &cr)
			var ctxs string
			tr.QueryRow(`SELECT group_concat(context_json, char(10)) FROM (SELECT substr(context_json, instr(context_json,'"images"'), 40) AS context_json FROM model_runs WHERE stage='reply')`).Scan(&ctxs)
			summary := fmt.Sprintf("model=%s\nreply=%s\ninput_tokens=%d cache_read=%d\nrun context images=%s\n", model, reply, in, cr, ctxs)
			os.WriteFile(filepath.Join(out, name+"-summary.txt"), []byte(summary), 0o644)
			t.Log(summary)
		})
	}

	key := localKey("console:" + testChar)
	run("direct", "auto/deepseek-flash", "", func(t *testing.T, e *testEnv, a *App, d *Desktop) {
		img := wordsPNG(t, "SUMIKA-7392")
		os.WriteFile(filepath.Join(out, "direct-input.png"), img, 0o644)
		if err := d.Reader.SendLocalImages(t.Context(), key, "图里写了什么？只回答图上的字。", [][]byte{img}, nil); err != nil {
			t.Fatal(err)
		}
	})
	run("read", "auto/deepseek-flash", "", func(t *testing.T, e *testEnv, a *App, d *Desktop) {
		os.WriteFile(filepath.Join(out, "read-input.png"), wordsPNG(t, "CRYSTAL-5518"), 0o644)
		if err := d.Reader.SendLocal(t.Context(), key, "用 read 工具读一下工作目录里的 note.png，告诉我图上写了什么字，只回答图上的字。", nil); err != nil {
			t.Fatal(err)
		}
	})
}
