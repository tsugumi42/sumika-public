package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/chatui"
)

// 新界面输入区的附件：拖入或粘贴的文件和文件夹只记路径；粘贴的图片先存进媒体库再挂上。
// 附件挂在哪一块由 Composer 定：粘贴挂在粘贴所在的块，拖入挂在最近有焦点的块（没有就是主输入框）。

// 一张图的上限和后台收图的上限（app.MaxLocalImage，每张 20 MiB）一致；超了在界面上说，不静默丢。
const maxImageBytes = backend.MaxLocalImage

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}

// attachPaths 把拖入或粘贴的路径挂到 to 这一块（nil 是最近有焦点的那块）。路径不存在的不挂，在输入区上方说一句。
func (a *app) attachPaths(p *chatui.Composer, to *chatui.Block, paths []string) {
	var missing, tooBig []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			missing = append(missing, path)
			continue
		}
		att := chatui.Att{Kind: chatui.AttFile, Name: filepath.Base(path), Path: path}
		if att.Name == "." || att.Name == string(filepath.Separator) {
			att.Name = path
		}
		switch {
		case info.IsDir():
			att.Kind = chatui.AttFolder
		case imageExt[strings.ToLower(filepath.Ext(path))]:
			if info.Size() > maxImageBytes {
				tooBig = append(tooBig, att.Name)
				continue
			}
			att.Kind = chatui.AttImage
			att.Stamp = fileStamp(info)
		}
		id, ok := p.Attach(to, att)
		if !ok {
			return
		}
		if att.Kind == chatui.AttImage {
			a.decodeThumb(p, id, path)
		}
	}
	var notes []string
	switch len(missing) {
	case 0:
	case 1:
		notes = append(notes, "找不到："+missing[0])
	default:
		notes = append(notes, fmt.Sprintf("有 %d 个路径找不到，如：%s", len(missing), missing[0]))
	}
	switch len(tooBig) {
	case 0:
	case 1:
		notes = append(notes, tooBig[0]+" 超过 20 MiB，没有加入")
	default:
		notes = append(notes, fmt.Sprintf("有 %d 张图超过 20 MiB 没有加入，如：%s", len(tooBig), tooBig[0]))
	}
	if len(notes) > 0 {
		p.Notice = strings.Join(notes, "；")
	}
}

// decodeThumb 在后台读图、量宽高、解一张小缩略图（不是整张原图：一张 4800 万像素的照片整张解开要 190 MB），
// 解出来替换附件上的图标；读不到、解不开就保持图标，附件中途被删了就丢掉。
func (a *app) decodeThumb(p *chatui.Composer, id uint64, path string) {
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		data, err := readLocalImage(ctx, path)
		if err != nil {
			return
		}
		w, h := imageDimsOf(data)
		bmp, err := decodeStatic(data, attachThumbTier)
		if err != nil {
			return
		}
		a.post(func() {
			if a.srcGen == gen && ctx.Err() == nil {
				p.Update(id, func(x *chatui.Att) { x.Thumb, x.Width, x.Height = bmp, w, h })
			}
		})
	}()
}

// pasteImage 把粘贴的图片存进媒体库：先挂一个"保存中"的缩略图方块，存好换成真路径；存失败或被删就撤掉。
func (a *app) pasteImage(k api.ConversationKey, p *chatui.Composer, to *chatui.Block, png []byte) {
	if len(png) > maxImageBytes {
		p.Notice = "粘贴的图片超过 20 MiB，没有加入"
		return
	}
	saver, ok := a.reader.(api.LocalImageSaver)
	if !ok {
		p.Notice = "后台不支持存图片"
		return
	}
	id, ok := p.Attach(to, chatui.Att{Kind: chatui.AttImage, Name: "粘贴的图片.png", Pending: true})
	if !ok {
		return
	}
	gen, ctx := a.srcGen, a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		att, err := saver.SaveLocalImage(ctx, k, png)
		var thumb ui.ImageSource
		var w, h int
		if err == nil {
			w, h = imageDimsOf(png)
			if bmp, derr := decodeStatic(png, attachThumbTier); derr == nil {
				thumb = bmp
			}
		}
		a.post(func() {
			if a.srcGen != gen || ctx.Err() != nil {
				return
			}
			if err != nil {
				if p.Detach(id) {
					p.Notice = "图片没存下来：" + err.Error()
				}
				return
			}
			p.Update(id, func(x *chatui.Att) {
				x.Pending, x.Path, x.Name, x.Thumb, x.Width, x.Height = false, att.Path, att.Name, thumb, w, h
			})
		})
	}()
}

// localAttachments 把一块的附件转成后台的附件：文件夹和文件一样记路径；折叠的粘贴文字不算。
func localAttachments(atts []chatui.Att) []api.LocalAttachment {
	var out []api.LocalAttachment
	for _, x := range atts {
		if x.Kind == chatui.AttText {
			continue // 折叠的粘贴已经拼进这一块的正文（Block.SendText），不是后台的附件
		}
		kind := "file"
		if x.Kind == chatui.AttImage {
			kind = "image"
		}
		out = append(out, api.LocalAttachment{Kind: kind, Path: x.Path, Name: x.Name})
	}
	return out
}

// dropDataOptions 是聊天列接的数据拖放：链接（URI 列表）和文字。文件走 DroppedFiles 的老路。
var dropDataOptions = transfer.DropOptions{Formats: []transfer.Format{transfer.URIList, transfer.Text}}

// dropContent 把一次数据拖放拆成要挂的文件路径和要放进输入框的文字。
// 带本地文件（file:// 链接）的当文件挂；否则拖来的网页链接作为文字放进输入框，没有链接时用拖来的纯文字。
// 链接不做成附件：后台的附件只有图片和文件两种，链接要靠文字让她看到、让链接插件展开，
// 和粘贴链接是同一个结果（粘贴不拦链接，见 TestComposerPasteTextAndLinksStayPlainText）。
func dropContent(d transfer.Data) (paths []string, text string) {
	if files, _ := d.Files(); len(files) > 0 {
		return files, ""
	}
	if urls, _ := d.URLs(); len(urls) > 0 {
		var links []string
		for _, raw := range urls {
			if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
				links = append(links, raw)
			}
		}
		if len(links) > 0 {
			return nil, strings.Join(links, "\n")
		}
	}
	if b, err := d.Read(transfer.Text); err == nil {
		return nil, strings.TrimSpace(string(b))
	}
	return nil, ""
}
