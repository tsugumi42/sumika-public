package desktopread

// 媒体和小黄脸的只读入口（规格-界面接Go后台 第五节）。
// 媒体只认后台发出的 MediaRef：事件在这个会话里可见、它或它的合法引用确实带这个 hash，
// 再按可信平台走当前数据组的 mediastore.Reader；不接受路径、URL，不拿别的平台同 hash 补图。
// 小黄脸只读 <data>/cache/qq/faces 的既有资源，不启动 Bun、不扫描 QQ 安装目录。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/bunseg"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/history"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

const (
	// mediaMax 是单文件上限；多读 1 字节用来认出超限，不截断后当坏图。
	mediaMax = 20 << 20
	faceMax  = 4 << 20
)

var (
	ErrMediaRef      = errors.New("媒体凭据不属于这个会话里可见的事件")
	ErrMediaTooLarge = errors.New("媒体超过 20 MiB")
	ErrNoData        = errors.New("数据目录尚未接入")
	// ErrQQStoreClosed：QQ 库没打开（整组库已关或没登记 qq）。以前这里直接用 nil 库，会 panic（静默失败盘点 三-23）
	ErrQQStoreClosed = errors.New("QQ 库没打开")
	ErrFaceIndex     = errors.New("小黄脸索引不存在")
	ErrFaceMissing   = errors.New("没有这个小黄脸的图")
)

// WithData 接上数据根；调用方已拿数据组排他锁，须在 Service.Close 等完读取后才放锁。
func (s *Service) WithData(root string) *Service {
	s.data = root
	r, err := mediastore.NewReader(root)
	s.mediaReader = r
	s.openMedia = func(_ string, platform, hash string) (*os.File, error) {
		if err != nil {
			return nil, err
		}
		return r.Open(platform, hash)
	}
	return s
}

func validTier(t api.MediaTier) bool {
	switch t {
	case 0, 128, 256, 384, 512, 768, 1024, 1536:
		return true
	}
	return false
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func failed[T any](state api.State, err error) (api.Result[T], error) {
	return api.Result[T]{State: state, Err: err}, err
}

// ReadMedia 先验证可见凭据、打开原件，再按档读派生缩略图；缓存命中也不能绕过授权或源文件路径检查。
func (s *Service) ReadMedia(ctx context.Context, ref api.MediaRef, tier api.MediaTier) (api.Result[api.MediaView], error) {
	if !validTier(tier) || !validHash(ref.Hash) {
		return failed[api.MediaView](api.Failed, store.ErrDisplayParameter)
	}
	if s.data == "" {
		return failed[api.MediaView](api.Unsupported, ErrNoData)
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	row, err := s.mediaRow(ctx, ref, false)
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	limit := int64(mediaMax)
	// 文件段沿用 ADR-261 的限额；普通图片仍是 20 MiB。
	if len(ref.Forward) > 0 {
		st, ok := s.stores.Platform("qq")
		if !ok {
			return failed[api.MediaView](api.Failed, ErrQQStoreClosed)
		}
		ms, err := st.ForwardMediaRows(ctx, ref.Forward[len(ref.Forward)-1])
		if err != nil {
			return failed[api.MediaView](api.Failed, err)
		}
		for _, m := range ms {
			if m.Kind == types.SegFile && m.Hash == ref.Hash && m.Format == "image" {
				limit = 100 << 20
				break
			}
		}
	} else {
		for _, r := range []*store.DisplayRow{&row, row.Reply} {
			if r == nil {
				continue
			}
			for _, m := range r.MediaMeta {
				if m.Kind == types.SegFile && m.Hash == ref.Hash && m.Format == "image" {
					limit = 100 << 20
					break
				}
			}
		}
	}
	f, err := s.openMedia(s.data, ref.Source.Conversation.Platform, ref.Hash)
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	defer f.Close()
	tooLarge := func() (api.Result[api.MediaView], error) {
		if limit == mediaMax {
			return failed[api.MediaView](api.Failed, ErrMediaTooLarge)
		}
		return failed[api.MediaView](api.Failed, errors.New("文件图片超过 100 MiB"))
	}
	info, err := f.Stat()
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	if info.Size() > limit {
		return tooLarge()
	}
	b, err := s.mediaReader.Thumbnail(ctx, f, ref.Source.Conversation.Platform, ref.Hash, int(tier))
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	if b == nil {
		b, err = io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return failed[api.MediaView](api.Failed, err)
		}
		if int64(len(b)) > limit {
			return tooLarge()
		}
	}
	if err := ctx.Err(); err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: http.DetectContentType(b), Size: int64(len(b))}}, nil
}

func rowHasHash(r store.DisplayRow, hash string) bool {
	for idx, seg := range parseEnvelope(r.Segments).segs {
		if seg.media == hash && (seg.seg.Kind != types.SegFile || r.MediaMeta[idx].Format == "image") {
			return true
		}
		if m, ok := r.MediaMeta[idx]; ok && (seg.seg.Kind == types.SegVideo || seg.seg.Kind == types.SegFile && m.Format == "video") && seg.media != "" && m.State == store.MediaDone && m.Hash == seg.media && m.CoverHash == hash {
			return true
		}
	}
	return false
}

// facesDir 下的每一级都不许是链接或联接，和媒体读取同一个口径。
func (s *Service) facePath(name string) (string, error) {
	cur := s.data
	for _, part := range []string{"cache", "qq", "faces", name} {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", mediastore.ErrMediaLink
		}
	}
	return cur, nil
}

func (s *Service) readFaceFile(name string) ([]byte, error) {
	p, err := s.facePath(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, faceMax+1))
	if err == nil && len(b) > faceMax {
		err = ErrMediaTooLarge
	}
	return b, err
}

// ReadFaceIndex 读 Bun 留下的 index.json（{version,faces}）；缺文件报明确的缺资源状态，不回落演示表。
func (s *Service) ReadFaceIndex(ctx context.Context) (api.Result[api.List[api.FaceView]], error) {
	if s.data == "" {
		return failed[api.List[api.FaceView]](api.Unsupported, ErrNoData)
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.List[api.FaceView]](api.Failed, err)
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return failed[api.List[api.FaceView]](api.Failed, err)
	}
	b, err := s.readFaceFile("index.json")
	if errors.Is(err, os.ErrNotExist) {
		return failed[api.List[api.FaceView]](api.Failed, ErrFaceIndex)
	}
	if err != nil {
		return failed[api.List[api.FaceView]](api.Failed, err)
	}
	var index struct {
		Faces map[string]struct{ Name, Kind, Char string } `json:"faces"`
	}
	if err := json.Unmarshal(b, &index); err != nil {
		return failed[api.List[api.FaceView]](api.Failed, err)
	}
	var out api.List[api.FaceView]
	for id, f := range index.Faces {
		if numericFace(id) {
			out.Items = append(out.Items, api.FaceView{ID: id, Name: f.Name, Kind: f.Kind, Char: f.Char})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, _ := strconv.Atoi(out.Items[i].ID)
		b, _ := strconv.Atoi(out.Items[j].ID)
		return a < b
	})
	return api.Result[api.List[api.FaceView]]{State: api.Ready, Value: out}, nil
}

func numericFace(id string) bool {
	if len(id) == 0 || len(id) > 6 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ReadFacePNG 按 1～6 位数字编号取 <id>.png，不拼别的路径。
func (s *Service) ReadFacePNG(ctx context.Context, id string) (api.Result[api.MediaView], error) {
	if !numericFace(id) {
		return failed[api.MediaView](api.Failed, store.ErrDisplayParameter)
	}
	if s.data == "" {
		return failed[api.MediaView](api.Unsupported, ErrNoData)
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	b, err := s.readFaceFile(id + ".png")
	if errors.Is(err, os.ErrNotExist) {
		return failed[api.MediaView](api.Failed, ErrFaceMissing)
	}
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: http.DetectContentType(b), Size: int64(len(b))}}, nil
}

// envelope 是 segments_json 的中立结构；导入的旧记录另带 import_meta（导入规格第五节）。
type envelope struct {
	Segs  []types.Seg `json:"segs"`
	Parts []struct {
		Kind  string              `json:"kind"`
		Body  []types.BodySegment `json:"body"`
		Quote string              `json:"quote"`
		Nodes []types.ForwardNode `json:"nodes"`
	} `json:"parts"`
	Unsent, Partial, Aborted bool
	LocalParts               []history.Part `json:"local_parts"` // 主人分块输入的存档（规格-引用）
	// Proactive 是主动联系的线往目标会话写她的话时宿主放的标记（规格-主动联系 第十节第 1 条）
	Proactive *struct {
		Thread string `json:"thread"`
		Reason string `json:"reason"`
	} `json:"proactive"`
	ImportMeta *struct {
		Version int              `json:"version"`
		Legacy  []map[string]any `json:"legacy_segments"`
	} `json:"import_meta"`
}

type parsedSeg struct {
	seg      types.Seg
	media    string // image / emoji / video 的有效 hash（中立段的，或对上号的旧段补的）
	legacy   map[string]any
	issue    string
	outbound *types.MediaResource
	// semantic 是出站段在绘制或下载前已检查的文字（卡片、图片的语义说明）；资源释放后界面靠它显示内容。
	semantic string
}

type parsed struct {
	env  envelope
	segs []parsedSeg
	ok   bool
}

func parseEnvelope(raw string) parsed {
	var p parsed
	if raw == "" || json.Unmarshal([]byte(raw), &p.env) != nil {
		return p
	}
	p.ok = true
	return parseSegments(p)
}

func parseSegments(p parsed) parsed {
	for i, seg := range p.env.Segs {
		ps := parsedSeg{seg: seg}
		if seg.Kind == types.SegCard && seg.CardTitle == "" && seg.CardJSON != "" && seg.CardIssue == "" {
			types.FillCardDisplay(&seg)
			ps.seg = seg
		}
		if meta := p.env.ImportMeta; meta != nil {
			old, err := bunseg.LegacySegment(meta.Version, meta.Legacy, i, seg)
			if err != nil {
				ps.issue = err.Error()
			} else {
				ps.legacy = old
			}
		}
		if seg.Kind == types.SegImage || seg.Kind == types.SegEmoji || seg.Kind == types.SegCard || seg.Kind == types.SegVideo || seg.Kind == types.SegFile {
			h := seg.Hash
			if h == "" && ps.legacy != nil {
				h, _ = ps.legacy["hash"].(string)
			}
			if validHash(h) {
				ps.media = h
			}
		}
		p.segs = append(p.segs, ps)
	}
	appendBody := func(body []types.BodySegment) {
		for _, s := range body {
			seg := types.Seg{Kind: s.Kind, Text: s.Text}
			if s.Kind == types.BodyGIF {
				seg.Kind = types.SegEmoji
			}
			// 临时资源已释放，句柄不能当永久收件库哈希。
			p.segs = append(p.segs, parsedSeg{seg: seg, outbound: s.Media, semantic: s.Semantic})
		}
	}
	for _, part := range p.env.Parts {
		if part.Quote != "" {
			p.segs = append(p.segs, parsedSeg{seg: types.Seg{Kind: types.SegReply, Target: part.Quote}})
		}
		if part.Kind == types.PartForward {
			for _, node := range part.Nodes {
				p.segs = append(p.segs, parsedSeg{seg: types.Seg{Kind: types.SegText, Text: node.Name + "\n"}})
				appendBody(node.Body)
			}
		} else {
			appendBody(part.Body)
		}
	}
	return p
}

func legacyInt(m map[string]any, k string) int {
	if v, ok := m[k].(float64); ok && v > 0 && v < 1<<20 {
		return int(v)
	}
	return 0
}

// legacySize 是旧段里的字节数：非负整数且不超过 2^53 才认，别的都当不知道
func legacySize(m map[string]any, k string) int64 {
	if v, ok := m[k].(float64); ok && v > 0 && v <= 1<<53 && v == math.Trunc(v) {
		return int64(v)
	}
	return 0
}

func legacyBool(m map[string]any, k string) bool {
	switch v := m[k].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	}
	return false
}

func legacyString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// displaySegments 一次把中立段整理成界面要的样子；旧段只补尺寸、动图、名字这类展示元数据。
func displaySegments(source api.EventRef, p parsed, atNames map[string]string, mediaMeta map[int]store.MediaFetch) []api.DisplaySegment {
	out := make([]api.DisplaySegment, 0, len(p.segs))
	for idx, ps := range p.segs {
		seg := ps.seg
		d := api.DisplaySegment{Kind: seg.Kind, Text: seg.Text, Target: seg.Target, Ref: seg.Ref, Issue: ps.issue}
		if ps.semantic != "" && seg.Kind != types.SegText {
			// 链接卡片等的语义文字：媒体释放后只剩占位时，界面用它当说明（chatmedia 的 Description 取自 Summary）。
			d.Summary = ps.semantic
		}
		if m := ps.outbound; m != nil {
			d.Width, d.Height, d.Size = m.Width, m.Height, m.Size
			d.Animated = m.MIME == "image/gif"
			d.MediaState = "outbound-released"
		} else if seg.Kind == types.SegVideo || seg.Kind == types.SegFile {
			d.Download = &api.VideoDownloadRef{Source: source, Index: idx}
		}
		switch seg.Kind {
		case types.SegImage, types.SegEmoji:
			if ps.media != "" {
				d.Media = &api.MediaRef{Source: source, Hash: ps.media}
			}
			d.Sticker = seg.Kind == types.SegEmoji
			d.Name = seg.File
		case types.SegVideo:
			if ps.outbound == nil {
				d.Size = seg.Size
			}
			if ps.media != "" {
				d.Media = &api.MediaRef{Source: source, Hash: ps.media}
			}
			if m, ok := mediaMeta[idx]; ok && m.Kind == types.SegVideo {
				d.FetchState = m.State
				if m.Size > 0 {
					d.Size = m.Size
				}
				if m.State == store.MediaDone && ps.media != "" && m.Hash == ps.media {
					d.Width, d.Height, d.DurationMS = m.Width, m.Height, m.DurationMS
					if validHash(m.CoverHash) {
						d.Cover = &api.MediaRef{Source: source, Hash: m.CoverHash}
					}
				}
			}
		case types.SegAt:
			d.Name = atNames[seg.Target] // 正文和引用一起批量查名；缺名由界面统一打码。
		case types.SegCard:
			d.Summary = seg.Raw
			if seg.CardIssue == "" && seg.CardJSON != "" {
				d.Links = linkreader.CardURLs(seg.CardJSON)
			}
			if seg.CardTitle != "" {
				d.Card = &api.CardView{Title: seg.CardTitle, Description: seg.CardDescription, Source: seg.CardSource, URL: seg.CardURL}
			}
			if ps.media != "" {
				d.Media = &api.MediaRef{Source: source, Hash: ps.media}
			}
		case types.SegOther:
			d.Summary = seg.Raw
		case types.SegFile:
			// 文件名是外来文字：显示前照群名、昵称那样清一遍（去换行、零宽和方向控制字符，超过 40 个字截断）；清完是空的就没有名字
			d.Name = store.CleanDisplayName(seg.File)
			if m, ok := mediaMeta[idx]; ok && m.Kind == types.SegFile {
				d.FetchState = m.State
				if m.Size > 0 {
					d.Size = m.Size
				}
				if m.State == store.MediaDone && m.Hash == ps.media {
					d.FileKind = m.Format
					if m.Format == "image" || m.Format == "video" {
						d.Media = &api.MediaRef{Source: source, Hash: ps.media}
					}
					d.DurationMS = m.DurationMS
					if validHash(m.CoverHash) {
						d.Cover = &api.MediaRef{Source: source, Hash: m.CoverHash}
					}
				}
			}
			if seg.Size > 0 && d.Size == 0 {
				d.Size = seg.Size
			}
		}
		if m, ok := mediaMeta[idx]; ok && ps.media != "" && m.Hash == ps.media {
			d.Width, d.Height, d.Animated = m.Width, m.Height, m.Animated
		}
		if m := ps.legacy; m != nil {
			if w, h := legacyInt(m, "width"), legacyInt(m, "height"); w > 0 && h > 0 {
				d.Width, d.Height = w, h
			}
			if _, has := m["animated"]; has {
				d.Animated = legacyBool(m, "animated")
			}
			d.Sticker = d.Sticker || legacyBool(m, "sticker") || legacyBool(m, "market")
			if v := legacyString(m, "summary"); v != "" {
				d.Summary = v
			}
			if v := legacyString(m, "name"); v != "" {
				if seg.Kind == types.SegFile {
					v = store.CleanDisplayName(v)
				}
				if v != "" {
					d.Name = v
				}
			}
			if seg.Kind == types.SegFile && d.Size == 0 {
				d.Size = legacySize(m, "size")
			}
		}
		out = append(out, d)
	}
	return out
}
