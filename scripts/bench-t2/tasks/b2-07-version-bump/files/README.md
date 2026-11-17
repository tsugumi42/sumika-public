# Harbor 任务网关

![version](https://img.example.com/badge/version-1.4.2-blue)

Harbor 是一个由三个子模块组成的任务网关示例项目：

- modules/api：HTTP 接口
- modules/web：管理界面
- modules/worker：后台任务执行器

当前版本：1.4.2
最低兼容版本：1.4.2（低于它的客户端需要先升级；这个数字只在出现不兼容变更时才改，发新版本时不动）

## 快速开始

```
docker compose -f docker/compose.yml up -d
```

安装和升级的说明见 docs/。
