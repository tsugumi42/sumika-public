package mediafetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os/exec"
	"strconv"
	"time"

	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
	"sumika-native/internal/toolchain"
)

type videoTools struct{ ffmpeg, ffprobe string }

// 测试替换寻找函数，缺工具不需要把机器 PATH 改成不存在。
var findVideoTools = func() videoTools {
	path := func(name string) string {
		if r, ok := toolchain.Lookup(name); ok {
			return r.Path
		}
		return ""
	}
	return videoTools{path("ffmpeg"), path("ffprobe")}
}

// videoToolPaths 第一次真要处理视频时才向登记表要 ffmpeg/ffprobe；New、Start（开后台的路径）都不查。
// 查一次在杀毒软件扫着时能花几秒，不能挡在开后台和开关接收前面；Lookup 读缓存，缓存没好时它自己现查一次。
func (f *Fetcher) videoToolPaths() videoTools {
	f.videoToolsOnce.Do(func() {
		f.videoTools = findVideoTools()
		if f.videoTools.ffmpeg == "" || f.videoTools.ffprobe == "" {
			f.warnf("视频：未找到 ffmpeg/ffprobe，保留本地视频播放，不生成封面和时长")
		}
	})
	return f.videoTools
}

// 外部输出有硬上限，坏文件不能让探测结果或封面无限占内存。
type videoOutput struct {
	bytes.Buffer
	max int
}

func (b *videoOutput) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, errors.New("视频工具输出超限")
	}
	return b.Buffer.Write(p)
}

func videoCommand(ctx context.Context, name string, max int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	lowVideoPriority(cmd)
	out := videoOutput{max: max}
	cmd.Stdout = &out
	cmd.Stderr = io.Discard // 可能包含文件路径或文件内的 URL；只向调用方报告固定原因。
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func probeVideo(b []byte) (width, height int, durationMS int64, err error) {
	var p struct {
		Streams []struct {
			Width, Height int
			Duration      string
			SideData      []struct{ Rotation int } `json:"side_data_list"`
		}
		Format struct{ Duration string }
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return
	}
	if len(p.Streams) == 0 {
		err = errors.New("没有视频流")
		return
	}
	s := p.Streams[0]
	if s.Width <= 0 || s.Height <= 0 || s.Width > 65535 || s.Height > 65535 {
		err = errors.New("视频尺寸不合法")
		return
	}
	width, height = s.Width, s.Height
	for _, data := range s.SideData {
		if data.Rotation%180 != 0 {
			width, height = height, width
			break
		}
	}
	d := p.Format.Duration
	if d == "" || d == "N/A" {
		d = s.Duration
	}
	seconds, e := strconv.ParseFloat(d, 64)
	if e != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= float64(math.MaxInt64)/1000 {
		return width, height, 0, nil
	}
	return width, height, int64(math.Round(seconds * 1000)), nil
}

func (f *Fetcher) videoMeta(ctx context.Context, hash string, row *store.MediaFetch) {
	tools := f.videoToolPaths()
	if tools.ffmpeg == "" || tools.ffprobe == "" {
		return
	}
	select {
	case f.videoGate <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-f.videoGate }()
	file, err := mediastore.OpenMedia(f.o.Data, f.o.Platform, hash)
	if err != nil {
		f.warnf("视频：事件 %d 打开本地文件失败", row.EventSeq)
		return
	}
	path := file.Name()
	file.Close()
	// 只允许读本地文件，不让容器里引用的远程资源触发网络访问。
	b, err := videoCommand(ctx, tools.ffprobe, 64<<10, "-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-select_streams", "v:0", "-show_entries", "stream=width,height,duration:stream_side_data=rotation:format=duration", "-of", "json", path)
	if err != nil {
		if f.live() {
			f.warnf("视频：事件 %d 探测失败，保留本地视频", row.EventSeq)
		}
		return
	}
	w, h, d, err := probeVideo(b)
	if err != nil {
		f.warnf("视频：事件 %d 探测输出不合法，保留本地视频", row.EventSeq)
		return
	}
	row.Width, row.Height, row.DurationMS = w, h, d
	b, err = videoCommand(ctx, tools.ffmpeg, 4<<20, "-v", "error", "-nostdin", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:v:0", "-frames:v", "1", "-an", "-vf", "scale=w='min(640,iw)':h='min(640,ih)':force_original_aspect_ratio=decrease", "-threads", "1", "-f", "image2pipe", "-c:v", "mjpeg", "pipe:1")
	if err != nil {
		if f.live() {
			f.warnf("视频：事件 %d 抽封面失败，保留时长和本地视频", row.EventSeq)
		}
		return
	}
	if ctx.Err() != nil {
		return
	}
	cover, _, err := f.put(bytes.NewReader(b), 4<<20)
	if err != nil {
		f.errorf("视频：事件 %d 保存封面失败", row.EventSeq)
		return
	}
	row.CoverHash = cover
}
