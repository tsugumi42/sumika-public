package chatui

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/icons"
)

var (
	iconMu    sync.Mutex
	iconCache = map[string]*ui.SVG{} // 只缓存自己画的；MingCute 的由 icons 包缓存
)

// Icon 返回图标；fill 为真取实心版。颜色跟着元素的 TextColor（currentColor）。
// 先找自己画的（ownSVG），再按 iconAlias 的短名换成 MingCute 名，最后当 MingCute 名直接取，所以全套 MingCute 图标都能用。
// 名字取不到时 panic：图标名是代码里写的常量，写错应当在测试里就红。
func Icon(name string, fill bool) *ui.SVG {
	if paths, ok := ownSVG[name]; ok {
		return ownIcon(name, fill, paths)
	}
	mc := name
	if a, ok := iconAlias[name]; ok {
		mc = a
	}
	s, err := icons.SVG(mc, fill)
	if err != nil {
		panic("chatui: 没有这个图标：" + name + "（" + err.Error() + "）")
	}
	return s
}

func ownIcon(name string, fill bool, paths [2]string) *ui.SVG {
	key := name
	if fill {
		key += "#fill"
	}
	iconMu.Lock()
	defer iconMu.Unlock()
	if s, ok := iconCache[key]; ok {
		return s
	}
	body := paths[0]
	if fill && paths[1] != "" {
		body = paths[1]
	}
	s := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">` + body + `</svg>`))
	iconCache[key] = s
	return s
}

// iconAlias 是界面里用惯的短名 → MingCute 名（icons 包里的名字，不带 _line、_fill）。
// 从 Bun 版的表搬过来时名字就是这样对的；新图标直接写 MingCute 名，不用加到这里。
var iconAlias = map[string]string{
	"arrowDown":   "arrow_down",
	"arrowUp":     "arrow_up",
	"attach":      "attachment_2",
	"bell":        "notification",
	"bellOff":     "notification_off",
	"calendar":    "calendar_time_add",
	"chart":       "chart_bar",
	"chat":        "chat_2",
	"copy":        "copy_2",
	"crop":        "frame",
	"dashboard":   "dashboard_3",
	"download":    "download_2",
	"enter":       "corner_down_left",
	"eye":         "eye_2",
	"eyedropper":  "color_picker",
	"fileZip":     "file_zip",
	"folderOpen":  "folder_open",
	"fork":        "git_branch",
	"forward":     "share_forward",
	"globe":       "earth_2",
	"home":        "home_4",
	"info":        "information",
	"micOff":      "mic_off",
	"more":        "more_1",
	"parallel":    "multiselect",
	"queue":       "list_check",
	"quote":       "quote_left",
	"refresh":     "refresh_2",
	"rotateLeft":  "anticlockwise",
	"rotateRight": "clockwise",
	"search":      "search_2",
	"send":        "send_plane",
	"settings":    "settings_3",
	"sidebar":     "layout_right",
	"stop":        "stop_circle",
	"terminal":    "terminal_box",
	"trash":       "delete_2",
	"upload":      "upload_2",
	"user":        "user_3",
	"volumeOff":   "volume_mute",
	"zoomIn":      "zoom_in",
	"zoomOut":     "zoom_out",
}

// ownSVG 是 MingCute 里没有、我们自己画的图标（同样 24×24、圆头），以及为保持长相而固定住的老版本图标。
var ownSVG = map[string][2]string{
	// 固定住的老版本图标：MingCute 2.9.72 重画后和以前界面里的样子差得看得出（见 icons_legacy_test.go 的 pinnedLegacyIcons），
	// 先保持原样；要换成新长相由用户并排看了定。线性是描边写法，实心是填充写法。
	"add":      {`<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="2" d="M5 12h14m-7 7V5"/>`, `<path fill="currentColor" fill-rule="evenodd" d="M12 3.5A1.5 1.5 0 0 1 13.5 5v5.5H19a1.5 1.5 0 0 1 0 3h-5.5V19a1.5 1.5 0 0 1-3 0v-5.5H5a1.5 1.5 0 0 1 0-3h5.5V5A1.5 1.5 0 0 1 12 3.5" clip-rule="evenodd"/>`},
	"bird":     {`<path fill="currentColor" d="m21 6l.832.555A1 1 0 0 0 21 5zM3 20l-.832-.555A1 1 0 0 0 3 21zM18.874 6l-.968.25a1 1 0 0 0 .968.75zM3.577 17.594a1 1 0 0 0 .846 1.812L4 18.5zm4.624-7.195a1 1 0 0 0 1.598 1.202L9 11zM15 6a1 1 0 1 0 0 2zm.002 2a1 1 0 0 0 0-2zm4.283 2.569l-.984.179zm.163-2.241l-.832-.555zm-.303 1.467l-.984.179zM11 7h1a3 3 0 0 1 3-3V2a5 5 0 0 0-5 5zm10-1l-.832-.555l-1.552 2.328l.832.555l.832.554l1.552-2.327zm-1.855 3.795l-.984.179l.14.774l.984-.18l.984-.178l-.14-.774zM3 20v1h8.414v-2H3zm7.445-11.168l-.832-.555l-7.445 11.168L3 20l.832.555l7.445-11.168zM15 3v1c1.396 0 2.573.955 2.906 2.25l.968-.25l.968-.25A5 5 0 0 0 15 2zm3.874 3v1H21V5h-2.126zM11.5 10v1a1.5 1.5 0 0 1 1.5 1.5h2A3.5 3.5 0 0 0 11.5 9zm2.5 2.5h-1c0 .632-.29 1.187-.902 1.724c-.631.554-1.542 1.029-2.609 1.446c-1.057.414-2.193.744-3.242 1.045c-1.003.289-2.01.571-2.67.879L4 18.5l.423.906c.48-.223 1.291-.457 2.376-.768c1.04-.299 2.262-.652 3.42-1.105c1.148-.45 2.31-1.026 3.199-1.806C14.325 14.93 15 13.867 15 12.5zM9 11l.8.6c.058-.078.268-.243.664-.391c.373-.14.765-.209 1.036-.209V9a5.2 5.2 0 0 0-1.736.336c-.534.2-1.166.535-1.563 1.063zm6-4v1h.002V6H15zm-4 0h-1c0 .455-.135.9-.387 1.277l.832.555l.832.555A4.3 4.3 0 0 0 12 7zm8.285 3.569l-.984.179c.782 4.297-2.52 8.252-6.887 8.252v2c5.615 0 9.86-5.085 8.855-10.61zm.163-2.241l-.832-.555a3 3 0 0 0-.455 2.2l.983-.178l.984-.179a1 1 0 0 1 .152-.734z"/>`, `<path fill="currentColor" d="M15 2a5 5 0 0 1 4.584 3H21a1 1 0 0 1 .832 1.555L20.28 8.883a1 1 0 0 0-.151.733l.14.774C21.275 15.914 17.03 21 11.415 21H3a1 1 0 0 1-.832-1.555l.991-1.486q.009-.016.019-.028l4.985-7.479l.009-.012l1.441-2.163A2.3 2.3 0 0 0 10 7a5 5 0 0 1 5-5m-2.5 8c-.818 0-2.044.393-2.5 1l-5 7.5c2.277-1.063 10-2 10-6a2.5 2.5 0 0 0-2.5-2.5M15 6a1 1 0 1 0 0 2a1 1 0 0 0 0-2"/>`},
	"bookmark": {`<path fill="none" stroke="currentColor" stroke-width="2" d="M6 6a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v14.066a.5.5 0 0 1-.777.416l-4.946-3.297a.5.5 0 0 0-.554 0l-4.946 3.297A.5.5 0 0 1 6 20.066z"/>`, `<path fill="currentColor" fill-rule="evenodd" d="M5 6a3 3 0 0 1 3-3h8a3 3 0 0 1 3 3v14.066c0 1.198-1.335 1.912-2.332 1.248L12 18.202l-4.668 3.112C6.335 21.978 5 21.264 5 20.066z" clip-rule="evenodd"/>`},
	"brain":    {`<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 7v10m0-10a3 3 0 1 0-6 0v2a3 3 0 1 0 0 6m6-8a3 3 0 1 1 6 0v2a3 3 0 1 1 0 6m-6 2a3 3 0 1 1-6 0v-2m6 2a3 3 0 1 0 6 0v-2M6 15c.35 0 .687-.06 1-.17M9 12a3 3 0 0 1 3 3a3 3 0 0 1 3-3m3 3c-.35 0-.687-.06-1-.17"/>`, `<path fill="currentColor" d="M15 3a4 4 0 0 1 4 4v1.126a4.002 4.002 0 0 1 0 7.748V17a4 4 0 0 1-7 2.645A4 4 0 0 1 5 17v-1.126a4.002 4.002 0 0 1 0-7.748V7a4 4 0 0 1 7-2.646A4 4 0 0 1 15 3m-3 3a1 1 0 0 0-1 1v4.536A4 4 0 0 0 9 11a1 1 0 0 0 0 2a2 2 0 0 1 2 2v2a1 1 0 0 0 2 0v-2a2 2 0 0 1 2-2a1 1 0 0 0 0-2c-.729 0-1.412.196-2 .536V7a1 1 0 0 0-1-1"/>`},
	"checks":   {`<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M2.929 11.829L7.17 16.07m4.95-4.95l4.95-4.95m-9.141 5.66l4.242 4.242l9.9-9.9"/>`, `<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="m2.929 11.829l3.914 3.914m5.659-5.659l4.24-4.24m-8.461 5.633l4.242 4.243l9.548-9.548"/>`},
	"minus":    {`<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="2" d="M5 12h14"/>`, `<path fill="currentColor" fill-rule="evenodd" d="M3.5 12A1.5 1.5 0 0 1 5 10.5h14a1.5 1.5 0 0 1 0 3H5A1.5 1.5 0 0 1 3.5 12" clip-rule="evenodd"/>`},
	"pic":      {`<path fill="currentColor" d="M16 8.5a.5.5 0 1 1-1 0a.5.5 0 0 1 1 0"/><path fill="currentColor" d="M14.121 14.879a1 1 0 1 0 1.415 1.414l-.708-.707zm2.829-1.415l.707-.707a1 1 0 0 0-1.414 0zm3.328 4.743a1 1 0 0 0 1.414-1.414l-.707.707zm-10.4-7.571l.708-.707a1 1 0 0 0-1.415 0zm5.158 6.571a1 1 0 0 0 1.414-1.414l-.707.707zM4 4v1h16V3H4zm17 1h-1v14h2V5zm-1 15v-1H4v2h16zM3 19h1V5H2v14zm11.828-3.414l.708.707l2.12-2.121l-.706-.708l-.707-.707l-2.122 2.122zm2.122-2.122l-.707.707l4.035 4.036l.707-.707l.707-.707l-4.035-4.036zm-7.071-2.828l-.707.707l5.864 5.864l.707-.707l.707-.707l-5.864-5.864zM3 17.515l.707.707l6.879-6.879l-.707-.707l-.707-.707l-6.88 6.879zM16 8.5h-1a.5.5 0 0 1 .5-.5v2A1.5 1.5 0 0 0 17 8.5zm-.5.5V8a.5.5 0 0 1 .5.5h-2a1.5 1.5 0 0 0 1.5 1.5zm-.5-.5h1a.5.5 0 0 1-.5.5V7A1.5 1.5 0 0 0 14 8.5zm.5-.5v1a.5.5 0 0 1-.5-.5h2A1.5 1.5 0 0 0 15.5 7zM4 20v-1H2a2 2 0 0 0 2 2zm17-1h-1v2a2 2 0 0 0 2-2zM20 4v1h2a2 2 0 0 0-2-2zM4 4V3a2 2 0 0 0-2 2h2z"/>`, `<path fill="currentColor" fill-rule="evenodd" d="M2 5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2zm18 0H4v11.1l5.172-5.171a1 1 0 0 1 1.414 0l4.242 4.243l1.415-1.415a1 1 0 0 1 1.414 0L20 16.101zm-6 3.5a1.5 1.5 0 1 1 3 0a1.5 1.5 0 0 1-3 0" clip-rule="evenodd"/>`},
	"pin":      {`<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="2" d="M9.879 14.122c2.508 2.509 4.275 3.781 5.3 4.402c.534.324 1.145-.02 1.233-.638l.175-1.227a10 10 0 0 1 1.9-4.586l2.185-2.914a1 1 0 0 0-.093-1.307l-4.43-4.43a1 1 0 0 0-1.307-.093l-2.914 2.185a10 10 0 0 1-4.586 1.9l-1.227.175c-.618.088-.962.7-.639 1.233c.622 1.025 1.894 2.792 4.403 5.3Zm0 0l-4.95 4.95"/>`, `<path fill="currentColor" fill-rule="evenodd" d="M14.242 2.529a2 2 0 0 1 2.614.186l4.43 4.43a2 2 0 0 1 .186 2.614l-2.185 2.914a9 9 0 0 0-1.71 4.127l-.175 1.227a1.89 1.89 0 0 1-.969 1.413a1.78 1.78 0 0 1-1.772-.06c-1-.607-2.592-1.749-4.776-3.85l-4.249 4.25a1 1 0 0 1-1.414-1.415l4.25-4.25C6.37 11.933 5.226 10.34 4.621 9.34a1.78 1.78 0 0 1-.062-1.772a1.89 1.89 0 0 1 1.414-.97L7.2 6.425a9 9 0 0 0 4.127-1.71z" clip-rule="evenodd"/>`},
	"qq":       {`<path fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="2" d="M4.5 17c.368-2.21 1.28-4.148 1.733-5.016c.169-.321.267-.675.267-1.038V8.5a5.5 5.5 0 1 1 11 0v2.446c0 .363.098.717.267 1.038c.454.868 1.365 2.806 1.733 5.016M6.389 14l-.056 1.01a5.676 5.676 0 1 0 11.334 0L17.611 14"/><path fill="currentColor" d="M12 21.09c0 .502-.5.909-2 .909s-4 .048-4-.454C6 20.636 8.857 20 9.857 20S12 20.588 12 21.09m0 0c0 .502.5.909 2 .909s4 .048 4-.454C18 20.636 15.143 20 14.143 20S12 20.588 12 21.09"/>`, `<path fill="currentColor" fill-rule="evenodd" d="m18.514 17.164l-.037-.211a6.7 6.7 0 0 1-2.138 3.445c.885.274 1.661.674 1.661 1.147c0 .463-2.125.458-3.633.454H14a5.4 5.4 0 0 1-.975-.077a6.7 6.7 0 0 1-2.05 0c-.264.05-.586.077-.975.077h-.367C8.125 22.003 6 22.008 6 21.545c0-.474.776-.873 1.66-1.147a6.7 6.7 0 0 1-2.136-3.445l-.038.211a1 1 0 1 1-1.972-.328c.393-2.36 1.358-4.407 1.834-5.316c.103-.197.152-.393.152-.574V8.5a6.5 6.5 0 1 1 13 0v2.446c0 .18.05.377.152.574c.476.909 1.441 2.955 1.834 5.316a1 1 0 1 1-1.972.328" clip-rule="evenodd"/>`},
	"tool":     {`<path fill="none" stroke="currentColor" stroke-linejoin="round" stroke-width="2" d="M13.646 5.14a6 6 0 0 1 1.267 6.62l4.928 4.17a2.76 2.76 0 1 1-3.89 3.89l-4.17-4.927a6.002 6.002 0 0 1-7.8-8.083l3.832 4.162l2.652-.527l.53-2.655L6.83 3.96a6 6 0 0 1 6.816 1.18Z"/>`, `<path fill="currentColor" d="M4.338 6.764c-.435-.527-1.259-.466-1.497.174a7.001 7.001 0 0 0 8.299 9.227c.423-.108.882.016 1.15.36l2.966 3.825a3.648 3.648 0 1 0 5.126-5.113l-3.834-2.99c-.342-.267-.466-.724-.36-1.145a7.001 7.001 0 0 0-9.202-8.291c-.642.235-.706 1.06-.18 1.497l3.732 3.103a1 1 0 0 1 .341.965l-.282 1.416a1 1 0 0 1-.786.785l-1.411.28a1 1 0 0 1-.966-.344z"/>`},
	// 拖动把手：两列三排小圆点，列表里“按住这里拖动排序”的记号。
	"grip": {`<path fill="currentColor" d="M9 4.5a1.75 1.75 0 1 1 0 3.5a1.75 1.75 0 0 1 0-3.5m6 0a1.75 1.75 0 1 1 0 3.5a1.75 1.75 0 0 1 0-3.5M9 10.25a1.75 1.75 0 1 1 0 3.5a1.75 1.75 0 0 1 0-3.5m6 0a1.75 1.75 0 1 1 0 3.5a1.75 1.75 0 0 1 0-3.5M9 16a1.75 1.75 0 1 1 0 3.5a1.75 1.75 0 0 1 0-3.5m6 0a1.75 1.75 0 1 1 0 3.5a1.75 1.75 0 0 1 0-3.5"/>`, ""},
	// 实心圆挖空感叹号：用 evenodd 把竖条和圆点挖成洞，整个是一块图形。
	// 圆占满整个 24 格（半径 12）：角标的框是多大，看得见的圆就是多大（规格"18 圆角标"；原来半径 10 只剩 15）。
	"alert": {"", `<path fill="currentColor" fill-rule="evenodd" d="M12 0a12 12 0 1 1 0 24a12 12 0 0 1 0-24m0 5.1c-.83 0-1.5.67-1.5 1.5v6.6a1.5 1.5 0 0 0 3 0V6.6c0-.83-.67-1.5-1.5-1.5m0 11.22a1.74 1.74 0 1 0 0 3.48a1.74 1.74 0 0 0 0-3.48"/>`},
}
