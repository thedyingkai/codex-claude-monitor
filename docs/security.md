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
- Review new Codex hooks with `/hooks`. The installer cannot and should not
  bypass Codex's trust prompt.
- Agent 上报、doctor 和本机 Hook 转发都拒绝 HTTP 30x，不会把 Bearer/Hook
  密钥带到重定向目标。服务端在访问 SQLite 前按令牌 SHA-256 指纹限速，并以全局
  上限和并发槽限制轮换假令牌攻击；`/healthz` 的数据库探测结果短暂缓存，避免公开
  健康检查变成 SQLite 放大器。日志只记录固定错误类别，不记录令牌、CLI stderr
  或数据库路径。
- Use a protected LiPo pack with documented polarity. Never solder directly to
  a pouch-cell tab, puncture the pouch, charge it unattended, or enclose a
  swollen cell.
