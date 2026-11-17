---
name: sumika
description: 用户问 Sumika 的功能、工具用法、审批、配置、渠道、项目说明文件、skill 或排错时，先读本说明书，再按主题读取附件。
---

# Sumika 说明书

用户问 Sumika 怎么用、能做什么、为什么没做成时，先读这里。本轮能实际使用什么，以当前工具和授权为准；功能状态、配置字段和默认值以当前程序附带的说明为准。

## 主题索引

- [工具](references/tools.md)：选哪个工具、短例子、截断续读。
- [审批](references/approval.md)：模式、受保护路径、授权和停止。
- [功能](references/features.md)：现在有的、现在没有的、正在接入的。
- [配置](references/configuration.md)：配置位置、生效时机和改错恢复。
- [渠道](references/channels.md)：本地私聊、本地群聊、QQ 私聊、QQ 群。
- [项目说明与 skill](references/project-context-and-skills.md)：发现、引用、预算、更新和续读。
- [排错](references/troubleshooting.md)：按现象找下一步。

## 读取顺序

先读与问题对应的一篇；涉及执行权限时再读审批，涉及听众或资料可见性时再读渠道。排错先定位现象，再按链接读主题。只把实际读到的正文当作已读资料。

## 开发接口索引

当前公开的是产品使用约定；编译内 Go 插件接口属于内部开发接口，随版本变化。`turn.Plugin.Setup` 经 `Registry` 注册 `OnContext`、`OnTool`、`OnSeen`、`OnSettled`、`OnBeforeCompact`；钩子只在宿主管理的边界运行，撤销注册不授予新权限。来源 resolver 在工具审批前冻结目标，不能让远端 annotations 生成本地政策。

渠道的 Interceptor 决定入站是否继续，Observer 观察已落库事件，Outbox/Outlet 管发送计划和实际交付；它们与模型工具是独立边界。子任务不能直接调用出口。候选 skill/插件必须留在候选目录，读取产物不代表允许启用。第三方 WASM 运行时尚未开放，不存在可供使用的 WASM ABI。
