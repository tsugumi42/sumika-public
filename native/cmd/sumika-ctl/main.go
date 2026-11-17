// sumika-ctl 是正在运行的 Sumika 桌面程序的命令行遥控器（规格-本机控制接口，ADR-227 第 4 条）：
// 经一条只给当前 Windows 用户的命名管道，对开着的程序做托盘能做的事，并拿到结果。主人和 Agent（Claude Code 里的铃音）都能用。
//
//	sumika-ctl status                 看现在的状态
//	sumika-ctl doctor                 环境清单：每项现在什么样、下一步做什么；有“有问题”时退出码 1
//	sumika-ctl receive start|stop     开始 / 停止接收 QQ（等于托盘那两项）；start 可加 --wait[=60s] 等结果
//	sumika-ctl window show            把主窗口叫出来
//	sumika-ctl logs [-n 50]           看桌面程序日志的最后几行
//	sumika-ctl gate list              列出各群的档位
//	sumika-ctl gate set <会话> off|dry 把一个群调成只记录或试运行（speak 不在这里：让群真开口请在界面上改）
//	sumika-ctl chat [--character <id>] [--new] [--timeout 120s] "<文字>"
//	                                  自检对话：以主人身份往该角色的自检话题发一句话，等这一轮结束，回她的话、
//	                                  调用的工具、用的模型；停在审批上回 waiting_approval，不替用户批。
//	                                  自检话题里的话带 selftest 标记：不进长期记忆、不往 QQ 或别处发；--new 开新话题
//	sumika-ctl goal [show|set <目标>|edit <目标>|pause|resume|clear]
//	                                  目标模式（和输入框 /goal 同一套逻辑，会话是自检话题）
//	sumika-ctl loop [list|add [间隔] <任务>|edit <编号> ...|pause|resume|remove <编号>]
//	                                  定期任务（和输入框 /loop 同一套逻辑）；add、edit 还认
//	                                  --every 30m | --daily 09:00 | --weekly 1,3,5 --at 09:00 | --self、
//	                                  --new-chat | --same-chat、--notify content|errors|none
//	sumika-ctl trust                  项目说明拒绝后想改主意：下一次重新问（和输入框 /trust 同一套逻辑）
//	sumika-ctl artist list|import|confirm|remove
//	                                  画师表（X 画师，按账号数字 ID；规格-画师原图）：list 只读，默认列待确认的；import <文件> 导入确认页
//	                                  导出的 artists-confirmed.json（可重复、import --undo <批次> 撤回）；confirm、remove 改状态
//	sumika-ctl backup now|list        手动备份 / 列出备份并给总览（占用、规则、剩余空间、上次抽验；规格-数据目录与备份 10.7）
//	sumika-ctl backup restore|verify  把一份备份恢复成新目录并核对 / 抽验（恢复到临时目录核对后删掉；规格-数据目录与备份 10.10）
//	sumika-ctl kernel faults          回合内核没解除的故障（哪个会话停止受理、什么原因；自检 kernel.faulted 对应这里，只读）
//	sumika-ctl kernel retry [<角色> <会话>]
//	                                  重试故障：重写没写成的写入并读回确认，成功就恢复受理，没恢复就失败；不写 = 全部故障
//	sumika-ctl mcp status|release     各 MCP 服务的状态（含被隔离的原因）/ 主人带原因接受“旧调用结果未知”的风险、解除隔离
//	sumika-ctl proactive log [-n 50]  主动联系的导演日志（只读）
//	sumika-ctl auto-receive on|off    启动后自动接收 QQ
//	sumika-ctl autostart on|off       开机自动启动
//	sumika-ctl quit                   退出
//
// 每个命令都能加 --json，输出一个 JSON 对象。退出码：0 成功，1 命令执行失败，2 桌面程序没在运行，3 被拒（参数不允许），4 协议版本不对。
// 这个程序只依赖协议包（internal/ctlproto），不链接后台（internal/archcheck 守着）：要小、要快。
package main

import (
	"context"
	"net"
	"os"

	"sumika-native/internal/ctlproto"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, dialPipe))
}

// dialPipe 连当前用户的管道；demo 为真连演示数据的那一条
func dialPipe(ctx context.Context, demo bool) (net.Conn, error) {
	name, err := ctlproto.PipeName(demo)
	if err != nil {
		return nil, err
	}
	return ctlproto.Dial(ctx, name)
}
