package linkplugin

// 清单说明要把展开的风险写清楚（规格-插件两层 第二节第 1 条：说明照设计语言第十一节，主人按它判断风险）。
// 展开开着时会把展开内容经 Outlet.Submit 发进所选会话，说明里要有这一层。
// 放回旧代码（说明只写了读取和参考信息）就红。

import (
	"strings"
	"testing"
)

func TestManifestSubSaysExpandSendsIntoConversation(t *testing.T) {
	t.Parallel()
	sub := (&Plugin{}).Manifest().Sub
	if !strings.Contains(sub, "展开") || !strings.Contains(sub, "发进") {
		t.Fatalf("清单说明没写展开会往会话里发内容：%q", sub)
	}
}
