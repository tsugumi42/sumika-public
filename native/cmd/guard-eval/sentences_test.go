package main

import (
	"context"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/embed"
)

// 整条记忆和第一句都与查询正交，只有单独送入的第二句匹配；漏切句会改变最终分数和决定。
type secondSentenceEmbed struct{}

func (secondSentenceEmbed) Model() string { return guardModel }
func (secondSentenceEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return secondSentenceEmbed{}.EmbedTyped(ctx, embed.TypeDocument, texts)
}
func (secondSentenceEmbed) EmbedTyped(_ context.Context, _ embed.Type, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if text == "备用钥匙收在蓝柜底层。" || text == "他把开门用的那把藏在柜子下面" {
			out[i] = []float32{1, 0}
		} else {
			out[i] = []float32{0, 1}
		}
	}
	return out, nil
}

func TestScoreSecondMemorySentence(t *testing.T) {
	ds := Dataset{Memories: []Memory{{ID: "m1", Kind: "fact", Status: "active", Scope: []string{"owner"}, Text: "阿枫养的猫怕雷声。备用钥匙收在蓝柜底层。"}}}
	emb := secondSentenceEmbed{}
	vectors, err := memoryVectors(context.Background(), options{}, ds, emb, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		aud   audience.Audience
		score float64
		block bool
	}{
		{"本地私聊", audience.Audience{Channel: "console", Conversation: "console:fake", Scene: "private", PrivateOK: true}, -1, false},
		{"本地群聊", audience.Audience{Channel: "console", Conversation: "console:group:1", Scene: "group"}, 1, true},
		{"QQ私聊", audience.Audience{Channel: "qq", Conversation: "qq:private:1", Scene: "private"}, 1, true},
		{"QQ群", audience.Audience{Channel: "qq", Conversation: "qq:group:1", Scene: "group"}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := Sample{Audience: tc.aud, Parts: []string{"他把开门用的那把藏在柜子下面"}}
			scores, err := scoreSample(context.Background(), emb, ds, sample, vectors)
			if err != nil {
				t.Fatal(err)
			}
			if len(scores) != 1 || scores[0].Literal || scores[0].Score != tc.score {
				t.Fatalf("第二句匹配的最终分数错误：%+v，要 %v", scores, tc.score)
			}
			d, err := decide(sample, "C", productionThreshold, func(string) (Evidence, error) {
				return Evidence{Literal: scores[0].Literal, Score: scores[0].Score, Yes: true, Asked: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if d.Blocked != tc.block {
				t.Fatalf("第二句匹配的拦截决定错误：%+v，要 %v", d, tc.block)
			}
		})
	}
}
