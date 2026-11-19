package docs

import (
	"strings"
	"testing"
)

// 回归夹具：2026-11-19 两处真实漏报，原文取自 main db63b0f（PR 20 手改之前）的前几行，行号和真实文件一致。
// 旧 D14（specmerged.go）两处都不报：规格那行的分支名没加反引号、“在分支 X 上”中间夹着分支名；
// 子系统参考那行是小节标题，不在“规格开头的引用块”里。
// 真实历史里这个分支是改名成 feat/write-eol-pub 合进公开仓库的，夹具按文档里写的名字合，测的是“名字对得上时报不报”。
const realSpec1119 = "# 规格：她写文件时用哪种换行\n\n> 状态：在分支 feat/write-eol 上（2026-11-19，未合 main；验收第 4 条真机试等主会话做）。来源：主人 2026-11-19“这个设计本身能不能也融入到sumika里，在某些时候（github仓库内/不同的操作系统等）或者手动设置换行方式？”。起因是仓库自己的 CRLF 问题（同日 `.gitattributes` 改成 `eol=lf`）。\n"

const realTools1119 = "# 工具与审批\n\n开发规则见 [开发指导/权限安全隐私](../开发指导/权限安全隐私.md)、[开发指导/插件与扩展](../开发指导/插件与扩展.md)。\n\nGo 共用身份在 `feat/kernel-int` 统一：`turn.ToolCall.Identity` 和 `PreparedCall.Invocation` 使用 [`kernel.Invocation`](../../native/internal/kernel/types.go)。`Store.StepStart` 在同一条 SQL 中保存宿主 ToolID、StepID、ProviderCallID 和 ordinal；provider ID 仅用于模型协议配对，不能作为步骤主键。steps 的 identity/provider/ordinal 由迁移 0007 所有，step_id/result_ref 由 0008 所有，0009 只增加模型账本字段。审批记录携带同一 Invocation。\n\n## Go 编辑预览与执行\n\n[`edittext.Parse / Apply`](../../native/internal/edittext/edit.go) 是编辑参数与正文计算的唯一实现；[`Gate.Describe`](../../native/internal/approval/approval.go) 的人工审批、自动批准记录与 [`tools.editLocked`](../../native/internal/tools/edit.go) 的实际写入都使用它。所有替换对着同一份原文定位，`newText` 的 `$` 标记按字面处理；空旧文本、找不到、多处匹配、重叠和无变化返回相同错误，预览显示原因，不伪造成功差分。当前工具没有“全部替换”参数。\n\n共用计算处理模糊匹配、BOM 和换行恢复；未改的原始字节沿用执行器规则。审批差分来自最终将写入的正文，超过 6000 个 UTF-16 码元只截显示并标 `Truncated`，实际写入不截断。测试矩阵见 [`edit_preview_test.go`](../../native/internal/edittext/edit_preview_test.go)。这里保证同一文件内容与参数的计算一致；审批等待期间文件被外部修改的并发保护不在本次 A01 修复范围。\n\n## Go 写文件的换行（feat/write-eol，未合 main）\n"

func TestD14RealMisses20261119(t *testing.T) {
	root := mkRepo(t, with(base(),
		"docs/README.md", "# 索引\n\n[换行](规格-写文件换行.md)\n",
		"docs/规格-写文件换行.md", realSpec1119,
		"docs/subsystems/tools.md", realTools1119,
	))
	git(t, root, "checkout", "-q", "-b", "feat/write-eol")
	put(t, root, "eol.txt", "x\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "写文件换行")
	git(t, root, "checkout", "-q", "main")
	git(t, root, "merge", "-q", "--no-ff", "-m", "Merge pull request #1 from tsugumi42/feat/write-eol", "feat/write-eol")
	var where []string
	for _, f := range check(t, root, false) {
		if strings.HasPrefix(f.Check, "D14") {
			where = append(where, f.Where)
		}
	}
	got := strings.Join(where, " ")
	if !strings.Contains(got, "docs/规格-写文件换行.md:3") || !strings.Contains(got, "docs/subsystems/tools.md:13") {
		t.Fatalf("两处真实漏报都该报，实际：%q", got)
	}
}
