package desktopread

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"sumika-native/internal/api"
)

// 头像：key 只认 user:<号> / group:<号>；登记了哈希且图在媒体库里才给字节，其余（没登记、哈希空、哈希不合格、图不在）都是 ErrNoAvatar；
// 不合格的 key 和没接数据目录的另算，不要和"没有这个头像"混在一起（界面对前者不重试，对后者也不重试，但日志要分得开）
func TestReadAvatar(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	good := putMedia(t, root, "qq", pngBytes)
	big := putMedia(t, root, "qq", bytes.Repeat([]byte{7}, avatarMax+1))
	for _, r := range [][3]any{
		{"user:90001", good, 100},
		{"group:90002", good, 100},
		{"user:90003", nil, 100},                                   // 上次没取到
		{"user:90004", "", 100},                                    // 空哈希
		{"user:90005", "编造的头像", 100},                               // 哈希不合格（导入的旧数据里见过）
		{"user:90006", string(bytes.Repeat([]byte("a"), 64)), 100}, // 合格但图不在媒体库里
		{"user:90007", big, 100},
		{"url:https://example.test/a.png", good, 100}, // 卡片封面：不给
	} {
		if _, e := qq.Events.Exec(`INSERT INTO qq_avatars(key,hash,fetched_at) VALUES (?,?,?)`, r[0], r[1], r[2]); e != nil {
			t.Fatal(e)
		}
	}
	ask := func(key string) (api.Result[api.MediaView], error) { return s.ReadAvatar(context.Background(), key) }

	for _, k := range []string{"user:90001", "group:90002"} {
		r, err := ask(k)
		if err != nil || r.State != api.Ready || !bytes.Equal(r.Value.Bytes, pngBytes) {
			t.Fatalf("%s：%+v %v", k, r.State, err)
		}
	}
	for _, k := range []string{"user:90003", "user:90004", "user:90005", "user:90006", "user:99999"} {
		if r, err := ask(k); !errors.Is(err, api.ErrNoAvatar) || r.State != api.Failed || len(r.Value.Bytes) != 0 {
			t.Fatalf("%s 应是没有这个头像：%+v %v", k, r.State, err)
		}
	}
	if _, err := ask("user:90007"); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("超过 5 MiB 应报太大：%v", err)
	}
	for _, k := range []string{"", "url:https://example.test/a.png", "user:12", "user:1234567890123", "user:../x", "user:9000a", "group:", "USER:90001", "user:90001 ", "user:90001\n", "qq:90001", "/user:90001"} {
		r, err := ask(k)
		if err == nil || errors.Is(err, api.ErrNoAvatar) || r.State != api.Failed || len(r.Value.Bytes) != 0 {
			t.Fatalf("不合格的 key %q 应拒掉：%+v %v", k, r.State, err)
		}
	}
	s.data = ""
	if r, err := ask("user:90001"); !errors.Is(err, ErrNoData) || r.State != api.Unsupported {
		t.Fatalf("没接数据目录：%+v %v", r.State, err)
	}
}
