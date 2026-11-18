// Package watchdog 是看门狗（docs/规格-看门狗.md）：桌面程序按时写心跳，守护进程（sumika.exe -watch）盯着它，
// 异常退出或卡死时留事件记录、弹桌面通知、再拉起；正常退出一概不管。
//
// 这个包不引桌面界面和后台的实现，只依赖协议包 ctlproto（探测控制接口用）和几个小的基础包。
package watchdog
