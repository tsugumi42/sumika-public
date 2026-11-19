package app

import (
	"bytes"
	"context"
	"errors"

	"sumika-native/internal/api"
	"sumika-native/internal/mediastore"
)

// MaxLocalImage 是一张粘贴图的上限：和剪贴板截图的量级相当，超了不收。
const MaxLocalImage = 20 << 20

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// SaveLocalImage 把主人粘贴进输入框的图存进媒体库（local 平台，内容寻址），返回可放进 LocalPart 的附件。
// 新图还没有事件，拿不到 Media 凭据（凭据要对上会话里已有的事件），所以给的是 Path；会话所有权照发送一样校验。
func (r *DesktopReader) SaveLocalImage(ctx context.Context, k api.ConversationKey, png []byte) (api.LocalAttachment, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.LocalAttachment{}, err
	}
	defer done()
	if _, err := r.localCharacter(ctx, k); err != nil {
		return api.LocalAttachment{}, err
	}
	if !bytes.HasPrefix(png, pngSignature) {
		return api.LocalAttachment{}, errors.New("剪贴板里的不是 PNG 图片")
	}
	root := r.desk.app.paths.Root
	hash, _, err := mediastore.Put(root, "local", bytes.NewReader(png), MaxLocalImage)
	if err != nil {
		return api.LocalAttachment{}, err
	}
	path, err := mediastore.Path(root, "local", hash)
	if err != nil {
		return api.LocalAttachment{}, err
	}
	return api.LocalAttachment{Kind: "image", Path: path, Name: "粘贴的图片.png"}, nil
}

var _ api.LocalImageSaver = (*DesktopReader)(nil)
