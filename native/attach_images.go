package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/mediastore"
)

// 本地聊天里的图片附件（规格-引用 第十节）：输入框缩略图、待发气泡、存好后的气泡、看大图，全走同一份图片缓存（imageStore）。
// 附件只有本地路径，没有后台的媒体凭据：用一个"本地文件"凭据，平台写 attachFilePlatform、哈希位写路径，
// 缓存的键、档位、淘汰、失败重试、看大图都照消息图片的老路走；只有"怎么取字节"不同（imageStore.fetch 里按平台分流）。

// attachFilePlatform 是本地文件凭据的平台名；真实平台只有 local、qq 等，不会撞。
const attachFilePlatform = "attach-file"

// attachThumbTier 是输入框缩略图解到多宽（像素）：72 逻辑像素 × 1.25 缩放 = 90，窄长的图铺满方块后长边约 4 倍，取 384 够清楚又不大。
const attachThumbTier = 384

// localFileRef 是一个本地路径的图片凭据。
// stamp 是挂上这张图时文件的修改时间和大小（fileStamp）：拼进哈希位，文件被改写后缓存认成另一张图，不再显示旧位图；空表示不带版本。
func localFileRef(path, stamp string) api.MediaRef {
	hash := path
	if stamp != "" {
		hash = path + stampSep + stamp
	}
	return api.MediaRef{Source: api.EventRef{Event: api.EventKey{Platform: attachFilePlatform}}, Hash: hash}
}

// stampSep 隔开哈希位里的路径和版本；路径里不会有 NUL。
const stampSep = "\x00"

// fileStamp 是文件当前的版本串：修改时间（纳秒）加大小。
func fileStamp(info os.FileInfo) string {
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
}

// localFileOf 取出凭据里的路径；不是本地文件凭据返回 false。
func localFileOf(ref api.MediaRef) (string, bool) {
	if ref.Source.Event.Platform != attachFilePlatform || ref.Hash == "" {
		return "", false
	}
	path, _, _ := strings.Cut(ref.Hash, stampSep)
	return path, true
}

// withLocalFiles 给取图函数加上"本地文件凭据直接读文件"这一路，其余照旧。
func withLocalFiles(remote func(context.Context, api.MediaRef, int) ([]byte, error)) func(context.Context, api.MediaRef, int) ([]byte, error) {
	return func(ctx context.Context, ref api.MediaRef, tier int) ([]byte, error) {
		if path, ok := localFileOf(ref); ok {
			return readLocalImage(ctx, path)
		}
		return remote(ctx, ref, tier)
	}
}

// readLocalImage 在后台线程读一张本地图的字节：超过后台收图上限的当读不到（和发送时的限制一致）。
func readLocalImage(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() || st.Size() > maxImageBytes {
		return nil, fmt.Errorf("图片不可读：%s", filepath.Base(path))
	}
	buf := make([]byte, st.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// imageDimsOf 量图片字节的像素宽高（已按 EXIF 转正）；量不出来返回 0，0。
func imageDimsOf(data []byte) (w, h int) {
	m := mediastore.ReadImageMeta(bytes.NewReader(data), int64(len(data)))
	return m.Width, m.Height
}

// attachSeg 把一张附件图做成消息的展示段：位置是它在这条消息 segs 里的序号，宽高来自存档（或输入框量的）。
func attachSeg(name, path, stamp string, media *api.MediaRef, w, h, position int) seg {
	ref := localFileRef(path, stamp)
	if media != nil {
		ref = *media // 媒体库里的图：照消息图片的老路取
	}
	s := seg{DisplaySegment: api.DisplaySegment{Kind: "image", Name: name, Media: &ref, Width: w, Height: h}, position: position}
	return s
}

// partImage 是一块上某个附件的取图来源：路径或媒体凭据，加上像素宽高。
type partImage struct {
	Path, Stamp   string
	Media         *api.MediaRef
	Width, Height int
}

// storedPartImages 是存好的消息每块、每个附件的取图来源（顺序同 chatParts 的 Atts）。
func storedPartImages(v api.MessageView) [][]partImage {
	out := make([][]partImage, len(v.Parts))
	for i, p := range v.Parts {
		for _, x := range p.Attachments {
			out[i] = append(out[i], partImage{Path: x.Path, Media: x.Media, Width: x.Width, Height: x.Height})
		}
	}
	return out
}

// pendingPartImages 是待发气泡每块、每个附件的取图来源（顺序同 pendingParts 的 Atts：折叠的粘贴文字不算附件）。
func pendingPartImages(blocks []*chatui.Block) [][]partImage {
	out := make([][]partImage, len(blocks))
	for i, b := range blocks {
		for _, x := range b.Atts {
			if x.Kind != chatui.AttText {
				pi := partImage{Path: x.Path, Stamp: x.Stamp, Width: x.Width, Height: x.Height}
				if pi.Width == 0 || pi.Height == 0 {
					pi.Width, pi.Height = localImageDims(x.Path) // 缩略图还没量完就发：提交时读一次文件头，待发气泡一开始就是真比例
				}
				out[i] = append(out[i], pi)
			}
		}
	}
	return out
}

// localImageDims 只读文件头量一张本地图的宽高（已按 EXIF 转正）；读不到返回 0，0。
func localImageDims(path string) (w, h int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return 0, 0
	}
	m := mediastore.ReadImageMeta(f, st.Size())
	return m.Width, m.Height
}

// bindPartImages 给分块消息里的图片附件配上展示段：图从图片缓存取（Host.Media.Image 按序号认），宽高让气泡在图加载前就占好位置，
// 点开大图时同一条消息的图可以左右切换。parts[i].Atts[j] 对应 srcs[i][j]；没有路径也没有凭据的（读不到来源）仍是文件标签。
func (m *msg) bindPartImages(parts []chatui.Part, srcs [][]partImage) {
	for i := range parts {
		if i >= len(srcs) {
			break
		}
		for j := range parts[i].Atts {
			at := &parts[i].Atts[j]
			if !at.Image || j >= len(srcs[i]) {
				continue
			}
			src := srcs[i][j]
			if src.Path == "" && src.Media == nil {
				continue
			}
			pos := len(m.segs)
			m.segs = append(m.segs, attachSeg(at.Name, src.Path, src.Stamp, src.Media, src.Width, src.Height, pos))
			at.Show, at.Pos, at.Width, at.Height = true, pos, src.Width, src.Height
		}
	}
}

// attachName 是附件的显示名：没给名字就用路径的文件名。
func attachName(name, path string) string {
	if name == "" {
		name = filepath.Base(path)
	}
	return name
}

// openComposerImage 点输入框里的缩略图：用看大图打开，输入区里所有已读好的图可以左右切换。
func (a *app) openComposerImage(p *chatui.Composer, id uint64) {
	var segs []seg
	at := 0
	for _, x := range p.ImageAtts() {
		if x.ID == id {
			at = len(segs)
		}
		w, h := x.Width, x.Height
		segs = append(segs, attachSeg(attachName(x.Name, x.Path), x.Path, x.Stamp, nil, w, h, len(segs)))
	}
	if len(segs) == 0 {
		return
	}
	st := a.is()
	a.chatLightbox = chatLightbox{msgID: "composer", segments: segs, at: at, open: true, images: st, gen: a.gen, srcGen: a.srcGen}
}
