# tg bot

# 功能

## 下载频道文件到本地

```shell
tg_bot file download https://t.me/username/1234
```

## 上传文件到指定频道

```shell
tg_bot file upload 1.mp4 --caption="标题"
```

## 复制一个频道的内容到另一个频道

```shell
tg_bot group copy --from=1234 --to=5678
```

## 清理频道重复文件
```shell
tg_bot group dedupe 1000000001
```

#### 

Telegram 官方推荐缩略图控制在 320x320 像素以内