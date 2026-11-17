package tools

import (
	"context"
	"fmt"
	"io"

	"sumika-native/internal/doctext"
	"sumika-native/internal/jsjson"
)

// readOffice 读 docx、xlsx（规格-读文件和文档第四节）。ok 为假表示这不是 Office 文档（普通 ZIP），交回去按二进制处理。
func readOffice(ctx context.Context, f io.ReaderAt, abs, path string, size int64, args jsjson.Value) (text string, ok bool, err error) {
	if _, has := argNum(args, "page"); has {
		return "", true, fmt.Errorf("page 只用于 PDF，%s 不是 PDF。用 offset、limit 分段。", path)
	}
	out, kind, err := doctext.Convert(f, size)
	if kind == doctext.KindUnknown {
		return "", false, nil
	}
	if ctx.Err() != nil {
		return "", true, errAborted
	}
	if err != nil {
		return "", true, fmt.Errorf("读不了 %s：%w", path, err)
	}
	if out == "" {
		out = "（文档里没有文字。）"
	}
	text, err = windowText(out, path, args)
	return text, true, err
}
