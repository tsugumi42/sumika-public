package freeapi

import (
	"math"
	"slices"
	"time"
)

// 状态机（规格-免费接口 第四节）。每个“提供方 + 路线”一份：正常 → 退避中 → 正常；旁路：429 暂停、需处理、手动关、本地额度用完。

// 进入退避的判据（LiteLLM 的“失败率加最少样本数”，加低流量接口用的连续失败）
const (
	backoffWindow   = 10 * time.Minute
	backoffMinN     = 5
	backoffFailRate = 0.5
	backoffStreak   = 3
	schemaToAttn    = 2 // 连续几次“格式变了”转需处理
	rateDefaultWait = 60 * time.Second
)

// State 是一个提供方现在的状态
type State string

const (
	StateOK        State = "ok"
	StateBackoff   State = "backoff"   // 退避中
	StatePaused    State = "paused"    // 对方说太频繁，暂停中
	StateAttention State = "attention" // 需处理
	StateOff       State = "off"       // 人关的
	StateQuota     State = "quota"     // 本地上限用完
)

// stateOf 算当前状态（不含本地额度，额度在 quotaLeft）。调用时持锁
func (e *Engine) stateOf(p Provider, route string, now time.Time) (State, time.Time) {
	if e.st.Manual[p.ID].Off {
		return StateOff, time.Time{}
	}
	ps := e.st.Providers[stKey(p.ID, route)]
	if ps == nil {
		return StateOK, time.Time{}
	}
	if ps.Attention != "" {
		return StateAttention, time.Time{}
	}
	if ps.OpenUntil > now.UnixMilli() {
		return StateBackoff, time.UnixMilli(ps.OpenUntil)
	}
	if ps.PausedUntil > now.UnixMilli() {
		return StatePaused, time.UnixMilli(ps.PausedUntil)
	}
	return StateOK, time.Time{}
}

// record 记一次尝试的结局。solo：这个区域下能用的只有它一个，不进退避。manual：人点的“立即检测”；probe 或 manual 的检测成功了都能解除“需处理”
func (e *Engine) record(p Provider, route string, k Kind, ms time.Duration, probe, solo, manual bool, retry time.Duration) {
	if k == KCanceled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	ps := e.pstate(p.ID, route)
	ps.LastCheck, ps.LastKind = now.UnixMilli(), k
	ps.Samples = append(ps.Samples, sample{T: now.UnixMilli(), K: k, MS: int32(ms / time.Millisecond), Probe: probe})
	if n := len(ps.Samples); n > maxSamples {
		ps.Samples = append([]sample(nil), ps.Samples[n-maxSamples:]...)
	}
	e.o.Logf("免费接口：%s %s %dms probe=%t", p.ID, k, ms/time.Millisecond, probe)
	switch k {
	case KOK, KNotFound:
		// 对方答了（查无结果也是答了），清掉故障计数
		ps.Streak, ps.SchemaStreak = 0, 0
		if ps.OpenUntil != 0 || ps.BackoffN != 0 {
			e.event(p.ID, "恢复正常")
		}
		ps.BackoffN, ps.OpenUntil = 0, 0
		if k == KOK {
			ps.LastOK = now.UnixMilli()
		}
		if ps.Attention != "" && (manual || probe) && k == KOK {
			ps.Attention = ""
			if manual {
				e.event(p.ID, "手动检测通过，解除需处理")
			} else {
				e.event(p.ID, "定期检测通过，解除需处理")
			}
		}
	case KRate:
		wait := retry
		if wait <= 0 {
			wait = rateDefaultWait
		}
		ps.PausedUntil = now.Add(wait).UnixMilli()
		e.event(p.ID, "对方说太频繁，暂停一会儿")
	case KAuth:
		if ps.Attention == "" {
			e.event(p.ID, "被拒绝（401/403），需要处理")
		}
		ps.Attention = KAuth
	case KSchema:
		ps.Streak++
		ps.SchemaStreak++
		if ps.SchemaStreak >= schemaToAttn && ps.Attention == "" {
			ps.Attention = KSchema
			e.event(p.ID, "返回内容连续不对，多半接口变了，需要处理")
		} else {
			e.maybeBackoff(p, ps, now, solo)
		}
	default: // 临时故障：timeout、net、5xx、cf、size
		ps.Streak++
		e.maybeBackoff(p, ps, now, solo)
	}
	e.save()
}

// maybeBackoff：最近 10 分钟样本够多且失败占比过半，或连续失败 3 次，就退避；只剩一个候选时不退避。调用时持锁
func (e *Engine) maybeBackoff(p Provider, ps *provState, now time.Time, solo bool) {
	if solo {
		return
	}
	n, fail := 0, 0
	cut := now.Add(-backoffWindow).UnixMilli()
	for _, s := range ps.Samples {
		if s.T < cut || s.K == KRate {
			continue
		}
		n++
		if !isOKKind(s.K) {
			fail++
		}
	}
	if !(ps.Streak >= backoffStreak || (n >= backoffMinN && float64(fail)/float64(n) > backoffFailRate)) {
		return
	}
	ps.BackoffN++
	d := e.o.Backoff(ps.BackoffN)
	ps.OpenUntil = now.Add(d).UnixMilli()
	e.event(p.ID, "连续出错，退避一会儿")
}

func isOKKind(k Kind) bool { return k == KOK || k == KNotFound }

// 成功率和延迟，样本取最近 24 小时
const statWindow = 24 * time.Hour

// successRate 是最近 24 小时指数衰减的成功率，向先验 0.8 收缩（样本少时不一次成功就满分、一次失败就垫底）；n 是有效样本数
func successRate(samples []sample, now time.Time) (rate float64, n int) {
	const prior, priorW, half = 0.8, 5.0, 6 * float64(time.Hour)
	var sw, ok float64
	for _, s := range samples {
		age := float64(now.UnixMilli()-s.T) * float64(time.Millisecond)
		if age < 0 {
			age = 0
		}
		if age > float64(statWindow) || s.K == KRate {
			continue
		}
		w := pow05(age / half)
		sw += w
		if isOKKind(s.K) {
			ok += w
		}
		n++
	}
	return (ok + prior*priorW) / (sw + priorW), n
}

func pow05(x float64) float64 { return math.Pow(0.5, x) }

// plainRate 是不加先验的 24 小时成功率，界面用；没有样本返回 -1
func plainRate(samples []sample, now time.Time) float64 {
	n, ok := 0, 0
	for _, s := range samples {
		if now.Sub(time.UnixMilli(s.T)) > statWindow || s.K == KRate {
			continue
		}
		n++
		if isOKKind(s.K) {
			ok++
		}
	}
	if n == 0 {
		return -1
	}
	return float64(ok) / float64(n)
}

// medianMS 是最近 24 小时成功样本的中位延迟；没有返回 0
func medianMS(samples []sample, now time.Time) int {
	var v []int
	for _, s := range samples {
		if s.K == KOK && now.Sub(time.UnixMilli(s.T)) <= statWindow {
			v = append(v, int(s.MS))
		}
	}
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}
