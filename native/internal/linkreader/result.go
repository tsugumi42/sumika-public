package linkreader

import (
	"errors"
	"fmt"
	"time"
)

var ErrMediaLimit = errors.New("媒体超过字节上限")

// Field 保留缺失原因；零值不等于平台返回的零。
type Field[T any] struct {
	State     string `json:"state"`
	Value     T      `json:"value"`
	Reason    string `json:"reason,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func available[T any](v T) Field[T]         { return Field[T]{State: "available", Value: v} }
func missing[T any](reason string) Field[T] { return Field[T]{State: "not_returned", Reason: reason} }

type Author struct {
	Name    Field[string] `json:"name"`
	Account Field[string] `json:"account"`
	ID      Field[string] `json:"id"`
	Avatar  Field[string] `json:"avatar"`
	// Verification 是认证标（X 才有）：none / blue / business / government；推特卡片画认证标用，文字版不用。
	// State 不是 available（包括旧缓存里的空值）就是不知道，卡片上不画。
	Verification Field[string] `json:"verification"`
	// Profile 是账号资料（只有 X 的 fx_v2 线路给，其余线路和旧缓存是 nil）：按需判断是不是画师用，不进任何发出去的文字
	Profile *AuthorProfile `json:"profile,omitempty"`
}

// AuthorProfile 是作者的简介、地点、主页链接和几个数字（外来文字，只用来匹配，不进日志）；数字没给是 -1。
type AuthorProfile struct {
	Bio        string   `json:"bio"`
	Location   string   `json:"location,omitempty"`
	Links      []string `json:"links,omitempty"`
	Followers  int64    `json:"followers"`
	MediaCount int64    `json:"media_count"`
	Statuses   int64    `json:"statuses"`
}

// 认证标的几种
const (
	VerifyNone       = "none"
	VerifyBlue       = "blue"
	VerifyBusiness   = "business"
	VerifyGovernment = "government"
)

type MediaVariant struct {
	URL       Field[string] `json:"url"`
	Container string        `json:"container"`
	Bitrate   Field[int64]  `json:"bitrate"`
	Width     Field[int]    `json:"width"`
	Height    Field[int]    `json:"height"`
	Size      Field[int64]  `json:"declared_bytes"`
}
type ExternalLink struct {
	URL      Field[string] `json:"url"`
	Original Field[string] `json:"original"`
	Display  Field[string] `json:"display"`
}
type LinkPreview struct {
	URL         Field[string] `json:"url"`
	Title       Field[string] `json:"title"`
	Description Field[string] `json:"description"`
	Site        Field[string] `json:"site"`
	Image       Field[Media]  `json:"image"`
}
type Media struct {
	Type      string                `json:"type"`
	URL       Field[string]         `json:"url"`
	Thumbnail Field[string]         `json:"thumbnail"`
	Width     Field[int]            `json:"width"`
	Height    Field[int]            `json:"height"`
	Duration  Field[float64]        `json:"duration_seconds"`
	Size      Field[int64]          `json:"declared_bytes"`
	Variants  Field[[]MediaVariant] `json:"variants"`
	Sensitive Field[bool]           `json:"sensitive"`
	Referer   string                `json:"referer,omitempty"`
	Fallback  *MediaFallback        `json:"fallback,omitempty"`
}
type Block struct {
	Type          string         `json:"type"`
	Text          string         `json:"text,omitempty"`
	Media         *Media         `json:"media,omitempty"`
	Quote         *Result        `json:"quote,omitempty"`
	ParagraphType *int           `json:"paragraph_type,omitempty"`
	SourceType    string         `json:"source_type,omitempty"`
	Links         []ExternalLink `json:"links,omitempty"`
	State         string         `json:"state,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

// PollChoice 是投票的一个选项；票数、百分比各自可能缺。
type PollChoice struct {
	Label   string         `json:"label"`
	Count   Field[int64]   `json:"count"`
	Percent Field[float64] `json:"percent"`
}

// PollInfo 是推文里的投票。EndsAt 缺时不能断言结束没有；VX 只给选项和票数，Total 是票数相加。
type PollInfo struct {
	Choices []PollChoice     `json:"choices"`
	Total   Field[int64]     `json:"total_votes"`
	EndsAt  Field[time.Time] `json:"ends_at"`
}

type VideoPart struct {
	Number   int           `json:"number"`
	CID      string        `json:"cid"`
	Title    Field[string] `json:"title"`
	Duration Field[int]    `json:"duration_seconds"`
}

// Result 仅含可序列化数据，缓存不需要保存客户端或上下文。
type Result struct {
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	CleanURL string `json:"clean_url"`
	// SkipCache 是这次有字段因临时失败没取到（例如小黑盒问 Steam 游戏名超时）：结果照常用，但不进缓存，下次重取
	SkipCache     bool                        `json:"-"`
	DisplayURL    string                      `json:"display_url,omitempty"` // 发出去的链接和 CleanURL 不同时才有（GitHub 保住用户写的大小写，见 githubDisplay），用 ShareURL 取
	Author        Author                      `json:"author"`
	Published     Field[time.Time]            `json:"published"`
	Title         Field[string]               `json:"title"`
	Text          Field[string]               `json:"text"`
	Media         Field[[]Media]              `json:"media"`
	Mosaic        Field[Media]                `json:"mosaic"` // X 三张及以上图的拼图（fxtwitter 给的 jpeg 拼接图）；没有时按 State 判断
	Quote         Field[*Result]              `json:"quote"`
	ReplyTo       Field[string]               `json:"reply_to"`
	Thread        Field[[]string]             `json:"thread"`
	Article       Field[string]               `json:"article"`
	Links         Field[[]ExternalLink]       `json:"links"`
	LinkCard      Field[LinkPreview]          `json:"link_card"`
	Expansion     Field[string]               `json:"expansion"`
	MoreQuotes    Field[bool]                 `json:"more_quotes"`
	Category      Field[string]               `json:"category"`
	Tags          Field[[]string]             `json:"tags"`
	Parts         Field[[]VideoPart]          `json:"parts"`
	SelectedPart  Field[VideoPart]            `json:"selected_part"`
	DynamicType   Field[int]                  `json:"dynamic_type"`
	PublishedText Field[string]               `json:"published_text"`
	Comments      Field[[]Comment]            `json:"comments"` // 视频、专栏的前几条热评；没取到时 Reason 写明原因（CommentsText 出文案）
	Stats         map[string]Field[int64]     `json:"stats"`
	Flags         map[string]Field[jsonValue] `json:"flags"`
	Blocks        []Block                     `json:"blocks"`
	FetchedAt     time.Time                   `json:"fetched_at"`
	Source        string                      `json:"source"`
	GitHub        *GitHubInfo                 `json:"github,omitempty"`
	Music         *MusicInfo                  `json:"music,omitempty"`
	Stage         string                      `json:"stage,omitempty"` // metadata 或 content；空值为既有完整结果。
	Ugoira        *UgoiraInfo                 `json:"ugoira,omitempty"`
	Novel         *NovelInfo                  `json:"novel,omitempty"`
	Mall          *MallInfo                   `json:"mall,omitempty"`
	Workshop      *WorkshopInfo               `json:"workshop,omitempty"`
	Bangumi       *BangumiInfo                `json:"bangumi,omitempty"`
	Course        *CourseInfo                 `json:"course,omitempty"`
	Collection    *CollectionInfo             `json:"collection,omitempty"`
	Booth         *BoothInfo                  `json:"booth,omitempty"`
	Steam         *SteamInfo                  `json:"steam,omitempty"`
	Poll          *PollInfo                   `json:"poll,omitempty"`
	Series        *SeriesInfo                 `json:"series,omitempty"`
	// Attempts 是这次取数试过的线路（X 才有，twitter_routes.go）；只给日志用，不进缓存
	Attempts []RouteAttempt `json:"-"`
	// RetriedAfterTimeout 说这次结果来自“全部超时后隔 5 秒再试的一轮”（规格 C3）；插件日志的 note 用。
	// 要进缓存的 JSON：Service 把结果在等待者之间按 JSON 转一份，标记跟着走；命中缓存时插件按 cache 写 note，不用它
	RetriedAfterTimeout bool `json:"retried_after_timeout,omitempty"`
}

// MediaFallback 保留下载阶段的退档条件；元数据阶段不知道文件大小，不能声称已经退档。
type MediaFallback struct {
	URL        Field[string] `json:"url"`
	AboveBytes int64         `json:"above_bytes"`
	Reason     string        `json:"reason"`
	Notice     string        `json:"notice"`
}
type UgoiraFrame struct {
	File    string `json:"file"`
	DelayMS int    `json:"delay_ms"`
}
type UgoiraInfo struct {
	ZIP      Field[string]        `json:"zip"`
	Frames   Field[[]UgoiraFrame] `json:"frames"`
	MIMEType Field[string]        `json:"mime_type"`
	Referer  string               `json:"referer"`
}
type NovelInfo struct {
	Description    Field[string] `json:"description"`
	CharacterCount Field[int64]  `json:"character_count"`
	Cover          Field[Media]  `json:"cover"` // 小说封面（coverUrl），和正文里的插图分开放
}

// SeriesLink 是系列导航里的上一话或下一话。
type SeriesLink struct {
	ID    string        `json:"id"`
	Title Field[string] `json:"title"`
	Order Field[int64]  `json:"order"`
}

// SeriesInfo 是 pixiv 的系列信息。作品页带的是导航（本作第几话、上一话、下一话）；系列页带的是总数和首末话 ID。
type SeriesInfo struct {
	Type     string        `json:"type"` // manga 或 novel
	ID       string        `json:"id"`
	Title    Field[string] `json:"title"`
	Order    Field[int64]  `json:"order"`
	Total    Field[int64]  `json:"total"`
	Prev     *SeriesLink   `json:"prev,omitempty"`
	Next     *SeriesLink   `json:"next,omitempty"`
	FirstID  string        `json:"first_id,omitempty"`
	LatestID string        `json:"latest_id,omitempty"`
}

// SteamPrice 按平台给的最小货币单位（分）保存，不换算；Formatted 是平台的展示串。
type SteamPrice struct {
	Currency        Field[string] `json:"currency"`
	Initial         Field[int64]  `json:"initial"`
	Final           Field[int64]  `json:"final"`
	DiscountPercent Field[int64]  `json:"discount_percent"`
	Formatted       Field[string] `json:"formatted"`
}

// SteamAppRef 是打包里包含的应用；ID 保持字符串，不转数字。
type SteamAppRef struct {
	ID   Field[string] `json:"id"`
	Name Field[string] `json:"name"`
}

// SteamInfo 只放 Steam 独有的字段；通用字段仍在 Result 上。
type SteamInfo struct {
	Type          Field[string]        `json:"type"`
	Price         Field[SteamPrice]    `json:"price"`
	IsFree        Field[bool]          `json:"is_free"`
	ComingSoon    Field[bool]          `json:"coming_soon"`
	RequiredAge   Field[int]           `json:"required_age"`
	Platforms     Field[[]string]      `json:"platforms"`
	Developers    Field[[]string]      `json:"developers"`
	Publishers    Field[[]string]      `json:"publishers"`
	DescriptorIDs Field[[]int64]       `json:"content_descriptor_ids"`
	Apps          Field[[]SteamAppRef] `json:"apps"`
	ConsumerAppID Field[string]        `json:"consumer_app_id"`
	FileSize      Field[int64]         `json:"file_size_bytes"`
	Updated       Field[time.Time]     `json:"updated"`
}

type GitHubInfo struct {
	State    Field[string]    `json:"state"`
	Language Field[string]    `json:"language"`
	License  Field[string]    `json:"license"`
	Updated  Field[time.Time] `json:"updated"`
}
type MusicInfo struct {
	Artists Field[[]Author] `json:"artists"`
	Album   Field[string]   `json:"album"`
}

// jsonValue 只保存平台标记的 JSON 值，不解释成权限结论。
type jsonValue = interface{}

type FetchError struct {
	Kind       string `json:"kind"`
	Source     string `json:"source,omitempty"`
	Status     int    `json:"http_status,omitempty"`
	Code       int    `json:"code,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Trail 是这次取数经过的线路和各自耗时（“fx_v2 timeout 10.0秒 → vx timeout 10.0秒”），只给日志用，不含网址。
	Trail string `json:"trail,omitempty"`
	// Detail 是网络错误的底层原文（EOF、connection reset、哪一步超时），只给日志用，不含网址（2026-11-11 主人：查不出 B 站失败原因）。
	Detail string `json:"-"`
	// Attempts 和 Trail 同一份内容的结构化版本（X 才有），给日志逐条线路记
	Attempts []RouteAttempt `json:"-"`
	// RetriedAfterTimeout 说这次失败是“全部超时后整条重试”之后的第二轮（规格 C3）；插件日志的 note 用
	RetriedAfterTimeout bool `json:"-"`
}

// routeTrail 在已有线路记录后面接一段“线路 原因 耗时”。
func routeTrail(prev, source, kind string, took time.Duration) string {
	s := fmt.Sprintf("%s %s %.1f秒", source, kind, took.Seconds())
	if prev == "" {
		return s
	}
	return prev + " → " + s
}

func (e *FetchError) Error() string           { return "linkreader: " + e.Kind + " (" + e.Source + ")" }
func failure(kind, source string) *FetchError { return &FetchError{Kind: kind, Source: source} }
func newResult(l Link, source string) Result {
	return Result{Platform: l.Platform, Kind: l.Kind, ID: l.ID, CleanURL: l.Clean, Source: source, FetchedAt: time.Now().UTC(),
		Author:    Author{Name: missing[string]("interface_omitted"), Account: missing[string]("interface_omitted"), ID: missing[string]("interface_omitted"), Avatar: missing[string]("interface_omitted"), Verification: missing[string]("interface_omitted")},
		Published: missing[time.Time]("interface_omitted"), Title: missing[string]("interface_omitted"), Text: missing[string]("interface_omitted"),
		Media: missing[[]Media]("interface_omitted"), Mosaic: missing[Media]("not_applicable"), Quote: missing[*Result]("interface_omitted"), ReplyTo: missing[string]("interface_omitted"),
		Thread: Field[[]string]{State: "unsupported", Reason: "single_post_only"}, Article: missing[string]("not_applicable"),
		Links: missing[[]ExternalLink]("interface_omitted"), LinkCard: missing[LinkPreview]("interface_omitted"), Expansion: missing[string]("not_applicable"), MoreQuotes: missing[bool]("not_applicable"),
		Category: missing[string]("interface_omitted"), Tags: missing[[]string]("interface_omitted"), Parts: missing[[]VideoPart]("interface_omitted"), SelectedPart: missing[VideoPart]("interface_omitted"),
		DynamicType: missing[int]("not_applicable"), PublishedText: missing[string]("interface_omitted"),
		Stats: map[string]Field[int64]{}, Flags: map[string]Field[jsonValue]{}}
}
