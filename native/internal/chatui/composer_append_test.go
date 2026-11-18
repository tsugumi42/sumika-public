package chatui

import "testing"

func TestComposerAppendTextJoinsAndRespectsSending(t *testing.T) {
	p := NewComposer()
	card := p.AddQuote(quoted("m1", "甲"), false)
	if p.AppendText(nil, "  ") || p.Main.Text != "" {
		t.Fatal("空白不该放进去")
	}
	if !p.AppendText(nil, "https://example.com/a") || p.Main.Text != "https://example.com/a" {
		t.Fatalf("空输入框直接放：%q", p.Main.Text)
	}
	p.AppendText(nil, "第二个")
	p.AppendText(p.Main, "a\nb")
	if p.Main.Text != "https://example.com/a 第二个\na\nb" {
		t.Fatalf("已有文字时补空格，多行补换行：%q", p.Main.Text)
	}
	if !p.AppendText(card, "给卡的") || card.Text != "给卡的" || p.Main.Text == "给卡的" {
		t.Fatal("指定了卡就放进卡")
	}
	card.Sending = true
	if p.AppendText(card, "不该进") || card.Text != "给卡的" || p.Notice == "" {
		t.Fatalf("发送中的块不收：%q %q", card.Text, p.Notice)
	}
}
