<p align="left"><a href="README.md">English</a> · <strong>简体中文</strong> · <a href="README_ZH_TW.md">繁體中文</a> · <a href="README_KO.md">한국어</a> · <a href="README_JA.md">日本語</a></p>

<p align="center">
  <img src="resources/icon.png" alt="SSHYard 图标" width="128" height="128">
</p>

<h1 align="center">SSHYard</h1>

SSHYard 是一款原生桌面 SSH / SFTP 客户端，支持 macOS、Windows 和 Linux，将终端、文件管理与 AI 助手集于一体。

## 截图

<table>
  <tr>
    <td width="50%" align="center">
      <img src="docs/terminal-ai-assistant.webp" alt="终端与 AI 助手" width="100%"><br>
      终端与 AI 助手
    </td>
    <td width="50%" align="center">
      <img src="docs/sftp-file-manager.webp" alt="SFTP 文件管理" width="100%"><br>
      SFTP 文件管理
    </td>
  </tr>
  <tr>
    <td width="50%" align="center">
      <img src="docs/system-monitor.webp" alt="系统监控" width="100%"><br>
      系统监控
    </td>
    <td width="50%" align="center">
      <img src="docs/ai-assistant-settings.webp" alt="AI 助手设置" width="100%"><br>
      AI 助手设置
    </td>
  </tr>
</table>

## 功能

- **终端**
  - 多标签、分屏与终端搜索，标签可拖动排序和重命名
  - 真彩色、宽字符、输入法，以及 tmux、vim、htop 鼠标操作
  - 十套内置配色方案并可导入 `.itermcolors`，支持关键字高亮、行号与时间戳，可调光标、行高和字宽
  - 多种输出编码，可按主机设置；⌘ / Ctrl + 点击打开终端中的网址，⌥ / Alt + 拖动进行矩形块选择
  - 除 SSH 外还支持 Telnet、串口和本地终端（Windows 可选 PowerShell、CMD、Git Bash 与 WSL）
  - 同一主机的多个面板共享连接，断线自动重连，网络切换或从睡眠唤醒后立即重连并回到原来的文件夹

- **工作区**
  - 系统监控、进程占用、主机详情、Docker、tmux、快捷指令、历史指令、广播指令和会话日志都是可拖拽窗格，可停靠到任意窗格旁或其他标签页
  - tmux 会话管理，断线重连后回到原会话
  - 查看 Docker 容器与镜像的详情和资源占用，并直接启停容器
  - 广播指令与同步输入，会话日志可按文本或原始格式记录
  - 命令面板与可自定义的快捷键，可记忆窗口与分屏布局

- **文件管理**
  - SFTP 与终端并排，支持跟随终端目录，服务器没有 SFTP 时自动改用 SCP
  - 多标签内置编辑器，带目录树
  - 可通过 sudo 提权访问文件
  - 支持文件拖放和文件夹传输，上传保留权限和修改时间
  - 通过拖放或复制粘贴，在两台服务器之间直接复制文件和文件夹，数据经应用内存中转，不落本地磁盘
  - 传输队列支持暂停、续传和重试，传输期间保持电脑唤醒

- **AI 助手**
  - 每台主机独立的助手面板，支持执行命令、编辑文件，SSH、Telnet、串口和本地终端均可使用，对话按主机保存
  - 接入 AI 服务商，或使用自动检测到的本机 ACP 智能体（Claude Code、Codex、Grok、Gemini CLI 等），可同时添加多个，并选择各自支持的模型与思考等级；操作始终在远程主机上进行
  - 支持粘贴图片和文件作为附件
  - 询问、自动审核、完全允许三种权限模式，可配置审核模型、单轮工具调用上限和请求超时

- **智能体接入**
  - 通过开放 API、MCP 和 Skill 接入本机 AI 智能体
  - 查看主机、建立连接和执行命令，无需向智能体暴露已保存的凭据
  - 支持为 Claude Code、Codex 一键配置 Skill，接入功能默认关闭

- **主机与凭据**
  - 从 SSH 配置导入，从 Xshell、MobaXterm、PuTTY 等十种客户端迁移，或粘贴文本批量导入；也可通过 `user@host:port` 快速连接
  - 支持密码、OpenSSH / PEM / PuTTY 密钥、OpenSSH 证书和 SSH Agent
  - 分组不限层级，可拖动整理，支持多选与测速
  - 记录服务商、续费周期和备注，续费前发送提醒
  - 凭据本地加密保存，支持生成密钥和安装公钥；主机密钥变化时核对指纹，并可管理已知主机

- **备份与恢复**
  - 密码加密备份主机、凭据、快捷指令和设置，支持跨设备恢复
  - 保存到本地、WebDAV、S3 兼容存储或 GitHub 私密 Gist
  - 支持手动与定时备份，可设置保留份数

- **网络连接**
  - 多级跳板机与 HTTP / SOCKS5 代理，AI 服务商和更新检查也可使用代理
  - 本地、远程和动态端口转发

- **日常体验**
  - 服务器状态监控
  - 浅色与深色主题，支持背景图、半透明和界面缩放
  - 可关闭到托盘，从托盘弹窗快速连接主机
  - 支持英文、简体中文、繁体中文、韩文和日文

## Linux 凭据与依赖

请以普通桌面用户运行 SSHYard，无需 root 或 `sudo`，保存和解锁凭据需要 libsecret 0.19 或更新版本（`libsecret-1.so.0`）、当前用户会话的 D-Bus，以及可用且已解锁的 [Secret Service](https://specifications.freedesktop.org/secret-service/latest/) 服务，例如桌面环境已配置的 GNOME Keyring 或其他兼容服务，仅安装 libsecret 不会提供运行中的 Secret Service；系统服务保存 SSHYard 的主密钥，凭据仍在本机 `secrets.enc` 中加密保存

- **Debian / Ubuntu：** `.deb` 在 GTK 之外声明 `libsecret-1-0 (>= 0.19)`，请使用能够处理依赖的包管理器安装，例如 `sudo apt install ./sshyard_<version>_amd64.deb`，文件名以实际下载为准
- **Arch Linux：** 生成的 `PKGBUILD` 声明 `libsecret` 和 `gtk3`，MyGo 的 Debian 到 Arch 映射会移除版本约束，请确保 libsecret 版本至少为 0.19
- **压缩包 / `install.sh`：** 按用户安装到 `~/.local`，不会自动安装 libsecret、GTK 或 Secret Service 服务，请先备齐依赖，不要以 root 运行脚本

系统缺少软件包，或租户策略禁用会话 D-Bus / Secret Service 时，可能需要管理员处理，需要解锁集合时请使用桌面环境现有的密钥环管理器；SSHYard 使用原生界面，无需 WebKitGTK

没有可用的 Secret Service 时，例如没有管理员权限的租户账号，SSHYard 可改用本地主密码保护主密钥：解锁失败且尚未保存任何凭据时，在错误对话框中选择**改用本地主密码…**；主密钥随后以该密码（Argon2id）加密保存在 `secrets.enc` 旁的 `master.enc` 中，不写入系统钥匙串，每次启动后首次使用已保存的机密内容时输入一次；SSHYard 不保存此密码，忘记后已保存的凭据只能从备份找回。之后可在**设置 → 关于 → 机密存储**中修改密码，或在系统钥匙串可用后改回系统钥匙串；同一对话框中的**复制诊断信息**会复制凭据服务自身的错误消息，便于反馈问题，其中不含机密内容

可在与 SSHYard **相同的桌面登录会话**中打开终端，不加 `sudo` 执行以下只读连通性检查（需要 `gdbus`）：

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /org/freedesktop/secrets \
  --method org.freedesktop.Secret.Service.ReadAlias default
```

返回集合路径说明服务可达，返回 `/` 表示没有默认集合；若返回了集合路径，可将下面的 `/returned/collection/path` 替换为该路径，查看锁定状态：

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /returned/collection/path \
  --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked
```

`true` 表示已锁定，`false` 表示已解锁；这些调用可能激活会话服务，但不会读取密码、解锁集合或修改、删除密钥环条目，仅检查连通性和集合状态，即使集合已解锁，也不能保证 SSHYard 能读取或保存主密钥；D-Bus 连接错误指向登录会话或访问策略问题，`ServiceUnknown` 通常表示没有可激活的 Secret Service 服务，返回值定义参见 [ReadAlias API](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Service.html#org.freedesktop.Secret.Service.ReadAlias)

## 开发

基于 [mygo](https://github.com/egoist/mygo) 构建，需要 Go 1.27.3（可借助 `GOTOOLCHAIN` 自动下载），不需要 cgo。

```sh
# 运行
go run .

# 运行开发版 app；macOS 上优先用 "Apple Development" 证书签名
# 稳定签名可减少钥匙串提示，自签名不能保证跨构建沿用授权
# macOS 仍可能要求重新授权
scripts/run-dev-app.sh

# 把数据放到单独的目录
SSHYARD_DATA_DIR=/tmp/demo go run .

# 本地测试用 SSH 服务器（macOS / Linux）
go run ./cmd/devsshd -addr 127.0.0.1:2222 -user demo -password demo -root /tmp/remote

# 本地测试用 HTTP / SOCKS 代理
go run ./cmd/devproxy -addr 127.0.0.1:3128

# 测试与检查
go test -race -count=1 ./...
go vet ./...
gofmt -l .

# 修改 locales/*.json 后重新生成多语言目录
GOFLAGS=-mod=mod go run github.com/egoist/mygo/cmd/mygo generate

# 打包到 dist/
go tool mygo build -platform linux/amd64,linux/arm64,windows/amd64,windows/arm64,darwin/arm64,darwin/amd64 -o dist
```

连接 devsshd 测试时，新建主机 `127.0.0.1`，端口 `2222`，用户 `demo`，密码 `demo`。

开发约定见 [AGENTS.md](AGENTS.md)。

## 社区支持

- [LinuxDO](https://linux.do)

## 开源协议

SSHYard 基于 [GNU Affero General Public License v3.0](LICENSE) 开源。
