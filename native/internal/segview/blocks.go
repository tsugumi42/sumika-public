package segview

import (
	"regexp"
	"strings"

	"sumika-native/internal/config"
)

type Segment struct {
	Kind, Text, Target, Ref                                string
	HasMedia, HasHash, HasCover, HasCoverHash, CanDownload bool
	DurationMS, Size                                       int64
	Width, Height                                          int
	Animated, Sticker                                      bool
	FetchState, FileKind, MediaState, Issue                string
	Summary, Name                                          string
	Links                                                  []string
	Card                                                   *Card
	Forward                                                *Forward
	Position                                               int
}

type Card struct{ Title, Description, Source, URL string }
type Forward struct {
	Title          string
	Lines          [3]string
	Count, Missing int
	State          string
}

type Kind int

const (
	Inline Kind = iota
	Images
	Note
	StickerNote
	CardBlock
	ForwardBlock
	Video
	File
)

type Block struct {
	Kind       Kind
	Runs       []Run
	Imgs       []Segment
	Text       string
	Card       Segment
	Video      Segment
	VideoIndex int
}

func (b Block) HasFace() bool {
	for _, r := range b.Runs {
		if r.Face != nil {
			return true
		}
	}
	return false
}
func (b Block) Plain() string {
	var sb strings.Builder
	for _, r := range b.Runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}
func (s Segment) DisplayName() string {
	switch s.Kind {
	case "emoji":
		name := s.Name
		if name == "" {
			name = s.Summary
		}
		if n := strings.TrimSuffix(strings.TrimPrefix(name, "["), "]"); n != "" {
			return n
		}
		return "表情包"
	case "file":
		if s.Name != "" {
			return s.Name
		}
		return "文件"
	}
	return s.Name
}
func TextBlocks(text string) []Block {
	budget := 20
	return TrimInline(&Block{Kind: Inline, Runs: LinkRuns(CleanText(text), &budget)})
}
func TrimInline(b *Block) []Block {
	rs := TrimRuns(b.Runs)
	if len(rs) == 0 {
		return nil
	}
	b.Runs = rs
	return []Block{*b}
}

var longDigits = regexp.MustCompile(`[0-9]{5,}`)

func AtText(target, name string) string {
	if target == "all" {
		return "@全体成员"
	}
	if target == "" {
		return "@某人"
	}
	if name != "" && name != target {
		if strings.Trim(name, "0123456789") == "" {
			return "@" + config.MaskID(name)
		}
		return "@" + longDigits.ReplaceAllStringFunc(name, config.MaskID)
	}
	if strings.Trim(target, "0123456789") != "" {
		return "@某人"
	}
	return "@" + config.MaskID(target)
}
func ImageNote(hasMedia bool, mediaState string) string {
	if hasMedia {
		return "[图片没下载下来]"
	}
	switch mediaState {
	case "too-big", "too_big":
		return "[图片太大，没有下载]"
	case "expired":
		return "[图片已过期]"
	case "", "nomedia":
		return "[尚无本地媒体]"
	case "forward_limit":
		return "[图片没有下载（这条聊天记录图太多）]"
	case "pending":
		return "[图片正在下载]"
	case "blocked":
		return "[图片地址不在允许的主机里]"
	case "not_image":
		return "[收到的文件不是图片]"
	}
	return "[图片没下载下来]"
}

func BlocksOf(segs []Segment) []Block {
	var out []Block
	var cur *Block
	budget := 20
	flush := func() {
		if cur != nil {
			out = append(out, TrimInline(cur)...)
			cur = nil
		}
	}
	add := func(r Run) {
		if cur == nil {
			cur = &Block{Kind: Inline}
		}
		r.Text = CleanText(r.Text)
		if n := len(cur.Runs); n > 0 && r.Face == nil && cur.Runs[n-1].Face == nil && cur.Runs[n-1].Bold == r.Bold && r.Link == nil && cur.Runs[n-1].Link == nil {
			cur.Runs[n-1].Text += r.Text
			return
		}
		cur.Runs = append(cur.Runs, r)
	}
	note := func(text string) { flush(); out = append(out, Block{Kind: Note, Text: text}) }
	for index, s := range segs {
		if s.Issue != "" {
			note("[这段的旧记录对不上，按新格式显示]")
		}
		switch s.Kind {
		case "text":
			for _, r := range LinkRuns(CleanText(s.Text), &budget) {
				add(r)
			}
		case "at":
			add(Run{Text: AtText(s.Target, s.Name), Bold: true})
		case "face", "superface":
			add(Run{Text: FaceFallback(s.Ref, s.Kind), Face: &FaceRef{ID: s.Ref, Kind: s.Kind}})
		case "reply":
		case "image", "emoji":
			if s.Kind == "emoji" && !s.HasMedia && s.Ref != "" {
				flush()
				out = append(out, Block{Kind: StickerNote, Text: "[" + s.DisplayName() + "]"})
				continue
			}
			if !s.HasHash {
				note(ImageNote(s.HasMedia, s.MediaState))
				continue
			}
			flush()
			if n := len(out); n > 0 && out[n-1].Kind == Images {
				out[n-1].Imgs = append(out[n-1].Imgs, s)
			} else {
				out = append(out, Block{Kind: Images, Imgs: []Segment{s}})
			}
		case "voice":
			note("[语音]")
		case "video":
			flush()
			out = append(out, Block{Kind: Video, Video: s, VideoIndex: index})
		case "file":
			flush()
			out = append(out, Block{Kind: File, Video: s, VideoIndex: index})
		case "forward":
			flush()
			out = append(out, Block{Kind: ForwardBlock, Card: s})
		case "card":
			if s.Card == nil {
				note("[卡片]")
			} else {
				flush()
				out = append(out, Block{Kind: CardBlock, Card: s})
			}
		default:
			if s.Kind == "" || s.Kind == "other" {
				note("[其他消息]")
			} else {
				note("[" + s.Kind + "]")
			}
		}
	}
	flush()
	return out
}
