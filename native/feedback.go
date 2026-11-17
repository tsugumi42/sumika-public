package main

import (
	"math"
	"sync"
	"testing"

	"github.com/egoist/mygo/audio"
	"github.com/tsugumi42/MewTypeUI/sound"
)

type feedbackCue struct {
	id                string
	from, to, seconds float64
	once              sync.Once
	prepared          *audio.Sound
}

// 声音资源归宿主，沿用原来的选中/打开/关闭音高、时长与 50% 音量。
// MewTypeUI 只判播放资格；不为几个反馈音继续链接旧组件的声音包。
var feedbackCues = [3]feedbackCue{
	{id: "select", from: 1046, to: 1046, seconds: .05},
	{id: "open", from: 500, to: 900, seconds: .09},
	{id: "close", from: 900, to: 500, seconds: .09},
}

const (
	feedbackSelect = iota
	feedbackOpen
	feedbackClose
)

func playFeedback(index int) {
	// 和原声音包一样，测试进程不打开音频设备。
	if testing.Testing() {
		return
	}
	cue := &feedbackCues[index]
	cue.once.Do(func() {
		const rate = 48000
		frames := int(cue.seconds * rate)
		samples := make([]float32, frames+1)
		phase := float64(0)
		for i := range frames {
			at := float64(i) / rate
			phase += 2 * math.Pi * (cue.from + (cue.to-cue.from)*at/cue.seconds) / rate
			envelope := math.Exp(-5*at/cue.seconds) * min(1, at/.003) * min(1, (cue.seconds-at)/.002)
			samples[i] = float32(.4 * envelope * math.Sin(phase))
		}
		cue.prepared = audio.NewSound(rate, 1, samples)
	})
	volume := float32(50)
	sound.Dispatch(sound.Request{CueID: cue.id, Resource: cue.prepared}, sound.Policy{Enabled: true, Eligible: true, VolumePercent: &volume}, sound.Options{
		OnPlay: func(request sound.Request) {
			request.Resource.(*audio.Sound).NewPlayer().SetVolume(float64(request.Gain)).Play()
		},
	})
}
