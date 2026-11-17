# 安装

## 用 Docker

```
docker pull example.com/harbor/api:1.4.2
docker pull example.com/harbor/web:1.4.2
docker pull example.com/harbor/worker:1.4.2
```

## 从发布包安装

```
curl -O https://example.com/harbor/releases/v1.4.2/harbor-1.4.2-linux-amd64.tar.gz
tar xzf harbor-1.4.2-linux-amd64.tar.gz
```

装好以后用 `harbor-worker --version` 确认版本。
