# E32R28T 手机配网、熄屏与安全 OTA

v0.3.0 是启用手机配网和 OTA 的首个版本。它必须先通过 USB 数据线烧录一次；设备
正在运行 v0.3.0 以后，后续版本才可以无线安装。手机配网页只在设备临时创建的
WPA2 热点上提供，不是云端管理后台，也不会暴露到设备连接的普通 Wi-Fi。

v0.3.1 在不改变原主 Wi-Fi 配置的前提下增加一组可选备用 Wi-Fi。每组网络最多尝试
12 秒；整轮失败后按 1、2、4 秒逐步退避，最长 60 秒。连接成功后，本次运行期间会
优先重试最后成功的网络；手动刷新会清除当前退避并立即开始新一轮连接。

同一版本还兼容 Claude Code 2.1.220 的文本型 `/usage` 输出，可解析当前会话和
`Current week (all models)` 周限额。额度主界面的布局及正常蓝/橙配色保持不变；
中文字体从 Regular 换为 Noto Sans CJK SC Medium 16px，并提高卡片和顶栏不透明度。
离线或过期数据改用灰色，避免与 Claude 的正常额度颜色混淆。

## 显示与触摸

默认行为如下：

- 正常亮度为 60%；60 秒没有触摸或按键后降到 10%。
- 300 秒没有操作后把 GPIO21 背光 PWM 设为 0。ESP32、触摸和 Wi-Fi 仍在运行，
  因而“熄屏”不是关机或深度睡眠。
- 熄屏时快照周期为正常刷新周期和 60 秒中的较大值。后台刷新不会点亮屏幕。
- 熄屏后的第一次触摸立即恢复原亮度、清除网络退避并请求刷新；同一次触摸释放
  不会再次触发。亮屏时任意短按请求刷新，1 秒内的重复操作会合并。
- 长按屏幕左侧显示网络诊断，长按右侧显示固件、IP 和数据源时间。
- BOOT 短按刷新，按住 1.2–5 秒显示诊断，持续 5 秒进入手机配网页。

配网或 OTA 期间强制保持正常亮度并暂停普通快照请求。结束后重新开始无操作计时。
固件不启用深度睡眠，因此 GPIO21 背光关闭只能降低屏幕功耗，不能达到整机关机电流。

以下设置写入 NVS，`0` 可以分别禁用自动降亮或熄屏：

```text
set brightness_percent 60
set dim_after_seconds 60
set screen_off_after_seconds 300
set screen_off_refresh_seconds 60
set ssid2 BackupWiFi
set password2 BackupPassword
save
```

`brightness_percent` 只接受 30、60 或 100。保存采用带提交标记的事务式写入；启动时
如果发现上次保存被断电打断，会恢复旧配置。`show` 始终遮蔽 Wi-Fi 密码和显示令牌。

## 打开临时配网页

没有有效 Wi-Fi/API 配置时，设备会自动进入配网模式。已有配置时，只能使用以下任一
本地操作打开：

1. 按住 BOOT 5 秒；或
2. 在 115200 baud 的 USB 串口输入 `portal`。

临时热点名为 `QMON-XXXXXX`。每次启动都会生成新的 12 位 WPA2 密码，并连同
`http://192.168.4.1` 显示在屏幕上。热点最多接受一个客户端；10 分钟没有网页操作
就自动关闭。普通 Wi-Fi 暂时断线不会自动开放热点。

手机连接热点后访问 `http://192.168.4.1`，可配置：

- 扫描或手工输入主 Wi-Fi，以及可选的备用 Wi-Fi 和各自密码；
- HTTPS 服务器地址、`display:read` 令牌、时区和快照刷新周期；
- 正常亮度、降亮时间、熄屏时间和熄屏刷新周期；
- 查看当前固件及服务器最新版本，并在人工确认后安装 OTA。

编辑已有配置时，页面不会返回密码或令牌；SSID 未改时，秘密输入框留空表示保留
旧密码；SSID 改动或清空时不会错误沿用之前网络的密码。主、备用 SSID 不能相同。
提交的新
配置先只放在内存中，依次测试 Wi-Fi（20 秒）、NTP（15 秒）及 HTTPS/TLS/令牌/
快照（10 秒）。全部成功才写入 NVS并重启；任何一步失败都会恢复旧连接和旧配置。
更改服务器地址或令牌会清除快照缓存及防重放时间戳，仅改 Wi-Fi、时区或亮度则保留。

配网服务只接受 SoftAP 子网请求，POST 请求体上限 2 KiB，并使用会话 CSRF、HTML
转义和安全响应头。密码、令牌和请求正文不会进入 URL、状态响应或串口日志。内置
接口固定为：

```text
GET  /
GET  /api/status
GET  /api/wifi
POST /api/config
GET  /api/config/status
GET  /api/ota/status
POST /api/ota/install
GET  /api/ota/progress
```

## 发布服务器固件

Standalone 必须带一个仅服务账户可读写的固件目录，例如：

```sh
install -d -m 700 "$HOME/.local/share/quota-monitor/firmware"
quota-monitor standalone \
  --listen=127.0.0.1:8787 \
  --firmware-dir="$HOME/.local/share/quota-monitor/firmware"
```

实际服务仍需保留原部署的数据库、显示令牌、Hook 密钥及 provider 参数。

生产服务器的系统级部署可使用 `/var/lib/quota-monitor/firmware`。固件只能由服务器
管理员在本机发布；项目没有公网上传接口。构建并通过容量门槛后执行：

```sh
quota-monitor firmware publish \
  --firmware-dir /var/lib/quota-monitor/firmware \
  --board e32r28t \
  --version 0.3.1 \
  --file firmware/.pio/build/e32r28t/firmware.bin
```

命令会验证严格三段式版本、板型、文件大小和 SHA-256，再以 `0600` 权限原子发布
版本文件和 manifest。发布不需要重启 quota-monitor 或 nginx。公网只提供两个需要
现有 `display:read` Bearer 令牌的 GET 接口：

```text
/api/v1/display/firmware/e32r28t/manifest
/api/v1/display/firmware/e32r28t/{version}.bin
```

固件文件由 Go 服务流式读取，不应由反向代理完整缓冲。nginx 示例见
`deploy/nginx-display-endpoint.conf.example`；Caddy 示例已经为 OTA 路径设置即时刷新
和 5 分钟读取超时。

## 设备安装与信任边界

进入配网页时设备才检查 manifest，永不自动安装。安装前必须在页面勾选“已连接稳定
USB 电源”；E32R28T 无法可靠检测 USB，这个确认不能代替实际接线检查。设备只接受：

- `board` 为 `e32r28t`，`schemaVersion` 为 1；
- 严格高于当前版本的三段式版本；
- 能放入非活动 OTA 分区且大小与 manifest 完全一致的文件；
- 64 位小写十六进制 SHA-256 与下载过程中计算结果一致的文件。

下载 URL 由设备使用当前 HTTPS 服务器同源构造，不采纳 manifest 外部 URL，也不跟随
重定向。TLS 继续使用固件内置受信根和域名校验，Bearer 令牌只提供读取权限。大小、
哈希、TLS 或写入任一失败时都会放弃非活动分区，继续运行原固件。

这个设计没有启用不可逆的 Secure Boot/eFuse，安全边界是受信 TLS、只读令牌和
SHA-256 完整性校验。若需要抵御服务器管理员或签发 CA 被攻破，应另行设计离线固件
签名和密钥轮换，不能把哈希本身当作发行者签名。

## 启动自检、回滚与 USB 恢复

OTA 新版本第一次启动有 30 秒本地自检期。显示、NVS、触摸和主循环保持稳定后才标记
当前分区有效；在此之前崩溃或重启，ESP32 双分区 Bootloader 会回滚到上一版本。
自检不以云服务器暂时可达为必要条件，避免网络故障触发无意义回滚。

如果升级后已自动回滚：

1. 不要重复安装同一文件；记录屏幕提示、串口启动日志和 manifest 元数据，敏感值脱敏。
2. 检查发布文件的大小、SHA-256、板型和版本，再修复后发布更高版本。
3. 设备仍可运行旧版本时，从临时配网页安装修复版本。

如果两个 OTA 分区都不能正常启动，使用 USB 数据线和 PlatformIO 重新烧录
`e32r28t`。常规 `pio run -e e32r28t -t upload` 不擦除 NVS；只有明确执行整片擦除或
写入从 `0x0` 开始的完整合并镜像才会清掉 Wi-Fi、令牌和缓存。烧录前应优先保留完整
4 MiB Flash 备份；备份包含密码和令牌，只能留在受保护的本机目录。
