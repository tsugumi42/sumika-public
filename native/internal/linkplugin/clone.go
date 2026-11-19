package linkplugin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
)

// 出口发完一个计划就释放计划里的媒体，所以换形式重发要用事先留好的备份：静态图、动图的字节拷一份留在内存里
// （图片有上限，见 linkexpand.imageLimit）；视频是文件、可能上百 MB，不备份，被拒后只能退文字。

// cloneLimit 是备份单个媒体的上限；比它大的不备份（那一段被拒后只能退文字，写明原因）
const cloneLimit = 32 << 20

// errNoBackup：这段的媒体没有备份（视频、超大、已释放）
var errNoBackup = errors.New("媒体没有备份")

func cloneMedia(m *types.MediaResource) *types.MediaResource {
	if m == nil || !m.Available() || m.Size > cloneLimit {
		return nil
	}
	var raw []byte
	if err := m.Read(func(r io.Reader) error {
		var e error
		raw, e = io.ReadAll(io.LimitReader(r, cloneLimit+1))
		return e
	}); err != nil || len(raw) == 0 || len(raw) > cloneLimit {
		return nil
	}
	return types.MemoryMedia(rand.Text(), m.MIME, m.Width, m.Height, raw)
}

// backupBody 拷一份正文：图和动图的媒体拷成独立的内存资源，视频不拷（Media 置空，表示没有备份）
func backupBody(body []types.BodySegment) []types.BodySegment {
	if body == nil {
		return nil
	}
	out := make([]types.BodySegment, len(body))
	for i, s := range body {
		out[i] = s
		switch s.Kind {
		case types.BodyImage, types.BodyGIF:
			out[i].Media = cloneMedia(s.Media)
		case types.BodyText:
		case types.BodyVideo:
			// 视频不备份：留着原资源只为认出是哪一个（合并转发放不了视频时按它找留下的硬链接），发完就释放了
		default:
			out[i].Media = nil
		}
	}
	return out
}

// backupParts 在发出去之前给每一段留一份备份。没有媒体的段几乎不花东西
func backupParts(parts []types.Part) []types.Part {
	out := make([]types.Part, len(parts))
	for i, p := range parts {
		out[i] = p
		out[i].Body = backupBody(p.Body)
		if p.Nodes != nil {
			out[i].Nodes = make([]types.ForwardNode, len(p.Nodes))
			for j, n := range p.Nodes {
				n.Body = backupBody(n.Body) // 节点的别的字段（显示名、引用的节点）原样带上
				out[i].Nodes[j] = n
			}
		}
	}
	return out
}

// clonePart 从备份造一份能发的段：媒体再拷一份新资源（出口发完会释放它），返回这些资源交给出口。
// 有媒体段没备份就返回 errNoBackup。
func clonePart(p types.Part) (types.Part, []*types.MediaResource, error) {
	var res []*types.MediaResource
	var failed bool
	body := func(in []types.BodySegment) []types.BodySegment {
		if in == nil {
			return nil
		}
		out := make([]types.BodySegment, len(in))
		for i, s := range in {
			out[i] = s
			if s.Kind == types.BodyText {
				continue
			}
			if s.Kind == types.BodyVideo {
				failed = true // 视频没有备份（备份里的原资源可能还没释放，也不能拷进内存发）
				continue
			}
			c := cloneMedia(s.Media)
			if c == nil {
				failed = true
				continue
			}
			out[i].Media = c
			res = append(res, c)
		}
		return out
	}
	cp := p
	cp.Body = body(p.Body)
	if p.Nodes != nil {
		cp.Nodes = make([]types.ForwardNode, len(p.Nodes))
		for i, n := range p.Nodes {
			n.Body = body(n.Body)
			cp.Nodes[i] = n
		}
	}
	if failed {
		return types.Part{}, nil, errNoBackup
	}
	return cp, res, nil
}

// mediaSegs 列一段里的媒体段（正文和各节点），按出现顺序
func mediaSegs(p types.Part) []types.BodySegment {
	var out []types.BodySegment
	for _, s := range p.Body {
		if s.Kind != types.BodyText {
			out = append(out, s)
		}
	}
	for _, n := range p.Nodes {
		for _, s := range n.Body {
			if s.Kind != types.BodyText {
				out = append(out, s)
			}
		}
	}
	return out
}

// partKey 是这一段里所有媒体内容的哈希（不含文字）：同一张图再来，认得出它。没有媒体或没有备份返回空
func partKey(p types.Part) string {
	h := sha256.New()
	n := 0
	for _, s := range mediaSegs(p) {
		if s.Media == nil || s.Kind == types.BodyVideo {
			return "" // 视频不转图，也不按图记结论
		}
		if err := s.Media.Read(func(r io.Reader) error { _, e := io.Copy(h, r); return e }); err != nil {
			return ""
		}
		n++
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// ---- 按图记的结论 ----

// 换形式的阶梯（按顺序试）
type form int

const (
	formDirect  form = iota // 正常发：消息里直接带图
	formForward             // 原本就是合并转发的段，原样发（阶梯里只作为这类段的第一级）
	formTurned              // 合并转发，里面的静态图转 180°
	formRotated             // 直接发，静态图转 180°
	formCount
)

func (f form) String() string {
	switch f {
	case formForward:
		return "合并转发"
	case formTurned:
		return "转 180° 的合并转发"
	case formRotated:
		return "转 180° 直接发"
	}
	return "直接发"
}

// rank 是形式在阶梯上的高低：0 原样、1 直接发转图、2 合并转发转图。imageMode 从哪一级开始按它比。
func (f form) rank() int {
	switch f {
	case formRotated:
		return 1
	case formTurned:
		return 2
	}
	return 0
}

const (
	verdictTTL = 6 * time.Hour
	verdictMax = 512
)

// verdictBook 记“这张图在这个场合哪种形式被协议端明确拒收过、哪种发成过”。只在内存里，到期自动忘；
// 键是场合加图片内容的哈希：一张图被拒，只影响这张图，不影响整个群的图片（能力缺失才按场合降级，见 app/media_block.go）。
// 用途：同一张图再来时跳过已知被拒的形式、先试发成过的那种，少往 QQ 提交注定被拒的内容。
type verdictBook struct {
	mu sync.Mutex
	m  map[string]*verdict
}

type verdict struct {
	bad   [formCount]bool
	good  int // -1 没有
	until time.Time
}

func (b *verdictBook) empty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m) == 0
}

func (b *verdictBook) get(key string, now time.Time) (bad [formCount]bool, good int) {
	if key == "" {
		return bad, -1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[key]
	if !ok || !now.Before(v.until) {
		delete(b.m, key)
		return bad, -1
	}
	return v.bad, v.good
}

// mark 记一次结论：ok 为真是这种形式发成了，否则是被明确拒收
func (b *verdictBook) mark(key string, f form, ok bool, now time.Time) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*verdict{}
	}
	if _, exists := b.m[key]; !exists && len(b.m) >= verdictMax {
		var oldest string
		for k, v := range b.m {
			if oldest == "" || v.until.Before(b.m[oldest].until) {
				oldest = k
			}
		}
		delete(b.m, oldest)
	}
	v := b.m[key]
	if v == nil || !now.Before(v.until) {
		v = &verdict{good: -1}
		b.m[key] = v
	}
	v.until = now.Add(verdictTTL)
	if ok {
		v.good = int(f)
		v.bad[f] = false
	} else {
		v.bad[f] = true
		if v.good == int(f) {
			v.good = -1
		}
	}
}
