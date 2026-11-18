package app

import "syscall"

// errConnRefused 是 Windows 真实的"拒绝连接"（WSAECONNREFUSED，10061）。
// errors.Is 认不出它是 syscall.ECONNREFUSED，不单列的话 Bun 关了 Go 也起不来（2026-10-29 实测）。
const errConnRefused = syscall.Errno(10061)
