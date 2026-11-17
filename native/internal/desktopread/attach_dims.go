package desktopread

import (
	"os"

	"sumika-native/internal/mediastore"
)

// imageDims 量一张本地图片的像素宽高（只读文件头，已按 EXIF 转正，和界面解码出来的方向一致）；
// 文件读不到、不是图、过大都返回 0，0：界面照 4:3 占位，不拿它当错误。
func imageDims(path string) (w, h int) {
	if path == "" {
		return 0, 0
	}
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
