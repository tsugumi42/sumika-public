package ctlproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	const max = 8
	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"正常", "abc\n", "abc", nil},
		{"空行", "\n", "", nil},
		{"刚好上限", "12345678\n", "12345678", nil},
		{"超过上限一个字节", "123456789\n", "", ErrTooLong},
		{"没有换行就断了（在上限内）", "abc", "abc", nil},
		{"没有换行就断了（超上限）", "123456789", "", ErrTooLong},
		{"只有一个换行之后还有别的", "ab\ncd\n", "ab", nil},
		{"什么都没有", "", "", io.EOF},
	}
	for _, c := range cases {
		got, err := ReadLine(bufio.NewReaderSize(strings.NewReader(c.in), 16), max)
		if !errors.Is(err, c.err) || string(got) != c.want {
			t.Errorf("%s：%q → %q %v，应是 %q %v", c.name, c.in, got, err, c.want, c.err)
		}
	}
}

// 缓冲区比一行小得多时，长行靠多次读拼起来（不是每次只给 4096 字节）
func TestReadLineLongerThanBuffer(t *testing.T) {
	line := strings.Repeat("甲", 20000) // 60000 字节
	got, err := ReadLine(bufio.NewReaderSize(strings.NewReader(line+"\n"), 16), MaxRequestBytes)
	if err != nil || string(got) != line {
		t.Fatalf("长行没读全：%d 字节 %v", len(got), err)
	}
	if _, err := ReadLine(bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", MaxRequestBytes+1)+"\n"), 16), MaxRequestBytes); !errors.Is(err, ErrTooLong) {
		t.Fatalf("超过 64 KB 应报太长：%v", err)
	}
	if got, err := ReadLine(bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", MaxRequestBytes)+"\n"), 16), MaxRequestBytes); err != nil || len(got) != MaxRequestBytes {
		t.Fatalf("刚好 64 KB 应能读：%d %v", len(got), err)
	}
}

func TestWriteJSONIsOneLine(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, Reply{OK: true, Data: []byte(`{"a":"x\ny"}`)}); err != nil {
		t.Fatal(err)
	}
	if s := b.String(); strings.Count(s, "\n") != 1 || !strings.HasSuffix(s, "\n") {
		t.Fatalf("回复必须是以换行结尾的一行：%q", s)
	}
}
