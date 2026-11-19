package mediafetch

import (
	"errors"
	"time"

	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

const (
	metaPage  = 64
	metaPause = 50 * time.Millisecond
)

func (f *Fetcher) imageMeta(hash string) (mediastore.ImageMeta, error) {
	file, err := mediastore.OpenMedia(f.o.Data, f.o.Platform, hash)
	if err != nil {
		return mediastore.ImageMeta{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return mediastore.ImageMeta{}, err
	}
	return mediastore.ReadImageMeta(file, info.Size()), nil
}

// 补扫独立于下载队列和桌面查询闸；每次启动顺序扫一遍，页间不持有数据库游标或事务。
func (f *Fetcher) backfillMeta() {
	defer f.wg.Done()
	f.backfillEventMeta()
	if f.o.Platform == "qq" {
		f.backfillForwardMeta()
	}
}

func (f *Fetcher) backfillEventMeta() {
	var seq int64
	idx := -1
	for f.live() {
		rows, err := f.o.Store.MediaMetaCandidates(seq, idx, metaPage)
		if err != nil {
			f.errorf("收图：查询待补元数据出错：%v", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		var done []store.MediaFetch
		for _, row := range rows {
			select {
			case <-f.ctx.Done():
				return
			case <-time.After(metaPause):
			}
			seq, idx = row.EventSeq, row.Idx
			m, err := f.imageMeta(row.Hash)
			if errors.Is(err, mediastore.ErrMediaMissing) {
				continue
			}
			if err != nil {
				f.warnf("收图：读取事件 %d 第 %d 段的元数据出错：%v", seq, idx, err)
				continue
			}
			if !f.live() {
				return
			}
			if m.Width == 0 || m.Height == 0 {
				continue
			}
			row.Width, row.Height, row.Animated = m.Width, m.Height, m.Animated
			done = append(done, row)
		}
		if len(done) > 0 {
			if err := f.o.Store.UpdateMediaMeta(done); err != nil {
				f.errorf("收图：补写 %d 张图的元数据出错：%v", len(done), err)
			}
		}
	}
}

func (f *Fetcher) backfillForwardMeta() {
	id, node, idx := "", -1, -1
	for f.live() {
		rows, err := f.o.Store.ForwardMediaMetaCandidates(id, node, idx, metaPage)
		if err != nil {
			f.errorf("转发图片：查询待补元数据出错：%v", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		var done []store.ForwardMedia
		for _, row := range rows {
			select {
			case <-f.ctx.Done():
				return
			case <-time.After(metaPause):
			}
			id, node, idx = row.ID, row.Node, row.Idx
			m, err := f.imageMeta(row.Hash)
			if errors.Is(err, mediastore.ErrMediaMissing) {
				continue
			}
			if err != nil {
				f.warnf("转发图片：读取节点 %d 第 %d 段的元数据出错：%v", node, idx, err)
				continue
			}
			if !f.live() {
				return
			}
			if m.Width == 0 || m.Height == 0 {
				continue
			}
			row.Width, row.Height, row.Animated = m.Width, m.Height, m.Animated
			done = append(done, row)
		}
		if len(done) > 0 {
			if err := f.o.Store.UpdateForwardMediaMeta(done); err != nil {
				f.errorf("转发图片：补写 %d 张图的元数据出错：%v", len(done), err)
			}
		}
	}
}
