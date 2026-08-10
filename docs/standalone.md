# Standalone 云端部署

`quota-monitor standalone` 在一个进程中运行额度采集、Hooks 接收器、SQLite 和只读 API。Codex/Claude 的官方 CLI 也安装并登录在云服务器上，本地电脑不需要运行 Agent。

这种模式适合只关心账号额度，或者所有 Codex/Claude 任务都在云服务器上执行的情况。额度属于账号，云端登录同一账号后可以正常查询；任务事件来自本机 Hooks，因此只能统计云服务器上的任务。本地 Codex Desktop、本地 Codex CLI 或本地 Claude Code 的任务不会出现在计数中。需要多机任务统计时，改用 `server` + `agent` 模式。

## 服务器配置

Standalone 除了 Go 程序，还会启动官方 CLI，资源必须按整套进程计算：

- [Codex CLI 官方要求](https://github.com/openai/codex/blob/main/docs/install.md#system-requirements)至少 4GB 内存，推荐 8GB。
- [Claude Code 官方要求](https://code.claude.com/docs/en/setup#system-requirements)4GB 以上内存。
- 因此，官方支持范围内的 Standalone 配置至少应有 4GB；同时运行其他服务时还要留出余量。

Standalone 默认启用 `--sequential-collection=true`，每轮固定先查询 Codex，释放可重建的 `codex app-server` 子进程后再启动 Claude 探测。这样可以减少两个官方 CLI 的内存重叠，但不能降低任意一个 CLI 自身的峰值，也不能让低配机器进入官方支持范围。内存充足且更看重两个平台同时完成刷新时，可以显式改为 `--sequential-collection=false`。

1 核、512MB 的机器即使只跑额度探测也远低于两套 CLI 的官方最低要求，不能保证持续运行，并可能被 OOM Killer 终止。若仍要同时启用 Codex 和 Claude 做实验性试跑：

- 保持默认的串行采集，不要使用 `--sequential-collection=false`；
- 配置至少 1GB swap，并确认 swap 不与现有服务的磁盘负载冲突；
- 先以前台方式试运行，不要直接设为长期服务；
- 使用 `free -h`、服务日志和系统日志持续检查内存、swap 抖动与 OOM；
- 现有业务一旦出现内存压力，应关闭其中一个 provider 或升级内存。

增加 swap 只能降低突发内存不足的概率，不会让 512MB 或 1GB 机器进入官方支持范围，也不构成稳定性保证。

还需要：

- Linux amd64 或 arm64 云服务器；
- 一个解析到服务器的域名；
- 公网 TCP 80、443 可用；
- 官方 Codex CLI，以及需要时安装的官方 Claude Code CLI；
- 一个普通 Linux 用户，用于登录 CLI 和运行用户级服务。

不要用 root 登录 CLI 后再让普通用户启动服务。OAuth 凭据保存在用户目录中，运行服务的 UID 和 `HOME` 不一致时，Standalone 看不到登录状态。

项目原有的 scratch Docker 镜像只包含 Go 服务，不能运行 Codex/Claude CLI。Standalone 推荐直接安装到宿主机，Caddy 也在宿主机上反向代理 `127.0.0.1:8787`。

## 安装单文件程序

可在开发机运行 `scripts/build.ps1` 或 `scripts/build.sh`，然后把对应架构的文件上传到服务器。也可在服务器源码目录构建：

```sh
go test ./...
go build -trimpath -o quota-monitor ./cmd/quota-monitor
```

使用准备运行服务的普通用户安装：

```sh
install -d -m 700 \
  "$HOME/.local/bin" \
  "$HOME/.local/share/quota-monitor" \
  "$HOME/.local/share/quota-monitor/firmware" \
  "$HOME/.quota-monitor" \
  "$HOME/.config/systemd/user"
install -m 755 ./quota-monitor "$HOME/.local/bin/quota-monitor"
"$HOME/.local/bin/quota-monitor" version
```

确认官方 CLI 可以被同一用户找到：

```sh
command -v codex
command -v claude   # 未启用 Claude 时可忽略
```

nvm、asdf 等工具的路径通常不会自动进入 `systemd --user` 的 `PATH`。遇到这种安装方式，应记录 `command -v` 返回的绝对路径，并在 Standalone 的启动参数中填写 `--codex-command=/绝对路径/codex` 或 `--claude-command=/绝对路径/claude`。

## 登录 Codex 和 Claude

项目提供的登录命令只负责调用官方 CLI，不保存账号、密码或 OAuth Token：

```sh
"$HOME/.local/bin/quota-monitor" login codex
```

购买并准备启用 Claude 时再执行：

```sh
"$HOME/.local/bin/quota-monitor" login claude
```

登录过程可能要求在浏览器中确认设备码。完成后不需要把任何凭据复制到配置文件。将来登录失效时重新运行对应命令即可，常驻进程会在下一次采集时自动恢复。

## 安装 Hooks

在同一用户下执行：

```sh
"$HOME/.local/bin/quota-monitor" hooks install \
  --executable "$HOME/.local/bin/quota-monitor" \
  --secret-file "$HOME/.quota-monitor/hook-secret"
```

安装器会备份并合并现有 Codex/Claude 配置。重新启动 Codex 后，还需按 Codex 的官方信任流程检查并批准新 Hooks。Claude 已有的 status line 会继续执行。

Hooks 接收器只监听 `127.0.0.1:47632`，不需要在云防火墙开放这个端口。

## 首次前台运行

只有 Codex 时使用：

```sh
"$HOME/.local/bin/quota-monitor" standalone \
  --listen=127.0.0.1:8787 \
  --db="$HOME/.local/share/quota-monitor/monitor.db" \
  --display-token-file="$HOME/.local/share/quota-monitor/display.token" \
  --hook-secret-file="$HOME/.quota-monitor/hook-secret" \
  --firmware-dir="$HOME/.local/share/quota-monitor/firmware" \
  --claude=false \
  --sequential-collection=true \
  --log-format=text
```

启用 Claude 时删除 `--claude=false`，或改为 `--claude=true`。默认采集周期为 60 秒，任务状态每 15 秒写入 SQLite；Standalone 默认按 Codex → 释放 app-server → Claude 的顺序串行采集。不要在 512MB 共享主机上关闭串行模式。

首次启动会自动创建：

- `monitor.db`：SQLite 数据库；
- `display.token`：ESP32 使用的只读 Bearer 令牌；
- `hook-secret`：本机 Hooks 共享密钥。
- `firmware/`：管理员本地发布并由 OTA GET 接口流式读取的固件目录。

这些文件权限会限制为当前用户。`display.token` 中的 `qmon_...` 需要复制到 ESP32，但不要提交到 Git、截图或发到聊天记录。数据库和令牌文件必须配套保存；令牌文件与数据库不匹配时，程序会拒绝启动，而不是悄悄创建一个无法使用的新令牌。

日志出现 `standalone API listening` 后，可在另一个 SSH 会话检查：

```sh
curl --fail --show-error http://127.0.0.1:8787/healthz
```

正常响应为 `{"status":"ok"}`。此时按 `Ctrl+C` 停止前台进程，再配置常驻服务。

`--codex-plan=pro5`、`--codex-plan=pro20`、`--claude-plan=max5` 和 `--claude-plan=max20` 只用于补齐 CLI 没有明确返回的套餐细档。应按真实订阅填写，不能根据剩余额度猜测。

## 设置用户级 systemd 服务

仓库提供了 [服务示例](../deploy/quota-monitor-standalone.service.example)，默认按 Codex-only 启动：

```sh
cp deploy/quota-monitor-standalone.service.example \
  "$HOME/.config/systemd/user/quota-monitor-standalone.service"
systemctl --user daemon-reload
systemctl --user enable --now quota-monitor-standalone.service
systemctl --user status quota-monitor-standalone.service
journalctl --user -u quota-monitor-standalone.service -n 100 --no-pager
```

启用 Claude 前，先确认服务器处于至少 4GB 的官方支持配置并完成 Claude 登录，再编辑 unit，把 `--claude=false` 改为 `--claude=true`。修改后执行：

```sh
systemctl --user daemon-reload
systemctl --user restart quota-monitor-standalone.service
```

用户退出 SSH 后仍需保持运行时，由管理员执行：

```sh
sudo loginctl enable-linger 运行服务的用户名
```

不要改成 root 系统服务，否则它会使用另一套用户目录和登录状态。

## 配置宿主机 Caddy

安装 Caddy 后，复制 [宿主机配置示例](../deploy/Caddyfile.host.example)，并把第一行的 `monitor.example.com` 换成实际域名：

```sh
sudo install -m 644 deploy/Caddyfile.host.example /etc/caddy/Caddyfile
sudo editor /etc/caddy/Caddyfile
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Caddy 对公网开放 80/443，并把请求转发到回环地址 `127.0.0.1:8787`。云防火墙不要开放 8787 或 47632。域名 A/AAAA 记录、服务器时间和 80/443 入站规则都正确时，Caddy 会自动申请 HTTPS 证书。

验证公网健康接口：

```sh
curl --fail --show-error https://monitor.example.com/healthz
```

生产版 ESP32 固件必须使用受信任证书的 HTTPS，不能直接连接明文 8787。
如果改用 nginx，只公开健康检查、显示快照和两个固件 GET 路径，并让其他路径返回
404；可从 [nginx 示例](../deploy/nginx-display-endpoint.conf.example)开始配置。固件
下载必须关闭代理缓冲并设置足够的读取超时。

## 验证显示 API

从令牌文件临时读入当前 shell，避免把令牌直接写进命令历史：

```sh
DISPLAY_TOKEN=$(tr -d '\r\n' < "$HOME/.local/share/quota-monitor/display.token")
curl --fail --show-error \
  -H "Authorization: Bearer $DISPLAY_TOKEN" \
  https://monitor.example.com/api/v1/display/snapshot
unset DISPLAY_TOKEN
```

然后通过 ESP32 串口设置：

```text
set base_url https://monitor.example.com
set token qmon_REPLACE_WITH_DISPLAY_TOKEN
save
test
```

## 登录失效与自动恢复

Standalone 检测到未登录或凭据失效后，会在日志中给出固定提示。API 快照同时返回对应 provider 的 `loginRequired: true`，显示终端会提示重新登录。健康接口仍返回 200，因为进程和数据库本身可以继续服务。

在运行服务的同一用户下执行：

```sh
"$HOME/.local/bin/quota-monitor" login codex
# 或
"$HOME/.local/bin/quota-monitor" login claude
```

不需要重启 Standalone。默认情况下，登录状态会在 60 秒内重新采集。可用以下命令观察恢复过程：

```sh
journalctl --user -u quota-monitor-standalone.service -f
```

如果一直提示未登录，先用 `systemctl --user status` 确认 unit 的用户，再检查该用户的 `HOME` 和 CLI 绝对路径。交互式 SSH 能执行 CLI，不代表 systemd 的 PATH 一定能找到它。

## 任务计数

任务数依赖安装在云服务器用户目录中的 Hooks：

- `UserPromptSubmit` 到 `Stop`、`StopFailure` 或 `SessionEnd` 计为一个主任务；
- `SubagentStart` 到 `SubagentStop` 计为一个子任务；
- 普通后台 Shell 进程不计入。

如果 Codex/Claude 只在本地电脑使用，而本地不运行 Agent，云端任务数保持 0 是正常结果。Standalone 不会扫描远程会话，也没有账号级任务列表接口。

## 备份、发布固件与升级

SQLite 使用 WAL，不能在服务运行时只复制 `monitor.db`。使用 Python 标准库的 online
backup API 可以不停服务获得一致副本：

```sh
umask 077
BACKUP_DIR="$HOME/qmon-backups/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 700 "$BACKUP_DIR"
python3 - "$HOME/.local/share/quota-monitor/monitor.db" \
  "$BACKUP_DIR/monitor.db" <<'PY'
import sqlite3, sys
source = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
target = sqlite3.connect(sys.argv[2])
with target:
    source.backup(target)
result = target.execute("PRAGMA integrity_check").fetchone()[0]
source.close()
target.close()
if result != "ok":
    raise SystemExit(f"backup integrity_check failed: {result}")
PY
install -m 600 "$HOME/.local/share/quota-monitor/display.token" "$BACKUP_DIR/"
install -m 755 "$HOME/.local/bin/quota-monitor" "$BACKUP_DIR/"
install -m 600 "$HOME/.config/systemd/user/quota-monitor-standalone.service" "$BACKUP_DIR/"
(cd "$BACKUP_DIR" && sha256sum ./* > SHA256SUMS && sha256sum -c SHA256SUMS)
unset BACKUP_DIR
```

生产部署还应由管理员把当前 quota-monitor 专用反向代理配置及其证书/私钥复制到同一个
`0700` 备份目录，并重新生成 `SHA256SUMS`。不要复制 `$HOME/.codex`、
`$HOME/.claude` 或其他 OAuth 目录。备份包含可用显示令牌和 TLS 私钥，只能留在受控
服务器或加密离线介质，不能提交 Git、截图或发送到聊天。

发布固件前先构建 `e32r28t` 并确认镜像小于 1,966,080 字节且至少余留 128 KiB：

```sh
quota-monitor firmware publish \
  --firmware-dir "$HOME/.local/share/quota-monitor/firmware" \
  --board e32r28t --version 0.3.0 \
  --file firmware/.pio/build/e32r28t/firmware.bin
```

发布会原子替换 manifest，不需要重启服务或 reload 反向代理。设备只会在临时配网页
检查更新，并且必须人工确认安装。详情见[手机配网与安全 OTA](firmware-ota.md)。

升级服务器程序时先验证上述备份，再把新二进制安装到同目录的临时文件，校验版本后
原子 `mv` 覆盖，随后只重启 quota-monitor。先执行 `caddy validate` 或 `nginx -t`，
代理配置通过后只 reload 对应代理。不得停止、重启或 reload Hysteria 及其他无关服务。
替换后验证 `/healthz`、显示快照、firmware manifest 和原有业务服务状态；失败时恢复
旧二进制和原代理配置。若可执行文件路径发生变化，需要重新运行
`hooks install --executable ...`，让 Hook 配置指向新路径。

卸载 Hooks 使用：

```sh
"$HOME/.local/bin/quota-monitor" hooks uninstall
```

卸载只移除本项目写入的条目，不会删除官方 CLI 凭据。
