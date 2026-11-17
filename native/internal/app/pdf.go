package app

import (
	"path/filepath"

	"sumika-native/internal/pdftext"
	"sumika-native/internal/tools"
)

// openPDF 装配 read 的 PDF 引擎（规格-读文件和文档第三节）。WASM 编译推迟到第一次读 PDF；
// 编译缓存放 cache/pdfium，属于可再生数据，不进备份。
func (a *App) openPDF() {
	a.pdf = pdftext.New(filepath.Join(a.paths.CacheDir(), "pdfium"))
	tools.SetPDFEngine(a.pdf)
}

func (a *App) closePDF() {
	if a.pdf == nil {
		return
	}
	tools.ClearPDFEngine(a.pdf)
	a.pdf.Close()
}
