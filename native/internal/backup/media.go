package backup

import (
	"database/sql"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 媒体（规格-数据目录与备份 10.1、10.3）：按 backup.dailyMedia 挑；和上一份校验通过的备份里同名同大小的文件建硬链接，建不了就复制；
// 永远不和数据目录本身建硬链接（那样数据那份坏了，备份跟着坏）。

func (b *builder) media(items []item, media string, stickers []StickerSource) {
	if media == MediaNone {
		return
	}
	var mediaItems []item
	for _, it := range items {
		if it.kind == kindMedia {
			mediaItems = append(mediaItems, it)
		}
	}
	if len(mediaItems) == 0 {
		return
	}
	pick := mediaItems
	if media == MediaStickers {
		pick = b.pickStickers(mediaItems, stickers)
	}
	if len(pick) == 0 {
		return
	}
	prev := b.prevBackup()
	for _, it := range pick {
		if b.failed() {
			return
		}
		b.mediaOne(it, prev)
	}
}

// pickStickers：对每个登记了来源的媒体目录，从聊天库副本里取哈希，挑出文件名在集合里的
func (b *builder) pickStickers(all []item, sources []StickerSource) []item {
	var out []item
	covered := map[string]bool{}
	for _, src := range sources {
		if b.failed() {
			return out
		}
		hasMedia := false
		for _, it := range all {
			if underDir(it.rel, src.MediaRel) {
				hasMedia = true
				break
			}
		}
		if !hasMedia {
			continue // 这个媒体目录里没有文件，用不着挑
		}
		covered[lower(src.MediaRel)] = true
		if _, err := os.Stat(b.dst(src.DBRel)); os.IsNotExist(err) {
			// 库没有副本：库本来就不在数据目录里（旧版库没有、平台没开），或者取快照已经失败（那里会记 problem）。这里只记一笔，不重复算失败
			b.notes = append(b.notes, "媒体目录 "+src.MediaRel+" 对应的库 "+src.DBRel+" 没有副本，stickers 档不带这个目录的媒体")
			continue
		}
		set, err := b.hashesFromCopy(src)
		if err != nil {
			b.problem("挑表情包：从 %s 的副本取哈希失败：%v", src.DBRel, err)
			continue
		}
		for _, it := range all {
			if !underDir(it.rel, src.MediaRel) {
				continue
			}
			if set[strings.ToLower(path.Base(it.rel))] {
				out = append(out, it)
			}
		}
	}
	for _, it := range all {
		hit := false
		for dir := range covered {
			if underDir(it.rel, dir) {
				hit = true
				break
			}
		}
		if !hit {
			b.notes = append(b.notes, "媒体目录 "+path.Dir(it.rel)+" 没有登记表情包来源，stickers 档不带它")
			break
		}
	}
	return out
}

func (b *builder) hashesFromCopy(src StickerSource) (map[string]bool, error) {
	file := b.dst(src.DBRel)
	if _, err := os.Stat(file); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	db, err := sql.Open("sqlite", "file:"+strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	hs, err := src.Hashes(b.ctx, db)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(hs))
	for _, h := range hs {
		if h != "" {
			set[strings.ToLower(h)] = true
		}
	}
	return set, nil
}

// linkSource 是上一份校验通过的备份里的媒体文件表（相对路径 → 清单项）；没有就是 nil
type linkFrom struct {
	folder string
	files  map[string]FileEntry
}

func (b *builder) linkSource() *linkFrom {
	entries, err := Scan(b.dir)
	if err != nil {
		return nil
	}
	for _, e := range b.s.ours(entries) { // 新的在前；只认做完且校验通过的，bad、partial 不当来源
		if e.Partial || e.Manifest.Status != StatusOK {
			continue
		}
		full, err := ReadManifest(e.Folder)
		if err != nil {
			continue
		}
		lf := &linkFrom{folder: e.Folder, files: map[string]FileEntry{}}
		for _, f := range full.Files {
			if f.Class == ClassMedia && f.SHA256 != "" {
				lf.files[f.Path] = f
			}
		}
		if len(lf.files) == 0 {
			continue // 上一份没带媒体：再往前找一份带了的
		}
		return lf
	}
	return nil
}

var hashName = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func (b *builder) mediaOne(it item, prev *linkFrom) {
	dst := b.dst(it.rel)
	// 硬链接复用只对内容寻址的文件：文件名是 64 位十六进制哈希，“同名同大小”才等于“同内容”；别的文件名（以后可能出现的）各复制各的
	if prev != nil && hashName.MatchString(path.Base(it.rel)) {
		if pf, ok := prev.files[it.rel]; ok && pf.Size == it.size {
			src := filepath.Join(prev.folder, "data", filepath.FromSlash(it.rel))
			if fi, err := os.Lstat(src); err == nil && fi.Mode().IsRegular() && fi.Size() == pf.Size {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
					if err := b.s.link(src, dst); err == nil {
						b.m.Files = append(b.m.Files, FileEntry{Path: it.rel, Size: pf.Size, SHA256: pf.SHA256, Class: ClassMedia, Linked: true})
						b.classes[ClassMedia] = true
						return
					}
				}
			}
		}
	}
	size, sum, err := b.s.copyFile(b.ctx, it.abs, dst)
	if err != nil {
		if b.ctx.Err() == nil {
			b.problem("复制媒体 %s 失败：%v", it.rel, err)
		}
		return
	}
	b.m.Files = append(b.m.Files, FileEntry{Path: it.rel, Size: size, SHA256: sum, Class: ClassMedia})
	b.classes[ClassMedia] = true
}
