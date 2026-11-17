package types

import "time"

// 大图上传慢时放宽等待（用户 2026-11-13，规格-链接发送改版 B12 第 4 条）：十几 MB 的图（pixiv 漫画原图 15.8 MB，
// base64 以后二十多 MB）从 SnowLuma 传到 QQ 服务器要十几秒，固定的 10 秒调用时限、2 秒等回声会把“还在上传”判成被拒或没回包，
// 接着转 180° 重发，原来那条后来又到了，群里就有两份。下面三处都按体积放宽：适配器的调用时限（snowluma/call.go，按帧长）、
// 出口的兜底时限（channel/outlet_run.go，按段里媒体的字节数）、链接插件等回声的时长（linkplugin/echo.go，同上）。
// 小图不受影响：前 256 KiB 不放宽，超出的部分按字节线性算（所以文字、小图的时限一个字节都没变）。

const (
	// uploadPerMiB 是每 MiB 多等多久：按 0.5 MiB/s 的上行估（4 Mbps，比一般宽带慢，宁可多等）
	uploadPerMiB = 2 * time.Second
	// uploadFree 是不放宽的那一小段：文字、表情、几百 KB 的图不加时间
	uploadFree int64 = 256 << 10
	// MaxUploadAllowance 是放宽量的上限：再大的包等两分钟也该有结论（撤回的门槛 RecallWithin 也是两分钟）
	MaxUploadAllowance = 2 * time.Minute
	// BigMediaBytes 是“大图”的线：段里的媒体加起来到了它，等回声的时间按 EchoAllowance 放得更宽，
	// 回声迟到时要靠 linkplugin 的去重撤回保证群里只剩一份
	BigMediaBytes int64 = 2 << 20

	// bigEchoPerMiB 是大图等回声时每 MiB 多等多久。数据（2026-11-13 起 4 次大图没回声）：2～3 MiB 的图等了 6～8 秒都没回声，
	// 之后日志里也没有一条晚到的回声，说明被 QQ 吞掉的多、慢上传的少；所以只放宽到 30 秒上下，
	// 再长就是让用户白等（要走完阶梯，每一级都要等一轮），也占着链接插件的发送协程
	bigEchoPerMiB = 9 * time.Second
	// MaxEchoAllowance 是大图等回声的放宽量上限：加上默认的 2 秒基础值，等回声最长 30 秒
	MaxEchoAllowance = 28 * time.Second
)

// UploadAllowance 是发 n 字节要在基础时限上多等多久；不超过 256 KiB 是 0
func UploadAllowance(n int64) time.Duration {
	if n <= uploadFree {
		return 0
	}
	d := time.Duration(n-uploadFree) * uploadPerMiB / (1 << 20)
	return min(d, MaxUploadAllowance)
}

// EchoAllowance 是等回声要在基础时限上多等多久（只给链接插件等回声用，适配器和出口的调用时限仍用 UploadAllowance）。
// 小于 BigMediaBytes 的和 UploadAllowance 完全一样（小图的行为一个字节都没变）；
// 大图：前 256 KiB 不加，往上每 MiB 多等 9 秒，上限 28 秒（2 MiB 约 15.8 秒，3 MiB 约 24.8 秒，加基础值 2 秒是 17.8、26.8 秒）
func EchoAllowance(n int64) time.Duration {
	if n < BigMediaBytes {
		return UploadAllowance(n)
	}
	n = min(n, 64<<20) // 防乘法溢出；再大早已碰上限
	return min(time.Duration(n-uploadFree)*bigEchoPerMiB/(1<<20), MaxEchoAllowance)
}

// PartMediaBytes 是一段里跟着帧一起传的媒体（图片、动图，正文和转发节点里的）的字节数；没有备份的按它登记的大小算。
// 视频不算：它以本机文件地址交给协议端（snowluma/call.go），不在帧里，上传多久由协议端自己的流程管，
// 放宽它的时限会拖慢“视频没送到、单独补发”那套退路（规格-链接发送改版 A6）。
func PartMediaBytes(p Part) int64 {
	var n int64
	add := func(body []BodySegment) {
		for _, s := range body {
			if s.Media != nil && s.Kind != BodyVideo {
				n += s.Media.Size
			}
		}
	}
	add(p.Body)
	for _, node := range p.Nodes {
		add(node.Body)
	}
	return n
}
