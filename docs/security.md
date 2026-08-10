# Security notes

- Treat Codex `auth.json`, Claude Code credentials, agent tokens and display
  tokens as passwords. Never copy them into this repository or a support log.
- Use the Caddy TLS deployment or another trusted HTTPS reverse proxy. There is
  intentionally no insecure-TLS firmware option.
- Generate a separate agent token for every computer and revoke it when that
  computer is lost or retired. Use a separate display token for every device.
- The database contains token hashes, current quota observations and opaque
  task IDs. Back it up as sensitive operational data even though it contains no
  conversations.
- 完整 ESP32 Flash 备份、NVS 和服务器备份都可能含 Wi-Fi 密码、有效显示令牌或
  TLS 私钥。它们只能保存在 Git 忽略的受控目录或加密介质，不能作为 issue/日志附件。
- Review new Codex hooks with `/hooks`. The installer cannot and should not
  bypass Codex's trust prompt.
- Agent 上报、doctor 和本机 Hook 转发都拒绝 HTTP 30x，不会把 Bearer/Hook
  密钥带到重定向目标。服务端在访问 SQLite 前按令牌 SHA-256 指纹限速，并以全局
  上限和并发槽限制轮换假令牌攻击；`/healthz` 的数据库探测结果短暂缓存，避免公开
  健康检查变成 SQLite 放大器。日志只记录固定错误类别，不记录令牌、CLI stderr
  或数据库路径。
- 手机配网页只绑定设备临时 SoftAP，使用每次新生成的 12 位 WPA2 密码、单客户端
  限制和十分钟超时；STA 网络和云端不开放该页面。配置 POST 受 2 KiB 上限、会话
  CSRF 和输出转义保护，响应不会回填已有密码或令牌。
- OTA 没有公网上传接口。管理员只能在服务器本地运行 `firmware publish`；设备只从
  当前 HTTPS 同源读取 manifest 和固件，拒绝重定向、旧版本、错误板型、错误大小和
  SHA-256 不匹配。反向代理不应开放 firmware 目录或自动生成目录索引。
- OTA 的 SHA-256、TLS 和只读 Bearer 令牌可防传输损坏与非授权读取，但 SHA-256
  不是发行者签名。本版本没有启用不可逆 Secure Boot/eFuse；若威胁模型包含服务器
  管理员或受信 CA 被攻破，需要另行增加离线签名，而不是关闭现有校验。
- Use a protected LiPo pack with documented polarity. Never solder directly to
  a pouch-cell tab, puncture the pouch, charge it unattended, or enclose a
  swollen cell.
