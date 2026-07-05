# squirrel_porter_bot

一个命令行 Telegram bot（基于 [urfave/cli v3](https://github.com/urfave/cli) + [gotgproto](https://github.com/celestix/gotgproto)），用于在频道之间搬运文件：上传本地文件、下载频道媒体、清理频道内重复文件。

## 构建

```shell
go build -o squirrel_porter_bot .
```

## 全局配置

所有命令共用以下全局参数，既可用命令行 flag，也可用环境变量传入。

| Flag | 别名 | 环境变量 | 说明 |
| --- | --- | --- | --- |
| `--api-id` | `-i` / `--id` | `SPB_API_ID` | Telegram API ID |
| `--api-hash` | `--hash` | `SPB_API_HASH` | Telegram API Hash |
| `--bot-token` | `-t` / `--token` | `SPB_BOT_TOKEN` | Bot Token（`--login bot` 时使用） |
| `--channel` | `-c` | `SPB_CHANNEL` | 目标频道用户名（不带 `@`） |
| `--login` | | `SPB_LOGIN` | 登录身份：`bot`（默认） 或 `user` |
| `--phone` | | `SPB_PHONE` | 用户账号登录手机号，如 `+8613800138000` |

> `--login user` 用手机号 + 验证码登录，能读取频道历史（`dedup` 必需，bot 无此权限）。首次运行会在终端提示输入验证码 / 两步验证密码，之后 session 会被缓存，免重复登录。

推荐把常用配置写进环境变量，命令行只写业务参数：

```shell
export SPB_API_ID=123456
export SPB_API_HASH=xxxxxxxxxxxxxxxx
export SPB_BOT_TOKEN=xxxxxxxx:yyyyyyyy
export SPB_CHANNEL=my_channel
```

## 命令

### file upload — 上传本地文件到频道

别名：`file u` / `file up`（`file` 亦可写 `f`）

```shell
squirrel_porter_bot file upload <file_path...>
```

- 位置参数 `<file_path...>`：一个或多个文件路径，支持通配符 `*` `?` `[]`（会自动展开、跳过目录）。
- 单文件：`.mp4` 按视频发送（自动通过 `ffprobe` 补全宽/高/时长，同名 `.jpg` 作为封面缩略图），其它按图片发送。
- 多文件：默认合并为相册（每 10 个一批），加 `--separate` 则逐个作为独立消息发送。

| Flag | 别名 | 默认 | 说明 |
| --- | --- | --- | --- |
| `--caption` | `-c` | | 媒体说明文字（相册只显示在第一条） |
| `--separate` | `-s` | `false` | 逐个发送，每个文件独立成一条消息 |
| `--threads` | `-t` | `4` | 上传并发 goroutine 数 |

示例：

```shell
# 上传单个视频，带标题（同目录下 1.jpg 会作为封面）
squirrel_porter_bot file upload 1.mp4 --caption="标题"

# 通配符批量上传，合并成相册
squirrel_porter_bot file upload "photos/*.jpg" -c "相册标题"

# 批量上传，每个文件独立成一条消息
squirrel_porter_bot file upload *.mp4 --separate
```

> 提示：Telegram 官方推荐缩略图（封面 `.jpg`）控制在 320x320 像素以内。视频属性依赖系统已安装 `ffprobe`（FFmpeg）。

### file download — 下载频道媒体到本地

别名：`file d`

```shell
squirrel_porter_bot file download <file_url>
```

- 位置参数 `<file_url>`：公开消息链接，如 `https://t.me/username/1234`。
- 文件名自动生成：优先用消息文字（caption），并从附件推断后缀；缺失时退回 `file_<msgId>`。

| Flag | 别名 | 默认 | 说明 |
| --- | --- | --- | --- |
| `--output` | `-o` | 当前目录 | 输出目录（不存在会自动创建） |
| `--threads` | `-t` | `4` | 下载并发 goroutine 数 |
| `--part-size` | `-p` | `1048576`（1 MiB） | 分片大小，须为 4KB 的整数倍，最大 1 MiB |

示例：

```shell
# 下载到当前目录
squirrel_porter_bot file download https://t.me/username/1234

# 指定输出目录与并发数
squirrel_porter_bot file download https://t.me/username/1234 -o ./downloads -t 8
```

### dedup — 清理频道内重复文件

别名：`dd`。**需要 `--login user` 登录**（bot 无法读取频道历史）。

```shell
squirrel_porter_bot --login user --phone +8613800138000 -c <channel> dedup
```

遍历频道全部历史，按「真实文件名 + 大小」（图片按最大尺寸字节数 + 宽×高）判重。每组重复保留消息 ID 最小（最早）的一条，其余标记为待删除。**默认是预览（dry-run），只打印不删除**，确认后加 `--confirm` 才真正删除。

| Flag | 别名 | 默认 | 说明 |
| --- | --- | --- | --- |
| `--confirm` | `-y` | `false` | 实际执行删除（不加则仅预览） |

示例：

```shell
# 1. 先预览将要删除哪些重复文件（安全，不改动频道）
squirrel_porter_bot --login user -c my_channel dedup

# 2. 确认无误后执行删除
squirrel_porter_bot --login user -c my_channel dedup --confirm
```

## 帮助

任意层级都可用 `--help` 查看可用命令与参数：

```shell
squirrel_porter_bot --help
squirrel_porter_bot file --help
squirrel_porter_bot file upload --help
```
