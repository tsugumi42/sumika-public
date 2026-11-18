package main

import (
	"errors"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/llm"
	"sumika-native/internal/turn"
)

// 一轮的结局写成日常说法：常驻一行说发生了什么、怎么办；技术细节（供应商原文）放在折叠里。
// 不把原始错误串直接糊给用户，也不吞：认得出的写原因，认不出的写“没认出来”并把原文放进折叠。

// runFail 是要显示的失败：Line 一行，Detail 折叠。
type runFail struct{ Line, Detail string }

// runEndFail 给一轮的结局定要显示什么；正常结束和手动停止返回零值。
func runEndFail(v api.LocalRunSnapshot) runFail {
	switch v.Outcome {
	case "done", "stopped", "":
		if v.ReasonClass == "exit" {
			return runFail{Line: "程序退出时被打断了，这一轮没有回完"}
		}
		return runFail{}
	case "interrupted":
		return runFail{Line: "程序退出时被打断了，这一轮没有回完"}
	case "budget_exhausted":
		return runFail{Line: "这一轮用的额度到顶了，她先停下了。想让她接着做就说“继续”", Detail: v.ReasonDetail}
	}
	model := "当前模型"
	if v.ReasonModel != "" {
		model = "模型“" + v.ReasonModel + "”"
	}
	line := ""
	switch v.ReasonClass {
	case llm.ClassModelUnknown:
		line = model + "不可用：服务商不认这个模型。到设置里换一个能用的模型再发"
	case llm.ClassAuth:
		line = model + "的服务拒绝了密钥或权限。到设置里检查模型服务的密钥"
	case llm.ClassRateLimited:
		line = "被模型服务限流了（请求太密或额度用完）。过一会儿再发"
	case llm.ClassTimeout:
		line = "等模型回复超时了。可以再发一次"
	case "local":
		line = "读本地文件出错了（角色卡、提示词或工作目录）。点“详情”看是哪个文件"
	case llm.ClassNetwork, llm.ClassStreamError:
		line = "和模型服务的网络连接断了。检查网络或代理后再发"
	case llm.ClassServerError:
		line = "模型服务那边出错了。过一会儿再试"
	case llm.ClassContextOverflow:
		line = "这段对话太长，超出了" + model + "的上限。先压缩对话再发"
	case llm.ClassContentFilter:
		line = "这条内容被模型服务的过滤拦下了，没有回复"
	case llm.ClassBadRequest:
		line = "模型服务拒绝了这次请求，可能是内容格式不合要求"
	case llm.ClassParseFailed:
		line = "她的回复格式出了问题，没能解析。可以再发一次"
	case llm.ClassLoopDetected:
		line = "她在原地打转，已被拦下。换个说法再试"
	case llm.ClassCancelled:
		line = "这次请求被取消了"
	case llm.ClassBudgetExhausted:
		line = "这一轮用的额度到顶了，她先停下了"
	default:
		line = "回复出错了，原因没有认出来。点“详情”看原文"
	}
	return runFail{Line: line, Detail: v.ReasonDetail}
}

// userFail 把一次后台调用的失败写给用户：已经是写给人看的哨兵错误原样用，其余写成一句话，原文进折叠。
func userFail(what string, err error) runFail {
	switch {
	case err == nil:
		return runFail{}
	case errors.Is(err, api.ErrLocalChatBusy), errors.Is(err, api.ErrLocalChatUnsupported), errors.Is(err, api.ErrLocalGroupBusy),
		errors.Is(err, api.ErrQuoteTarget):
		return runFail{Line: what + "：" + firstLine(err.Error())}
	}
	if kernelWide, ok := faultKind(err); ok {
		// 常驻一行说日常话；命令行的办法和原始错误放折叠里。
		// 临时：界面里的“重试”按钮还没做（后续路线 P0-4 的界面部分，挂待办）；按钮做好后这里加上它，折叠里的命令行说法可以去掉。
		line := what + "：这个对话暂时停下了，刚才有内容没能保存。别的对话不受影响，重启 Sumika 可以恢复。"
		if kernelWide {
			line = what + "：所有对话暂时停下了，有内容没能保存。重启 Sumika 可以恢复。"
		}
		return runFail{Line: line, Detail: "也可以运行 sumika-ctl kernel retry 重试\n" + err.Error()}
	}
	if class := llm.ClassifyError(err); class != "" && class != llm.ClassUnknown && class != llm.ClassCancelled {
		f := runEndFail(api.LocalRunSnapshot{Outcome: "error", ReasonClass: class, ReasonDetail: err.Error()})
		f.Line = what + "：" + f.Line
		return f
	}
	return runFail{Line: what + "（后台出错了）。可以再试一次，点“详情”看原文", Detail: err.Error()}
}

// faultKind 认回合内核的故障（turn.FaultError）：这个对话（false）或整个内核（true）暂时不受理新输入。
// 错误走 RPC 以后只剩文字，认的是 turn 里同一个哨兵文字（不会和后台的写法各改各的）。
func faultKind(err error) (kernelWide, ok bool) {
	msg := firstLine(err.Error())
	switch {
	case strings.Contains(msg, turn.ErrKernelFaulted.Error()):
		return true, true
	case strings.Contains(msg, turn.ErrConversationFaulted.Error()):
		return false, true
	}
	return false, false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
