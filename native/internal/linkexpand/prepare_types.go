package linkexpand

import (
	"context"
	"errors"
	"strconv"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/taskrun"
)

var ErrVideoDisabled = errors.New("已关闭")
var ErrPlayback = errors.New("没有可用的匿名播放地址")
var ErrContainer = errors.New("媒体容器无效或不支持")
var ErrVideoCapability = errors.New("这种视频在这里发不出去")
var ErrGIFCapability = errors.New("这种动图在这里发不出去")
var ErrMediaTools = errors.New("本机缺少媒体转换或验证工具")
var errImageCapability = errors.New("这种图片在这里发不出去")

type VideoLimits struct {
	Enabled bool
	Timeout time.Duration
	Bytes   int64
}
type Loaded struct {
	Resource   *types.MediaResource
	Kind, Note string
	Received   int64
}

// quietMediaFailure 说这次媒体失败是不是我们自己的限制或本机的问题（大小上限、下载范围、本机工具、容器、没有播放地址）：
// 这类原因对群友没有意义，只进运行记录，消息里不写“X未下载：…”（用户 2026-11-12）。
// 平台那边的原因（需要登录、已删除）和“这里发不出去”这类能力缺失照旧写。
func quietMediaFailure(err error) bool {
	var fe *linkreader.FetchError
	if errors.As(err, &fe) && fe.Kind == "unsafe_target" {
		return true
	}
	return errors.Is(err, linkreader.ErrMediaLimit) || errors.Is(err, ErrMediaTools) || errors.Is(err, ErrContainer) || errors.Is(err, ErrPlayback)
}

func MediaReason(err error) string {
	var source *SourceError
	switch {
	case errors.As(err, &source):
		return source.Error()
	case errors.Is(err, ErrVideoDisabled):
		return "已关闭"
	case errors.Is(err, context.DeadlineExceeded):
		return "下载超时"
	case errors.Is(err, context.Canceled):
		return "已取消"
	case errors.Is(err, linkreader.ErrMediaLimit):
		return "超过大小上限"
	case errors.Is(err, ErrContainer):
		return "媒体容器无效或不支持"
	case errors.Is(err, ErrPlayback):
		return "没有可用的匿名播放地址"
	case errors.Is(err, ErrMediaTools):
		return ErrMediaTools.Error()
	case errors.Is(err, ErrVideoCapability):
		return ErrVideoCapability.Error()
	case errors.Is(err, ErrGIFCapability):
		return ErrGIFCapability.Error()
	}
	var fe *linkreader.FetchError
	if errors.As(err, &fe) {
		switch fe.Kind {
		case "not_found":
			return "图片地址已失效（HTTP " + statusText(fe.Status, "404") + "）"
		case "login_required":
			return "图片需要登录才能看（HTTP " + statusText(fe.Status, "403") + "）"
		case "rate_limited":
			return "图片站限流了，稍后再发一次链接试试"
		case "network":
			return "下载中断，或连不上图片站"
		case "unsafe_target":
			switch fe.Reason {
			case linkreader.ReasonFakeIPNoProxy:
				return "本机 DNS 给了 fake-ip 假地址，又没有代理可走，没有下载（linkReader.proxy 要配代理）"
			case linkreader.ReasonFakeIPNotListed:
				return "本机 DNS 给了 fake-ip 假地址，这个网站不在允许下载的名单里，没有下载"
			}
			return "地址不在允许下载的网站里，没有下载"
		}
		if fe.Status != 0 {
			return "图片站返回了错误（HTTP " + statusText(fe.Status, "") + "）"
		}
	}
	return "下载失败"
}

func statusText(status int, fallback string) string {
	if status > 0 {
		return strconv.Itoa(status)
	}
	return fallback
}

// MediaClass 给媒体退化定类别和结局（taskrun 的词表：tools_missing、container_invalid、capability、timeout、blocked，
// 认不出的是 unknown）。和 MediaReason 同一套判断，界面文字和运行记录不会各说各的
func MediaClass(err error) (class, outcome string) {
	var source *SourceError
	switch {
	case errors.As(err, &source):
		return sourceClass(source)
	case errors.Is(err, context.Canceled):
		return taskrun.ClassUnknown, taskrun.OutcomeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return taskrun.ClassTimeout, taskrun.OutcomeFailed
	case errors.Is(err, ErrMediaTools):
		return taskrun.ClassToolsMissing, taskrun.OutcomeFailed
	case errors.Is(err, ErrContainer):
		return taskrun.ClassContainerInvalid, taskrun.OutcomeFailed
	case errors.Is(err, ErrVideoCapability), errors.Is(err, ErrGIFCapability), errors.Is(err, errImageCapability):
		return taskrun.ClassCapability, taskrun.OutcomeSkipped
	case errors.Is(err, ErrVideoDisabled), errors.Is(err, linkreader.ErrMediaLimit):
		return taskrun.ClassBlocked, taskrun.OutcomeSkipped
	case errors.Is(err, ErrPlayback):
		return taskrun.ClassNotFound, taskrun.OutcomeFailed
	}
	var fe *linkreader.FetchError
	if errors.As(err, &fe) {
		switch fe.Kind {
		case "not_found":
			return taskrun.ClassNotFound, taskrun.OutcomeFailed
		case "login_required":
			return taskrun.ClassLoginRequired, taskrun.OutcomeFailed
		case "rate_limited":
			return taskrun.ClassRateLimited, taskrun.OutcomeFailed
		case "network":
			return taskrun.ClassNetwork, taskrun.OutcomeFailed
		case "unsafe_target":
			// fake-ip 下没有代理可走是本机配置问题，归网络类，运行页上能查到；别的 unsafe_target 照旧归未知
			if fe.Reason == linkreader.ReasonFakeIPNoProxy {
				return taskrun.ClassNetwork, taskrun.OutcomeFailed
			}
		}
	}
	return taskrun.ClassUnknown, taskrun.OutcomeFailed
}
