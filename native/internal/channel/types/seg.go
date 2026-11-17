package types

// 段的种类（规格-渠道中间件 第三节）。字段和种类名都是中立的：OneBot 的 face / mface / record、at.qq、image.file
// 在适配器翻译时转成这里的，旧库里的转换映射见规格第三节的表（第 7 步导入时用）
const (
	SegText      = "text"
	SegAt        = "at"
	SegReply     = "reply"     // 入站的引用；出站附在 PartMessage.Quote 上
	SegImage     = "image"     // 普通图片
	SegEmoji     = "emoji"     // 表情包和商城表情（协议端推成图片的那种）
	SegFace      = "face"      // 小黄脸
	SegSuperface = "superface" // 超级表情
	SegVoice     = "voice"
	SegVideo     = "video"
	SegFile      = "file"
	SegForward   = "forward" // 合并转发：只存平台消息 id，展开的树不在段里
	SegCard      = "card"    // 小程序、分享卡片
	SegOther     = "other"   // 认不出的：类型加截短的原始数据，不丢（规格-QQ渠道 第三节）
)

// AtAll 是 SegAt 的 Target 里"全体成员"的写法
const AtAll = "all"

// RawMax 是 Seg.Raw、Notice.Raw 的预览上限（rune）；完整 JSON 卡片另放 CardJSON。
const RawMax = 200

// CardJSONMax 是完整卡片载荷的字节上限，见规格-链接解析 3.1：256 KiB，Raw 仍只作预览。
const CardJSONMax = 256 * 1024

// MediaRefMax 是 Seg.URL、Seg.File 的字节上限；真帧里的图片地址约 250 字节
const MediaRefMax = 2048

// Seg 是消息里的一段。一条消息是一串段，纯文本只是从段里派生出来的，库里不存 CQ 码串（规格-QQ渠道 第八节）。
// 字段只写已知的消费者要用的；用到新的（媒体下载地址、尺寸、是否动图……）再加，加字段不会让现有的写法编译不过。
// json 标签是它存进 events.segments_json 的样子（中间件写，历史和界面读）：全小写，空的不写
type Seg struct {
	Kind   string `json:"kind"`             // 上面 Seg* 之一
	Text   string `json:"text,omitempty"`   // text
	Target string `json:"target,omitempty"` // at：被 @ 的号，AtAll 是全体；reply：被引用的平台消息号
	Hash   string `json:"hash,omitempty"`   // image、emoji、video 和 card 封面：文件内容的 sha256，不是临时链接；空表示尚未下载，不能当成相等（规格第十二节）
	Ref    string `json:"ref,omitempty"`    // forward：转发 id；face、superface：编号；emoji：表情 id
	Raw    string `json:"raw,omitempty"`    // other、card：截短到 RawMax 个字的原始数据
	// URL、File 是媒体收到时协议端给的下载地址和文件名；card 的 URL 是封面地址。
	// 地址是会过期的临时链接，只能拿来补下载，不能当图的身份比较（比较只认 Hash）；File 是平台给的名字，不能当本地路径用。
	// 只留 http(s) 地址、不带路径分隔符和控制字符的文件名，超过 MediaRefMax 字节的整个不存；旧行缺字段时为空
	URL  string `json:"url,omitempty"`
	File string `json:"file,omitempty"`
	// CardJSON 用字符串保留解包后对象的原文，避免 JSON 再序列化压紧空白；旧行缺字段时为空。
	CardJSON  string `json:"card_json,omitempty"`
	CardType  string `json:"card_type,omitempty"`  // card：适配器收到的 json / xml 类型
	CardIssue string `json:"card_issue,omitempty"` // card_too_large / card_malformed；正常和 xml 为空
	// 卡片显示字段从完整载荷提取；URL / Hash 在 card 段表示封面，不能拿封面当跳转链接。
	CardTitle       string `json:"card_title,omitempty"`
	CardDescription string `json:"card_description,omitempty"`
	CardSource      string `json:"card_source,omitempty"`
	CardURL         string `json:"card_url,omitempty"`
	// 文件、视频、语音（2026-11-02 真机：接管后 file 段只存了 kind，界面写"[文件：文件]"）。出处是 SnowLuma 的 index.mjs：
	//   file（toSegment 约 21609 行）的 data 是 {file, name（都是文件名）, file_id, id（文件标识，字符串）, file_size, size（字节数，数字）, url, file_hash}；
	//   record、video（约 21527、21550 行）的 data 只有 {file, url}：file 是 fileName 或 fileId（可能是标识不是名字），时长、大小、宽高被丢掉了，段里拿不到。
	// URL、File 沿用上面的规矩（file 段的 File 是文件名；record、video 的 File 可能是标识，只存不显示）；
	// FileID 保存 file、video 的 file_id，是不透明字符串，只限长度和控制字符，不当路径用；
	// Size 是字节数，只收非负整数，0 表示不知道，不写。旧行缺这些字段时为空
	FileID string `json:"file_id,omitempty"`
	// FileHash 是私聊文件取址所需的不透明令牌，不当内容哈希，也不带到展示或日志。
	FileHash string `json:"file_hash,omitempty"`
	// FileGroup 仅来自转发节点明确的 group_id；缺失不猜接收转发的群。
	FileGroup string `json:"file_group,omitempty"`
	Size      int64  `json:"size,omitempty"`
}
