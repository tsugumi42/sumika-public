package linkplugin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 摘要里放不下的一条也走统一的截断写法：截在最近的标点后，补“……”，总长不超过 briefMax。
func TestBriefTruncatesAtPunctuation(t *testing.T) {
	long := strings.Repeat("字", 33) + "，" + strings.Repeat("尾", 20)
	got := Brief([]string{long}, 0)
	if n := utf8.RuneCountInString(got); n > briefMax {
		t.Fatalf("摘要 %d 字，超过 %d：%q", n, briefMax, got)
	}
	if !strings.HasSuffix(got, "……") || !strings.HasSuffix(got, "，……") {
		t.Fatalf("应截在逗号后补“……”：%q", got)
	}
	if strings.Contains(got, "…………") {
		t.Fatalf("省略号重复：%q", got)
	}
}
