package whisper

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/store"
	"sumika-native/internal/whisperlog"
)

// whisperlogReader 把对话日志读成一行行“种类:状态”，测试里找字就够。
type whisperlogReader struct{ st *store.Store }

func (w *whisperlogReader) read(t *testing.T, source string) string {
	t.Helper()
	page, err := (&whisperlog.Reader{St: w.st}).Read(context.Background(), whisperlog.Request{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, e := range page.Entries {
		parts = append(parts, fmt.Sprintf("%s:%s", e.Kind, e.State))
	}
	return strings.Join(parts, " ")
}
