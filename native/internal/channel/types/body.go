package types

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

// MediaResource 的内容只存在于当前进程；JSON 仅保存不可重用的元信息。
// 构造后调用方交出数据所有权，直到 Outlet 释放前不能改写或关闭文件。
type MediaResource struct {
	ID     string `json:"id"`
	MIME   string `json:"mime"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Size   int64  `json:"size"`
	// MD5 是静态图、动图内容的 md5（小写十六进制），出口入队时算好随计划存下：回声里图的标识要和它比（媒体发完会释放，事后算不了）
	MD5      string `json:"md5,omitempty"`
	mu       sync.RWMutex
	data     []byte
	file     *os.File
	release  func() error
	released bool
}

func MemoryMedia(id, mime string, width, height int, data []byte) *MediaResource {
	return &MediaResource{ID: id, MIME: mime, Width: width, Height: height, Size: int64(len(data)), data: data}
}

// FileMedia 只接已经由宿主打开的文件，不接来自正文的路径或 URL。
func FileMedia(id, mime string, f *os.File, cleanup func() error) (*MediaResource, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("媒体不是普通文件")
	}
	return &MediaResource{ID: id, MIME: mime, Size: st.Size(), file: f, release: cleanup}, nil
}

func (m *MediaResource) Available() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.released && m.Size > 0 && (len(m.data) > 0 || m.file != nil)
}

// Read 调用期间持有读锁，释放不会关闭正在使用的文件。
func (m *MediaResource) Read(fn func(io.Reader) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.released {
		return errors.New("媒体已经释放")
	}
	if m.file != nil {
		return fn(io.NewSectionReader(m.file, 0, m.Size))
	}
	if len(m.data) == 0 {
		return errors.New("媒体内容不在当前进程")
	}
	return fn(bytes.NewReader(m.data))
}

func (m *MediaResource) FileName() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.released || m.file == nil {
		return "", errors.New("媒体没有受管理文件")
	}
	return m.file.Name(), nil
}

func (m *MediaResource) FileAvailable() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.released && m.file != nil && m.Size > 0
}

func (m *MediaResource) Release() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.released {
		return nil
	}
	m.released = true
	m.data = nil
	var err error
	if m.file != nil {
		err = m.file.Close()
		m.file = nil
	}
	if m.release != nil {
		err = errors.Join(err, m.release())
		m.release = nil
	}
	return err
}

// BodySegment 不含引用或节点；这两者只能由 Part 的固定结构表达。
type BodySegment struct {
	Kind  string         `json:"kind"`
	Text  string         `json:"text,omitempty"`
	Media *MediaResource `json:"media,omitempty"`
	// Semantic 是卡片绘制前已检查的文字，历史可展示，不能代替图外链接。
	Semantic string `json:"semantic,omitempty"`
}

const (
	BodyText  = "text"
	BodyImage = "image"
	BodyVideo = "video"
	BodyGIF   = "gif"
)

func TextBody(text string) []BodySegment { return []BodySegment{{Kind: BodyText, Text: text}} }
func Message(text string) Part           { return Part{Kind: PartMessage, Body: TextBody(text)} }

func BodyTextOf(body []BodySegment) string {
	var b bytes.Buffer
	for _, s := range body {
		if s.Kind == BodyText {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

func ValidateBody(body []BodySegment) error {
	if len(body) == 0 {
		return errors.New("消息没有内容")
	}
	for _, s := range body {
		switch s.Kind {
		case BodyText:
			if strings.TrimSpace(s.Text) == "" || s.Media != nil {
				return errors.New("空文字或非法文字段")
			}
		case BodyImage, BodyVideo, BodyGIF:
			if s.Text != "" || !s.Media.Available() {
				return errors.New("媒体不是可用的宿主资源")
			}
		default:
			return errors.New("不支持的消息内容段")
		}
	}
	return nil
}
