package app

// 设置工具能改的键（规格-客户端自知与设置工具 第十一节）。这张表是宿主的白名单：不在表里的键一律拒绝，
// 不因为配置结构里有这个字段就推断可写；模型看不到也改不了它。数字范围不在这里抄，校验走配置的校验正本（PatchSetting、PatchLink）。
//
// Level 是确认等级。现在每一项都是 LevelEvery（每次让主人在审批卡上确认）。
// 以后想让某一项不用审批：这里换一档，settingtool 的 apply 里加一支，别处不动。

import "sumika-native/internal/settingtool"

const linkEffect = "保存后发布，下一条消息使用"

var settingToolSpecs = []settingtool.Spec{
	{Key: "location", Name: "所在地", Kind: "location", Level: settingtool.LevelEvery, Effect: "立即",
		Impact: "所有者本人的场合（本地私聊、QQ 所有者私聊）用完整的所在地；所有者开放的群只知道城市；别人的私聊当不知道。查天气时坐标会发给 Open-Meteo。"},
	{Key: "linkReader.context.enabled", Name: "给她看链接资料", Kind: "bool", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：她能看到消息里链接的资料和详情工具；关掉不等于关掉自动展开。"},
	{Key: "linkReader.expand.enabled", Name: "链接自动展开", Kind: "bool", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：自动解析链接并把结果发出去，仍受原来的名单和发送许可约束。"},
	{Key: "linkReader.expand.maxLinks", Name: "每条消息最多展开几个链接", Kind: "int", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：一条消息里最多解析几个链接，影响请求量。"},
	{Key: "linkReader.expand.bareBiliID", Name: "识别裸 BV / av 号", Kind: "bool", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：消息里单独的 BV、av 号也当成视频链接。"},
	{Key: "linkReader.expand.rotateOnReject", Name: "图被拒收后旋转重发", Kind: "bool", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：图被 QQ 拒收后转 180 度重发一次。"},
	{Key: "linkReader.expand.panoramaOriginals", Name: "拼接后另附原图", Kind: "bool", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：推特把一张横幅拆成几张发时，拼回的图后面再用合并转发附上原图；关着只发拼好的一张。"},
	{Key: "linkReader.expand.video.enabled", Name: "下载视频", Kind: "bool", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：解析视频链接时下载视频发出去，会增加流量、等待和磁盘占用；不影响普通图片。"},
	{Key: "linkReader.expand.video.timeoutSeconds", Name: "单个视频超时（秒）", Kind: "int", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：视频任务的时限，不能保证提高成功率。"},
	{Key: "linkReader.expand.video.maxMB", Name: "单个视频上限（MB）", Kind: "int", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部启用的会话：单次视频的字节预算，调高会增加流量和磁盘占用。"},
	{Key: "linkReader.expand.privateStatus", Name: "私聊开始解析的提示", Kind: "text", Level: settingtool.LevelEvery, Effect: linkEffect,
		Impact: "全部私聊：开始解析链接时发的那句话（只是显示内容，不是指令）。"},
}

func settingToolSpec(key string) (settingtool.Spec, bool) {
	for _, s := range settingToolSpecs {
		if s.Key == key {
			return s, true
		}
	}
	return settingtool.Spec{}, false
}
