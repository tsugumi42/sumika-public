package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionHelpAndUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if c := Run([]string{"version"}, &out, &errb); c != 0 || !strings.Contains(out.String(), "源码摘要") {
		t.Fatalf("version：%d %q", c, out.String())
	}
	out.Reset()
	if c := Run([]string{"help"}, &out, &errb); c != 0 || !strings.Contains(out.String(), "sumika-dev version") {
		t.Fatalf("help：%d %q", c, out.String())
	}
	if c := Run([]string{"nope"}, &out, &errb); c != 2 {
		t.Fatalf("未知子命令应退出码 2，得到 %d", c)
	}
	if c := Run(nil, &out, &errb); c != 2 {
		t.Fatalf("没有子命令应退出码 2，得到 %d", c)
	}
}
