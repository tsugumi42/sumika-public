package snowluma

import (
	"testing"

	"sumika-native/internal/channel/types"
)

// “对方正在输入”不进库：既不是消息也不是通知，是一条瞬时状态；认不出是谁的就丢。
func TestInputStatusIsTransient(t *testing.T) {
	frame := `{"time":1793625741,"self_id":90040,"post_type":"notice","notice_type":"notify","sub_type":"input_status","user_id":90051,"event_type":1,"status_text":"对方正在输入..."}`
	in, n, st, err := translate([]byte(frame))
	if err != nil || in != nil || n != nil || st == nil {
		t.Fatalf("%v %v %v %v", in, n, st, err)
	}
	if st.Kind != types.StatusTyping || st.Conv.Scene != types.ScenePrivate || st.Conv.ID == "" || st.Account != "90040" {
		t.Fatalf("%+v", st)
	}
}
