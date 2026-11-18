package snowluma

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 文件、视频、语音三种段的翻译（2026-11-02 真机：接管后库里 file 段只有 {"kind":"file"}，界面写"[文件：文件]"）。
// 字段形状出自 SnowLuma 的 index.mjs（约 21527、21550、21609 行，细节见 types.Seg 的注释）：
// file 有 file、name、file_id、id、file_size、size、url、file_hash；record、video 只有 file、url。
// 下面全是编的帧，不读任何真实数据。旧代码这三支只设 Kind，凡是期望里带 File、URL、FileID、Size 的行都会红。
func TestFileVideoVoiceSegments(t *testing.T) {
	long := strings.Repeat("a", types.MediaRefMax+1)
	const dl = "https://files.example.invalid/download?fileid=编的&rkey=编的"
	file := func(s types.Seg) types.Seg { s.Kind = types.SegFile; return s }
	video := func(s types.Seg) types.Seg { s.Kind = types.SegVideo; return s }
	voice := func(s types.Seg) types.Seg { s.Kind = types.SegVoice; return s }
	cases := []struct {
		name, typ, data string // data 为空表示这一段没有 data 字段
		want            types.Seg
	}{
		// file：形状
		{"file-real-shape", "file", `{"file":"清单.txt","file_id":"/编的-id-1","file_size":2048,"name":"清单.txt","size":2048,"id":"/编的-id-1","url":"` + dl + `","file_hash":"ab12"}`,
			file(types.Seg{File: "清单.txt", FileID: "/编的-id-1", FileHash: "ab12", Size: 2048, URL: dl})},
		{"file-private-without-url", "file", `{"file":"清单.txt","file_id":"f1","file_size":7,"name":"清单.txt","size":7,"id":"f1","url":"","file_hash":""}`,
			file(types.Seg{File: "清单.txt", FileID: "f1", Size: 7})},
		{"file-no-data", "file", ``, file(types.Seg{})},
		{"file-data-not-object", "file", `"abc"`, file(types.Seg{})},
		{"file-data-empty", "file", `{}`, file(types.Seg{})},
		{"file-data-null", "file", `null`, file(types.Seg{})},
		// file：名字
		{"name-from-file-when-name-missing", "file", `{"file":"a.txt"}`, file(types.Seg{File: "a.txt"})},
		{"name-from-file-when-name-empty", "file", `{"name":"","file":"b.txt"}`, file(types.Seg{File: "b.txt"})},
		{"name-prefers-name", "file", `{"name":"n.txt","file":"f.txt"}`, file(types.Seg{File: "n.txt"})},
		{"name-number-falls-back", "file", `{"name":12345,"file":"c.txt"}`, file(types.Seg{File: "c.txt"})},
		{"name-number-alone", "file", `{"name":12345}`, file(types.Seg{})},
		{"name-array", "file", `{"name":["x.txt"]}`, file(types.Seg{})},
		{"name-object", "file", `{"name":{"a":1}}`, file(types.Seg{})},
		{"name-null", "file", `{"name":null,"file":"d.txt"}`, file(types.Seg{File: "d.txt"})},
		{"name-dotdot-path", "file", `{"name":"../../a.txt"}`, file(types.Seg{})},
		{"name-bad-falls-back-to-file", "file", `{"name":"../a.txt","file":"ok.txt"}`, file(types.Seg{File: "ok.txt"})},
		{"name-both-bad", "file", `{"name":"x/y.txt","file":"x\\y.txt"}`, file(types.Seg{})},
		{"name-backslash", "file", `{"name":"..\\a.txt"}`, file(types.Seg{})},
		// 冒号是合法的文件名字符（手机端、Linux 发来的 "录音 12:30.m4a"）；文件名只显示不当路径，不像图片的哈希名那样拒冒号
		{"name-colon", "file", `{"name":"录音 12:30.m4a"}`, file(types.Seg{File: "录音 12:30.m4a"})},
		{"name-drive-kept", "file", `{"name":"C:a.txt"}`, file(types.Seg{File: "C:a.txt"})},
		{"name-colon-only-in-file", "file", `{"file":"a:b.txt"}`, file(types.Seg{File: "a:b.txt"})},
		{"name-colon-with-slash", "file", `{"name":"a:b/c.txt"}`, file(types.Seg{})},
		{"name-dot", "file", `{"name":"."}`, file(types.Seg{})},
		{"name-dots", "file", `{"name":".."}`, file(types.Seg{})},
		{"name-control", "file", `{"name":"a\u0000.txt"}`, file(types.Seg{})},
		{"name-newline", "file", `{"name":"a\nb.txt"}`, file(types.Seg{})},
		{"name-too-long", "file", `{"name":"` + long + `"}`, file(types.Seg{})},
		{"name-max-long", "file", `{"name":"` + long[1:] + `"}`, file(types.Seg{File: long[1:]})},
		// 方向控制字符不是 Unicode 的控制类（Cc），翻译时留着；显示前由 desktopread 清掉（desktopread 的 TestDisplayFileSegments）
		{"name-rtlo-kept-here", "file", `{"name":"a\u202Etxt.exe"}`, file(types.Seg{File: "a\u202Etxt.exe"})},
		// file：大小
		{"size-zero", "file", `{"name":"a","file_size":0,"size":0}`, file(types.Seg{File: "a"})},
		{"size-negative", "file", `{"name":"a","file_size":-1}`, file(types.Seg{File: "a"})},
		{"size-string", "file", `{"name":"a","file_size":"123"}`, file(types.Seg{File: "a", Size: 123})},
		{"size-string-spaces", "file", `{"name":"a","file_size":" 123 "}`, file(types.Seg{File: "a", Size: 123})},
		{"size-float-whole", "file", `{"name":"a","file_size":12.0}`, file(types.Seg{File: "a", Size: 12})},
		{"size-fraction", "file", `{"name":"a","file_size":12.5}`, file(types.Seg{File: "a"})},
		{"size-huge", "file", `{"name":"a","file_size":1e20}`, file(types.Seg{File: "a"})},
		{"size-over-2^53", "file", `{"name":"a","file_size":"9007199254740994"}`, file(types.Seg{File: "a"})},
		{"size-2^53", "file", `{"name":"a","file_size":9007199254740992}`, file(types.Seg{File: "a", Size: 1 << 53})},
		{"size-word", "file", `{"name":"a","file_size":"abc"}`, file(types.Seg{File: "a"})},
		{"size-nan-word", "file", `{"name":"a","file_size":"NaN"}`, file(types.Seg{File: "a"})},
		{"size-inf-word", "file", `{"name":"a","file_size":"Inf"}`, file(types.Seg{File: "a"})},
		{"size-bool", "file", `{"name":"a","file_size":true}`, file(types.Seg{File: "a"})},
		{"size-null", "file", `{"name":"a","file_size":null}`, file(types.Seg{File: "a"})},
		{"size-object", "file", `{"name":"a","file_size":{"n":1}}`, file(types.Seg{File: "a"})},
		{"size-from-size-when-file-size-bad", "file", `{"name":"a","file_size":-1,"size":7}`, file(types.Seg{File: "a", Size: 7})},
		{"size-from-size-when-file-size-missing", "file", `{"name":"a","size":9}`, file(types.Seg{File: "a", Size: 9})},
		// file：地址
		{"url-http", "file", `{"name":"a","url":"http://files.example.invalid/x"}`, file(types.Seg{File: "a", URL: "http://files.example.invalid/x"})},
		{"url-file-scheme", "file", `{"name":"a","url":"file:///C:/Users/x/a.txt"}`, file(types.Seg{File: "a"})},
		{"url-base64", "file", `{"name":"a","url":"base64://AAAA"}`, file(types.Seg{File: "a"})},
		{"url-local-path", "file", `{"name":"a","url":"C:\\Users\\x\\a.txt"}`, file(types.Seg{File: "a"})},
		{"url-control", "file", `{"name":"a","url":"https://files.example.invalid/\u0001"}`, file(types.Seg{File: "a"})},
		{"url-too-long", "file", `{"name":"a","url":"https://` + long + `"}`, file(types.Seg{File: "a"})},
		{"url-number", "file", `{"name":"a","url":12345}`, file(types.Seg{File: "a"})},
		// file：文件标识
		{"id-from-id", "file", `{"name":"a","id":"/只有-id"}`, file(types.Seg{File: "a", FileID: "/只有-id"})},
		{"id-prefers-file_id", "file", `{"name":"a","file_id":"x1","id":"x2"}`, file(types.Seg{File: "a", FileID: "x1"})},
		{"id-number", "file", `{"name":"a","file_id":12,"id":13}`, file(types.Seg{File: "a"})},
		{"id-control", "file", `{"name":"a","file_id":"x\u0000y"}`, file(types.Seg{File: "a"})},
		{"id-too-long", "file", `{"name":"a","file_id":"` + long + `"}`, file(types.Seg{File: "a"})},
		{"id-bad-falls-back", "file", `{"name":"a","file_id":"x\u0000y","id":"ok"}`, file(types.Seg{File: "a", FileID: "ok"})},
		// 私聊文件取址令牌同样只收有界字符串，不当作内容 hash。
		{"hash-empty", "file", `{"file_hash":""}`, file(types.Seg{})},
		{"hash-null", "file", `{"file_hash":null}`, file(types.Seg{})},
		{"hash-number", "file", `{"file_hash":123}`, file(types.Seg{})},
		{"hash-array", "file", `{"file_hash":["x"]}`, file(types.Seg{})},
		{"hash-object", "file", `{"file_hash":{"x":1}}`, file(types.Seg{})},
		{"hash-control", "file", `{"file_hash":"x\u0000y"}`, file(types.Seg{})},
		{"hash-too-long", "file", `{"file_hash":"` + long + `"}`, file(types.Seg{})},
		{"hash-max-length", "file", `{"file_hash":"` + long[1:] + `"}`, file(types.Seg{FileHash: long[1:]})},
		// video、voice：data 里只有 file 和 url
		{"video-real-shape", "video", `{"file":"clip.mp4","url":"` + dl + `"}`, video(types.Seg{File: "clip.mp4", URL: dl})},
		{"video-no-url", "video", `{"file":"clip.mp4","url":""}`, video(types.Seg{File: "clip.mp4"})},
		{"video-no-data", "video", ``, video(types.Seg{})},
		{"video-data-not-object", "video", `[1]`, video(types.Seg{})},
		{"video-file-with-slash", "video", `{"file":"a/b.mp4","url":"` + dl + `"}`, video(types.Seg{URL: dl})},
		{"video-url-file-scheme", "video", `{"file":"clip.mp4","url":"file:///C:/a.mp4"}`, video(types.Seg{File: "clip.mp4"})},
		{"video-url-number", "video", `{"file":"clip.mp4","url":1}`, video(types.Seg{File: "clip.mp4"})},
		{"video-file-too-long", "video", `{"file":"` + long + `"}`, video(types.Seg{})},
		{"video-size-and-id", "video", `{"file":"clip.mp4","file_id":"/opaque","file_size":"2048","url":"` + dl + `"}`, video(types.Seg{File: "clip.mp4", FileID: "/opaque", Size: 2048, URL: dl})},
		{"video-size-fallback", "video", `{"file_size":-1,"size":33,"id":"v1"}`, video(types.Seg{Size: 33, FileID: "v1"})},
		{"video-wrong-types", "video", `{"file":{},"file_id":4,"url":[],"size":true}`, video(types.Seg{})},
		{"video-null", "video", `null`, video(types.Seg{})},
		{"video-zero", "video", `{"file":"","url":"","size":0}`, video(types.Seg{})},
		{"video-huge-size", "video", `{"size":1e20}`, video(types.Seg{})},
		{"video-fraction", "video", `{"size":1.2}`, video(types.Seg{})},
		{"video-control", "video", `{"file":"a\u0000.mp4","file_id":"x\n","url":"https://a.invalid/\n"}`, video(types.Seg{})},
		{"video-url-long", "video", `{"url":"https://` + long + `","file_id":"` + long + `"}`, video(types.Seg{})},
		{"voice-real-shape", "record", `{"file":"voice.amr","url":"` + dl + `"}`, voice(types.Seg{File: "voice.amr", URL: dl})},
		{"voice-file-is-id", "record", `{"file":"0123ABCD-编的-uuid","url":""}`, voice(types.Seg{File: "0123ABCD-编的-uuid"})},
		{"voice-no-data", "record", ``, voice(types.Seg{})},
		{"voice-data-null", "record", `null`, voice(types.Seg{})},
		{"voice-url-base64", "record", `{"file":"voice.amr","url":"base64://AAAA"}`, voice(types.Seg{File: "voice.amr"})},
		{"voice-file-control", "record", `{"file":"a\u0000","url":"` + dl + `"}`, voice(types.Seg{URL: dl})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"type":"` + tc.typ + `"`
			if tc.data != "" {
				raw += `,"data":` + tc.data
			}
			raw += `}`
			got, err := segments([]byte(`[` + raw + `]`))
			if err != nil || !reflect.DeepEqual(got, []types.Seg{tc.want}) {
				t.Fatalf("%s\n得 %#v（%v）\n要 %#v", raw, got, err, tc.want)
			}
		})
	}
}

// 整帧走一遍：三种段混在一条消息里，前后的文字照常，每段各自翻对（编的帧，形状照上面）
func TestFileVideoVoiceInFrame(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	frame := `{"post_type":"message","time":100,"self_id":100,"user_id":200,"group_id":400,"message_type":"group","message_id":300,"sender":{"nickname":"虚构甲","card":"虚构乙"},"message":[` +
		`{"type":"text","data":{"text":"看"}},` +
		`{"type":"file","data":{"file":"清单.txt","file_id":"/f1","file_size":2048,"name":"清单.txt","size":2048,"id":"/f1","url":"https://files.example.invalid/a","file_hash":"x"}},` +
		`{"type":"video","data":{"file":"clip.mp4","url":"https://files.example.invalid/v"}},` +
		`{"type":"record","data":{"file":"voice.amr","url":"https://files.example.invalid/r"}}]}`
	in, n, st, err := translate([]byte(frame))
	if err != nil || n != nil || st != nil || in == nil {
		t.Fatalf("translate = %#v %#v %#v %v", in, n, st, err)
	}
	want := []types.Seg{
		{Kind: types.SegText, Text: "看"},
		{Kind: types.SegFile, File: "清单.txt", FileID: "/f1", FileHash: "x", Size: 2048, URL: "https://files.example.invalid/a"},
		{Kind: types.SegVideo, File: "clip.mp4", URL: "https://files.example.invalid/v"},
		{Kind: types.SegVoice, File: "voice.amr", URL: "https://files.example.invalid/r"},
	}
	if !reflect.DeepEqual(in.Segs, want) || !in.Time.Equal(at) {
		t.Fatalf("段 = %#v\n要 %#v", in.Segs, want)
	}
}
