package main

import (
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/transfer"
)

// 浏览器拖来的链接走数据拖放（DataDrop），Tester 没有对应的公开入口，所以把"拆成文件和文字"
// 做成纯函数直接测；文件拖放的整条路径仍由 TestWorkbenchDropFilesFoldersAndImagesOnTheFocusedBlock 覆盖。
func TestDropContentSplitsFilesLinksAndText(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "资料.txt")
	fileURL := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(file)}).String()
	link, err := transfer.URLData("https://example.com/a?b=1")
	if err != nil {
		t.Fatal(err)
	}
	uriList := func(s string) transfer.Data {
		return transfer.New(transfer.NewItem(transfer.Bytes(transfer.URIList, []byte(s))))
	}
	cases := []struct {
		name      string
		data      transfer.Data
		wantPaths []string
		wantText  string
	}{
		{"浏览器拖来的链接放进文字", link, nil, "https://example.com/a?b=1"},
		{"带本地文件的链接列表当文件挂", uriList(fileURL), []string{file}, ""},
		{"纯文字照放，去掉首尾空白", transfer.TextData("  拖来的一句话  "), nil, "拖来的一句话"},
		{"多个链接各占一行，跳过注释", uriList("https://a.example/1\r\n# 注释\r\nhttps://b.example/2"), nil, "https://a.example/1\nhttps://b.example/2"},
		{"非网页链接且没有文字：什么都不放", uriList("javascript:alert(1)"), nil, ""},
		{"空数据", transfer.Data{}, nil, ""},
	}
	for _, c := range cases {
		paths, text := dropContent(c.data)
		if !slices.Equal(paths, c.wantPaths) || text != c.wantText {
			t.Errorf("%s：得到 %v %q，想要 %v %q", c.name, paths, text, c.wantPaths, c.wantText)
		}
	}
}
