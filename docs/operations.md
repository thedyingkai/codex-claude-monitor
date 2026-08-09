# 运行维护与故障排查

## 日常检查

Standalone 部署先检查用户级服务和日志：

```sh
systemctl --user status quota-monitor-standalone.service
journalctl --user -u quota-monitor-standalone.service --since '15 minutes ago' --no-pager
curl --fail --show-error http://127.0.0.1:8787/healthz
```

建议按以下顺序判断故障属于服务、采集器、兼容 Agent 还是显示端：

1. `GET /healthz` 是否返回 200；
2. 带显示令牌读取 `/api/v1/display/snapshot` 是否返回 200；
3. `agents.online`、各 provider 的 `freshness`/`observedAt` 和 `warnings` 是否合理；
4. Standalone 日志是否持续采集，或兼容 Agent 是否持续上报；
5. 在同一用户下直接检查 `codex login status`、`claude auth status --json`；
6. 最后检查 ESP32 的 Wi-Fi、DNS、时间同步和 TLS。

兼容 Docker server + Agent 部署的服务端检查：

```sh
quota-monitor doctor --health-url https://monitor.example.com/healthz
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --since=15m monitor caddy
```

`doctor` 目前只验证健康接口及数据库可用性。它不验证显示令牌、Agent 心跳、CLI 登录或配额字段。

## 快照语义

- `fresh`：服务器选择到不超过 5 分钟的最新有效样本。
- `stale`：有历史额度，但最新 `observedAt` 已超过 5 分钟。
- `unavailable`：尚无可用额度样本；此时 `observedAt` 可以省略，两个窗口均为
  `null`，显示端应显示 `N/A`，不能把缺失时间解释成公元 1 年或拒绝整份快照；
  例如未登录、采集解析失败或账号没有返回窗口。
- `loginRequired: true`：对应 CLI 未登录或凭据失效；在运行服务的同一用户下执行
  `quota-monitor login codex` 或 `quota-monitor login claude`，无需重启 Standalone。
- `agents.online`：最近 45 秒内收到完整报告的 Agent 数；`agents.total` 包括离线 Agent。
- 任务只统计在线 Agent 且 `lastSeenAt` 不超过 15 分钟的记录。
- 5h/7d 窗口不存在时为 JSON `null`，不是 0%；0% 与 100% 都是有效值。

多机使用同一账号时，服务器按 provider 选择 `observedAt` 最新的额度，绝不累加。任务以 `agentId + provider + sessionId` 区分；所以每台电脑必须使用唯一且稳定的 `agentId`。

## Agent 运维

Windows：

```powershell
schtasks.exe /Query /TN "Quota Monitor Agent" /V /FO LIST
schtasks.exe /End /TN "Quota Monitor Agent"
schtasks.exe /Run /TN "Quota Monitor Agent"
```

Linux：

```sh
systemctl --user status quota-monitor-agent.service
systemctl --user restart quota-monitor-agent.service
journalctl --user -u quota-monitor-agent.service --since '15 minutes ago' --no-pager
```

Agent 启动时立即采集，正常情况下每 60 秒重新采集一次、每 15 秒上报一次。上报失败按指数退避，最多退避到配置的 `maxBackoff`。每次报告是完整状态替换，不是增量事件。

修改 Agent JSON 后需重启常驻服务。JSON 使用严格字段解析：拼错或加入未知字段会导致启动失败。`tokenFile` 与 `hookSecretFile` 可以是绝对路径、相对配置文件的路径，或以 `~/` 开头的用户路径；令牌文件不能为空。

`allowInsecureHttp` 只允许 `http://localhost`、回环 IPv4 或 `::1`，即使设为
`true` 也会拒绝局域网地址和公网域名。它只用于本机开发，不是“忽略 TLS 错误”
开关；程序没有跳过证书校验的选项。

## Hooks 与任务计数

Hooks 配置位置：

- Codex：`~/.codex/hooks.json`
- Claude：`~/.claude/settings.json`
- 安装状态：`~/.quota-monitor/hooks-state.json`
- 本机 Hook 共享密钥：`~/.quota-monitor/hook-secret`

安装器在修改已存在的 JSON 前，会在同目录写入 `原文件名.quota-monitor.<UTC时间>.bak`。重复安装会先过滤旧的本项目条目，再写入一组新条目，不应复制累积。卸载只删除带 `quota-monitor-managed-v1` 标记的命令，并在条件允许时恢复原 Claude status line。

安全卸载顺序：

```text
1. quota-monitor hooks uninstall
2. 重启 Codex 和 Claude Code，确认原配置仍在
3. 停止并删除 Agent 自启项
4. 需要时在服务器撤销该 Agent 令牌
```

Windows 删除自启项：

```powershell
schtasks.exe /Delete /TN "Quota Monitor Agent" /F
```

Linux 删除用户服务：

```sh
systemctl --user disable --now quota-monitor-agent.service
rm "$HOME/.config/systemd/user/quota-monitor-agent.service"
systemctl --user daemon-reload
```

删除操作不等于撤销服务器令牌，设备退役时必须完成两者。

### 任务数一直为 0

- 确认常驻 Agent 正在运行；`agent --once` 不启动长期 Hook 接收器。
- 确认本机没有其他程序占用 `127.0.0.1:47632`。
- 安装 Hooks 后重启 CLI；Codex 还需在 `/hooks` 中人工检查和信任。
- 比较 Agent JSON 的 `hookSecretFile` 与安装器输出的 `secretPath`，两者必须指向同一文件。
- 检查 hooks JSON 中的可执行程序路径仍然存在；升级时若移动了二进制，应重新安装 Hooks。
- 配额探测进程带专用环境标记并被排除，不应被计为任务。

任务定义是 `UserPromptSubmit` 到 `Stop`、`StopFailure` 或 `SessionEnd` 的主任务，以及 `SubagentStart` 到 `SubagentStop` 的子任务。Codex 与 Claude 支持的具体 Hook 事件并不完全相同；安装器只为各自支持的事件写入条目。

## 令牌生命周期

列出数据库中的令牌元数据（不会显示原始令牌）：

```sh
quota-monitor token list --db /path/to/monitor.db
```

轮换令牌应先创建、切换、确认，再撤销旧令牌：

1. 使用相同 `agentId` 创建新的 `agent:write` 令牌，或新建 `display:read` 令牌；
2. 更新目标设备的令牌文件/NVS；
3. 重启 Agent 或在屏幕串口执行 `save`、`test`；
4. 确认新令牌成功使用；
5. 用 `token list` 找到旧 Token ID，再执行 `token revoke --id <ID>`。

Docker 中的示例：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec monitor \
  /app/quota-monitor token list --db /data/monitor.db
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec monitor \
  /app/quota-monitor token revoke --db /data/monitor.db --id tok_REPLACE_ME
```

撤销立即生效。原始令牌无法从数据库恢复，因为服务只保存 SHA-256 摘要。

## SQLite 备份与恢复

数据库保存令牌摘要和当前状态，仍应视为敏感运维数据。SQLite 使用 WAL；不要在运行时只复制单个 `monitor.db` 文件。最简单可靠的 Docker 冷备份流程是先停止 `monitor`，再完整复制数据卷内容，最后重新启动。停止期间 Caddy 会返回上游错误。

先用 `docker volume ls` 查明 Compose 创建的数据卷名称，再在一个专用备份目录执行：

```sh
mkdir -p ./backups
docker compose --env-file deploy/.env -f deploy/docker-compose.yml stop monitor
docker run --rm \
  -v YOUR_MONITOR_DATA_VOLUME:/source:ro \
  -v "$PWD/backups:/backup" \
  alpine:3.22 tar -C /source -czf /backup/monitor-data.tgz .
docker compose --env-file deploy/.env -f deploy/docker-compose.yml start monitor
```

恢复前应先停止服务，把现有数据卷另做一份保留副本，再将归档解压进准确的数据卷。恢复数据库也会恢复当时的令牌状态；备份后撤销的令牌可能重新变为有效，恢复后应立即审计并轮换令牌。

如果不需要保留令牌，数据库也可以从空目录重建；随后必须重新签发所有 Agent/显示令牌。

## 升级与回滚

升级前备份 SQLite 和用户 Hooks 配置。Go 服务与 Agent 应使用同一个 `schemaVersion: 1` 契约版本。

Docker 服务：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml build --pull monitor
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d monitor caddy
curl --fail https://monitor.example.com/healthz
```

Agent 二进制应在停止服务后替换，再重新启动。若二进制路径改变，运行 `hooks install --executable <新绝对路径>` 以更新 Hooks。回滚时恢复旧二进制；只有数据库格式不兼容时才恢复数据库备份。

ESP32 升级固件前记录 `show` 输出（其中密码和令牌已脱敏）。常规刷写不应清除 NVS，但仍需准备显示令牌以便重新配置。

## 常见故障

### HTTP 401 / 403

- 401：令牌缺失、无效、已撤销或作用域错误。检查是否误把 Agent 令牌用于显示 API。
- 403 `agent_mismatch`：`agent:write` 令牌绑定的 Agent ID 与 JSON 中 `agentId` 不一致。
- 令牌文件可包含末尾换行，程序会去除首尾空白；中间字符不能变化。

### HTTP 409 `stale_report`

服务器已经保存同一 Agent 更新的 `sentAt`。常见原因是系统时间回拨、同时运行两个相同 `agentId` 的实例，或重放旧报告。停止重复实例并修正 NTP；不要通过清库规避时间问题。

### HTTP 413 / 429

- 413：Agent 报告超过 64 KiB。检查异常多的任务记录或非官方客户端，不要提高公网限制来掩盖故障。
- 429：同一令牌超过默认 120 次/分钟；响应带 `Retry-After`。默认 15 秒刷新不会触发该限制。

### Codex 为 `unavailable`

- 在 Agent 用户下运行 `codex login status`。
- 确认 `codex app-server --stdio` 可以启动且版本与当前实现兼容。
- 查看 Agent 日志中的 `account/read` 或 `account/rateLimits/read` 错误。
- API 只按 300 分钟和 10080 分钟识别 5h/7d；未知时长会保持 `null`。

### Claude 为 `unavailable`

- 运行 `claude auth status --json`。Claude 未登录时该命令在部分版本会返回非零退出码，但仍输出 JSON；采集器会优先解析 JSON。
- 手工执行 `claude -p "/usage" --output-format stream-json --verbose --no-session-persistence`，确认是 slash command 输出且包含配额字段。
- 若 `/usage` 字段改变，statusLine 的结构化 `five_hour`/`seven_day` 可作为回退；确保 Claude 设置仍指向包装器。
- 不要改用普通提示词探测，否则会产生不必要的模型调用。

### ESP32 TLS/DNS/API 失败

- `show` 检查 SSID、域名、时区、刷新周期；敏感值应显示为遮蔽形式。
- `base_url` 必须以 `https://` 开头，证书域名必须匹配，实际证书链需锚定固件内置的 ISRG Root X1。若 Caddy 改用其他 ACME CA，浏览器可用并不代表当前固件会信任该证书链。
- 检查 Wi-Fi 是否能解析域名、NTP 是否可达、服务器时间与设备时间是否准确。
- HTTP 401 时重新写入只读显示令牌；不要把写入令牌放进屏幕。
- 修复后执行 `test`；断网期间设备保留缓存并指数退避，恢复网络后自动继续。

### Caddy 无法签发证书

- 域名 A/AAAA 必须指向当前服务器；错误的 AAAA 记录也会导致验证失败。
- TCP 80/443 必须从公网到达 Caddy，且没有另一个进程占用。
- Caddy 容器必须能够访问公网 ACME 服务和 DNS；检查 Compose 网络与云出口策略。
- 查看 `docker compose ... logs caddy` 的具体 ACME 错误，不要关闭 TLS 校验作为解决办法。

### `monitor` 报 `/data/monitor.db` 权限错误

运行镜像使用非 root 用户。检查 named volume 中 `/data` 是否允许镜像用户写入，并在镜像/卷初始化阶段修正所有权；不要用 `chmod 777` 或改成 root 容器来长期运行。修正后重新启动并确认 SQLite 能创建 WAL 文件。

## 安全基线

- Go 服务只在 Compose 私有网络的 `:8787` 监听；公网只开放 Caddy 的 80/443。
- 不要在日志、工单、截图或 shell 命令行中暴露令牌；排障样本必须删除账号和凭据。
- 每台 Agent/显示器使用独立、最小作用域令牌，并定期审计 `lastUsedAt`。
- 不要把 Codex/Claude 凭据复制到云端数据库或 ESP32。
- 对服务器和 Agent 主机启用系统时间同步，限制 SQLite 与令牌文件的读取权限。
- 显示终端丢失后撤销其令牌；NVS 中保存的是可用的只读凭据。
- 项目没有管理网页、CORS 或远程令牌管理；不要通过反向代理自行开放本地管理命令。
