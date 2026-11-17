// Package linkreader 提供纯函数链接解析和独立的匿名联网取数。
//
// Recognize 识别 X、B 站、GitHub、YouTube、抖音、网易云、pixiv、BOOTH、Bluesky、Telegram 公开频道帖子、Misskey（misskey.io）和 Mastodon（mastodon.social）、Steam、pixiv 和爱发电、Spotify、TikTok、网易云和 pixiv 和 小黑盒和小红书；Fetcher 有界展开短链并取匿名公开元数据。
// Raw 保留调用者输入，Clean 供展示和缓存使用；未知查询参数及片段原样保留。
// 带凭据和非默认端口的链接拒绝，避免规范化后悄悄改变目标或泄漏凭据。
// 不依赖渠道类型，也不注册插件、Hub 或 app。
package linkreader
