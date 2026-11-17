package memory

import (
	"errors"
	"math"

	"sumika-native/internal/embed"
)

// 三处"够不够像"的比较（守卫 0.70、召回底线 0.6、近重复 0.92）和守卫给一条禁看条目打分的那段循环。
// 生产代码（outbound.go、recall.go）和评测工具（cmd/guard-eval、cmd/models-parity）都调这里，不各写一份：
// 比较的类型是故意不一样的，评测自己再写一份，量到的就不是线上的行为（规格-Go本地模型 第五节"翻转"一行，LESSONS"读同一门槛的程序没一起改"）。

// DupSimilarity：召回挑出来的候选里，和已选的某条点积不低于它的算近重复，不重复给（nearDuplicate）。
const DupSimilarity = 0.92

// GuardReaches：守卫的"够像"。得分本身是 float32（embed.Dot 的返回类型），门槛先转成 float32 再比：
// 0.70 转 float32 是 0.699999988…，按 float64 比的话，点积恰好等于 float32(0.70) 的得分会落在门槛下面一格而被放过。
func GuardReaches(score float32, threshold float64) bool { return score >= float32(threshold) }

// RecallReaches：召回的底线 SimilarityFloor。按 float64 比（召回的得分转成 float64 后还要参与名次和合并）。
func RecallReaches(score float32) bool { return float64(score) >= SimilarityFloor }

// NearDuplicateReaches：近重复 DupSimilarity。按 float64 比，同召回。
func NearDuplicateReaches(score float32) bool { return float64(score) >= DupSimilarity }

var (
	// ErrGuardDimension：查询句向量和条目的句向量维度对不上（换了模型而缓存没重算）。
	ErrGuardDimension = errors.New("守卫：向量维度对不上")
	// ErrGuardInvalid：点积是 NaN 或 Inf。NaN 与任何数比都是假，会被当成"不像"悄悄放行。
	ErrGuardInvalid = errors.New("守卫：点积不是有限数")
)

// GuardScore 是守卫给一条禁看条目打的分：每个查询句向量和这条条目的每个句向量做点积，取最大的（float32）；
// 没有可比的向量是 -1。向量都应该是单位向量。维度对不上、点积不是有限数就报错（checkOutbound 因此走"嵌入出问题"的退路，
// 不拿一个不可信的分数去比门槛）。
func GuardScore(queries, docs [][]float32) (float32, error) {
	score := float32(-1)
	for _, v := range queries {
		for _, doc := range docs {
			if len(v) != len(doc) {
				return 0, ErrGuardDimension
			}
			s := embed.Dot(v, doc)
			if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
				return 0, ErrGuardInvalid
			}
			score = max(score, s)
		}
	}
	return score, nil
}
