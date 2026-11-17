package linkexpand

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

type dynTransport struct{ t *testing.T }

func (d dynTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	name := "bili-opus-fallback-1900000000000000100.json"
	if req.URL.Path == "/x/polymer/web-dynamic/v1/detail" {
		name = "bili-dynamic-forward-1900000000000000100.json"
	}
	b, err := os.ReadFile("../linkreader/testdata/" + name)
	if err != nil {
		d.t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: nopCloser{strings.NewReader(string(b))}}, nil
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

// 转发动态（opus/detail 回 fallback、普通动态详情取到）要能一路排成消息：原动态的作者、标题、正文、图都在发出去的内容里。
func TestDynamicForwardRenders(t *testing.T) {
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: dynTransport{t}})
	if err != nil {
		t.Fatal(err)
	}
	l, _ := linkreader.Recognize("https://www.bilibili.com/opus/1900000000000000100")
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]Options{"auto": {}, "always": {Sites: map[string]SiteSetting{"bilibili-dynamic": {Forward: "always"}}}} {
		d := build(r, o)
		if got := strings.Join(kinds(d), ","); got != "forward" {
			t.Fatalf("%s：%s", name, got)
		}
		var texts []string
		images := 0
		for _, p := range d.Parts {
			for _, n := range p.Nodes {
				for _, it := range n.Body {
					switch it.Kind {
					case "text":
						texts = append(texts, it.Text)
					case "image":
						images++
					}
				}
			}
		}
		all := strings.Join(texts, "\n")
		for _, want := range []string{"UP主：编造作者", "这是编造", "引用 编造作者：", "《编造的B站标题》", "这是编造的B站示例正文"} {
			if !strings.Contains(all, want) {
				t.Errorf("%s：发出去的内容里没有 %q：\n%s", name, want, all)
			}
		}
		if images != 1 {
			t.Errorf("%s：原动态的图应该有 1 张，实际 %d", name, images)
		}
	}
}
