package linkplugin

import (
	"crypto/rand"
	"os"
	"path/filepath"

	"sumika-native/internal/channel/types"
)

// 视频进合并转发（实验，规格-链接发送改版 A6）的退路：协议端不收转发里的视频时，视频要单独补发一条。
// 视频不备份进内存（可能上百 MB），出口发完计划就释放它（连同所在的临时目录），所以发之前给合并转发里的视频
// 在同一个临时根目录下另建一个硬链接（不占空间、不拷贝，也不在发之前打开它，免得挡住出口删原文件）；
// 用不上就在这次展开收尾时删掉。硬链接建不成（跨盘、没有文件）就没有退路，退回时视频并进文字版。

type keptVideo struct {
	path, dir, mime string
}

type videoKeep struct {
	m map[*types.MediaResource]keptVideo
}

// keepVideos 给合并转发节点里的视频建硬链接；没有就返回空表
func keepVideos(parts []types.Part) *videoKeep {
	k := &videoKeep{m: map[*types.MediaResource]keptVideo{}}
	for _, p := range parts {
		if p.Kind != types.PartForward {
			continue
		}
		for _, n := range p.Nodes {
			for _, s := range n.Body {
				if s.Kind != types.BodyVideo || s.Media == nil {
					continue
				}
				name, err := s.Media.FileName()
				if err != nil {
					continue
				}
				dir := filepath.Join(filepath.Dir(filepath.Dir(name)), "keep-"+rand.Text())
				if os.MkdirAll(dir, 0o700) != nil {
					continue
				}
				path := filepath.Join(dir, filepath.Base(name))
				if os.Link(name, path) != nil {
					_ = os.RemoveAll(dir)
					continue
				}
				k.m[s.Media] = keptVideo{path: path, dir: dir, mime: s.Media.MIME}
			}
		}
	}
	return k
}

// reopen 用留下的硬链接造一段只有这个视频的消息和它的资源（资源交给出口，发完连目录一起删）；没留下返回 false
func (k *videoKeep) reopen(seg types.BodySegment) (types.Part, []*types.MediaResource, bool) {
	if k == nil {
		return types.Part{}, nil, false
	}
	v, ok := k.m[seg.Media]
	if !ok {
		return types.Part{}, nil, false
	}
	delete(k.m, seg.Media)
	f, err := os.Open(v.path)
	if err != nil {
		_ = os.RemoveAll(v.dir)
		return types.Part{}, nil, false
	}
	res, err := types.FileMedia(rand.Text(), v.mime, f, func() error { return os.RemoveAll(v.dir) })
	if err != nil {
		f.Close()
		_ = os.RemoveAll(v.dir)
		return types.Part{}, nil, false
	}
	return types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyVideo, Media: res}}}, []*types.MediaResource{res}, true
}

// release 删掉没用上的硬链接
func (k *videoKeep) release() {
	if k == nil {
		return
	}
	for m, v := range k.m {
		_ = os.RemoveAll(v.dir)
		delete(k.m, m)
	}
}
