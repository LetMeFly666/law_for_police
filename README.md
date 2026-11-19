<!--
 * @Author: LetMeFly
 * @Date: 2026-11-18 14:16:05
 * @LastEditors: LetMeFly.xyz
 * @LastEditTime: 2026-11-19 12:20:08
-->
某题库背题辅助工具，带部分解析，可跨设备同步。

# 法律基础知识 · 学习本（Go + MySQL）

把现有法律题库学习工具改成 **Go 服务端 + MySQL 持久化**。前端使用原生 HTML、CSS、JavaScript，不依赖前端框架或第三方 UI 库；页面加载自托管的 `Common.js` 访问统计脚本。Go 服务用 `embed.FS` 将页面、脚本和题库嵌入可执行文件。

## 技术选型

- Go 工具链最低版本：`go1.26.5`，见 `go.mod`。代码没有使用高于此版本的语言特性。
- 截至 2026-11-18，Go 1.26 分支已有 1.26.11 安全修订版。项目保留 `go 1.26.5` 的最低版本要求；部署时建议使用 Go 1.26.11 或更高兼容补丁版本构建。官方版本记录：<https://go.dev/doc/devel/release>。
- 数据库使用现有 MySQL，所有账号、设备、学习记录、笔迹、题友心得和反馈都保存在 MySQL。学习记录采用“每账号递增版本号 + 每项最新值”同步，不保存重复的历史笔迹快照。
- 唯一显式声明的第三方 Go 依赖是 `github.com/go-sql-driver/mysql`（v1.10.2，纯 Go，无 CGO）；它支持 Go 1.24+ 和 MySQL 5.7+。Go 标准库的 `database/sql` 不包含 MySQL 网络协议驱动，因此需要这个驱动。邮件使用标准库 `net/smtp`，HTTP 使用标准库 `net/http`。

## 项目结构

```text
law_study_go/
├── cmd/lawstudy/main.go        # 入口、HTTP 服务、优雅退出
├── internal/
│   ├── config.go               # 环境变量、MySQL 配置
│   ├── db.go                   # MySQL 连接池、自动建表
│   ├── server.go               # API、账号恢复、同步、SMTP、反馈
│   └── web/
│       ├── index.html          # 页面结构与内嵌题库数据
│       ├── app.js              # 页面交互逻辑（由 index.html 加载）
│       └── bank.json           # 110 道题、解析、助记
├── deploy/
│   ├── law-study.service       # systemd 非 root 服务
│   └── nginx-location.conf    # Nginx 反向代理片段
├── sql/00-create-database.sql  # MySQL 数据库和专用用户示例
├── internal/server_test.go     # 不依赖 MySQL 的单元测试
├── .env.example
├── go.mod
└── README.md
```

## 账号：首次打开即在服务端建号

第一次打开页面时，浏览器会自动请求服务端创建账号和当前设备。无需注册、无需先点“同步”：

1. 服务端创建匿名用户记录和设备记录。
2. 服务端生成随机设备令牌，浏览器保存令牌；MySQL 只保存令牌的 SHA-256 哈希。
3. 服务端生成 **160 bit 永久设备连接码**，只把原始连接码返回这一次；MySQL 只保存它的 SHA-256 哈希。
4. 页面首次显示连接码保存提示。建议复制到密码管理器或其他安全位置。

永久设备连接码就是账号恢复密钥，不是 10 分钟的一次性配对码。它允许新设备加入原账号；在新设备打开网站时，新设备也会自动建立本机设备凭证，粘贴永久连接码后，服务端把这台设备关联到已有账号。之后所有设备共享同一用户数据。

**设备损坏后的恢复：**在新设备打开网站，选择“在本设备恢复已有账号”，粘贴永久连接码。服务端切换该设备所属账号，随后同步该账号的全部学习数据。账号数据不会依赖旧设备的 localStorage。

如果用户主动轮换永久连接码，之前的旧码立即失效；已关联设备仍能继续使用。永久连接码应当视为密码，不要公开发送。若清除浏览器数据且没有在其他地方保存连接码，也没有绑定验证邮箱，则不能从服务端反推出原始连接码。

## 邮箱找回与自定义昵称

昵称可以在“个人设置”里自定义，并随账号数据同步。邮箱是可选项：配置 SMTP 后，用户可以请求 6 位验证码并完成邮箱验证。验证过的邮箱可在新设备上用于找回账号：

- 旧设备不存在时，新设备使用“邮箱找回”，输入已验证邮箱和验证码。
- 验证成功后，服务端会把新设备关联到原账号，并轮换永久连接码。
- 新连接码会显示在页面，必须重新保存；旧连接码此时不再有效。

邮件验证码有效期 10 分钟，60 秒发送冷却，并限制最多 5 次错误验证。验证码使用 `APP_SECRET` 做 HMAC-SHA256 后保存，数据库不存明文验证码。未配置 SMTP 时，邮箱绑定和邮件找回入口不可用，其他学习功能不受影响。

## 学习数据实时保存

页面以服务端 MySQL 为持久化主存，本地 localStorage 只作为界面缓存和网络中断时的暂存队列。每个账号维护一个单调递增版本号，每个笔记/收藏/掌握度/自测记录只保存最新值；设备按版本增量拉取。新设备或游标失效时，服务端按一致性快照返回当前数据，而不是重放全部历史笔迹。

- 文字笔记：输入停止约 250 ms 后保存到本地，随后约 60 ms 内发起服务端同步。
- 收藏、掌握状态、自测记录、复习阈值和昵称：操作后立即进入同步队列。
- 画布圈画：每笔结束立即保存到本地并发起同步；绘制过程中不会重建画布或丢失当前指针轨迹。
- 其他设备更新：页面每 2.5 秒轻量轮询一次；当前设备发起的写入不等待轮询。
- 短暂离线时，变更保存在浏览器本地队列；网络恢复后自动重试。服务端确认写入后，即使浏览器缓存清空也能从账号恢复。

网络断开时还没有送达服务器的最后少量操作只能暂存在当前设备；任何纯网页应用都无法保证离线设备突然损坏后尚未上传的内容也能恢复。因此页面会提示云端连接状态，正式使用时建议等状态显示已连接后再结束重要批注。

## 圈画笔记：直接在题目纸面上操作

每题支持文字笔记和圈画笔记。工具栏只保留“文字笔记 / 圈画笔记 / 收藏 / 题友心得”。点击“圈画笔记”后，题目下方直接展开纸面，用户可以在题干和选项上用手指、触控笔或鼠标圈画，不再打开独立画图弹窗。

红笔、荧光笔、橡皮、撤销一笔、清空圈画和“完成”全部紧邻“本题圈画笔记”显示；工具按钮使用白色样式。选中画笔后可直接在题目纸面上绘制。笔迹以矢量路径 JSON 保存，记录工具、颜色、宽度和 0—1 归一化坐标；调整屏幕尺寸后按新画布重绘。每笔完成后本地保存并进入多设备同步队列。

## 题友心得

题友心得按点赞数从高到低显示，支持分页（每页 10 条）、点赞/取消点赞、查看点赞用户。每个用户对同一条心得最多有一条点赞记录，由 MySQL 复合主键保证。用户只能编辑或删除自己的心得；公开列表中的昵称通过用户表实时读取，因此昵称变更会同步更新旧心得的显示名称。

个人心得弹窗默认显示最近 10 条，数量更多时可点“显示所有心得”。公开列表优先显示在发布输入框上方；没有心得时，空状态提示放在输入框下方。

## 学习时长与同步

学习页面在可见且窗口处于前台时计时，切到后台或失去焦点后暂停。学习时长以批次写入 MySQL，前端会在短暂断网时将未上传时长暂存在本机；每个批次有唯一 ID，服务端通过唯一键防止网络重试造成重复累计。学习时长按账号汇总，多设备共享。

当前题目、题型、做题/背题模式、搜索内容、筛选、导航展开状态、圈画面板状态、复习阈值、昵称、收藏、掌握状态和笔记均纳入账号同步。切换设备后会从同一账号恢复这些设置和学习记录。

## 反馈与邮件通知

反馈内容取消原来的 5 字下限，只要求非空，最多 3000 字。配置 SMTP 后，每条新反馈会在写入 MySQL 后额外通知 `Tisfy@qq.com`；邮件发送失败不会影响反馈保存，具体错误会写入服务端日志。未配置 SMTP 时，反馈仍可正常提交和在管理后台查看。

页面同时加载 `https://web.letmefly.xyz/Links/Common.js` 访问统计脚本；脚本加载失败不会阻断题库核心功能。

## MySQL 准备

推荐给学习本单独建立数据库和数据库用户，不要复用 Nextcloud 的数据库账号，也不要让 Go 服务使用 MySQL root。

先修改 `sql/00-create-database.sql` 中的数据库密码，再由 MySQL 管理员执行。示例授权只针对 `law_study` 数据库。MySQL 用户的 Host 必须和 Go 服务实际连入数据库时匹配；例如容器网桥的访问方式可能需要将 `'lawstudy'@'localhost'` 改为 `'lawstudy'@'172.17.%'` 或按你的安全边界配置具体来源地址。

你的服务器之前使用过 `172.17.0.1:3306` 访问 MySQL。如果该地址仍是 Go 服务能访问的 MySQL 地址，环境变量中设置 `MYSQL_HOST=172.17.0.1`；若 Go 服务和 MySQL 都在宿主机，优先测试 `127.0.0.1`。不要未经测试就假定 MySQL 用户的 Host 匹配规则。

## 本地运行

需要 Go 1.26.5 或更高版本，以及可访问的 MySQL 实例。首次下载依赖时，Go modules 只需获取 MySQL 驱动及其必要的传递依赖。

```bash
cd law_study_go

# 唯一显式的第三方 Go 模块是 MySQL driver。
go mod tidy

export HTTP_ADDR=127.0.0.1:8787
export MYSQL_HOST=127.0.0.1
export MYSQL_PORT=3306
export MYSQL_USER=lawstudy
export MYSQL_PASSWORD='请替换为长随机密码'
export MYSQL_DATABASE=law_study
export APP_SECRET="$(openssl rand -hex 32)"
export ADMIN_TOKEN="$(openssl rand -hex 32)"

# 邮箱功能可选，SMTP 常用 587 + STARTTLS。
# export SMTP_HOST=smtp.example.com
# export SMTP_PORT=587
# export SMTP_USER=...
# export SMTP_PASSWORD=...
# export SMTP_FROM=law-study@example.com

go run ./cmd/lawstudy
```

访问 `http://127.0.0.1:8787/`。健康检查：

```bash
curl http://127.0.0.1:8787/api/health
```

Go 服务启动时会自动执行 `CREATE TABLE IF NOT EXISTS`。数据库必须预先创建，应用数据库用户需要对该库有查询、新增、更新、删除、建表、修改表结构和索引权限；示例 SQL 已限定为 `law_study` 这一库。

## Ubuntu 部署：/srv + systemd + Nginx

按独立站点根路径部署。Go 进程只监听 `127.0.0.1:8787`，Nginx 继续负责 HTTPS；应用以专用低权限用户 `lawstudy` 运行，不使用 root，不需要应用本地可写目录。

### 1. 上传项目并准备用户

```bash
sudo install -d -o root -g root /srv/law-study
sudo cp -a law_study_go/. /srv/law-study/
sudo useradd --system --user-group --home /nonexistent --shell /usr/sbin/nologin lawstudy
```

若 `lawstudy` 用户已经存在，不要重复创建。

### 2. 构建二进制

使用 Go 1.26.5 或更新版本：

```bash
cd /srv/law-study
go mod tidy
sudo install -d -o root -g root -m 755 bin
go build -trimpath -ldflags='-s -w' -o bin/lawstudy ./cmd/lawstudy
sudo chmod 755 bin/lawstudy
```

源码、依赖缓存和二进制不需要由服务账户修改。静态文件已经嵌入二进制。

### 3. 创建环境文件

```bash
sudo install -d -m 700 /etc/law-study
sudo sh -c 'cat > /etc/law-study/law-study.env <<EOF
HTTP_ADDR=127.0.0.1:8787
MYSQL_HOST=127.0.0.1
MYSQL_PORT=3306
MYSQL_USER=lawstudy
MYSQL_PASSWORD=替换为数据库密码
MYSQL_DATABASE=law_study
APP_SECRET=$(openssl rand -hex 32)
ADMIN_TOKEN=$(openssl rand -hex 32)
EOF'
sudo chmod 600 /etc/law-study/law-study.env
```

如需 SMTP 邮箱功能，再向该文件增加 `SMTP_HOST`、`SMTP_PORT`、`SMTP_USER`、`SMTP_PASSWORD`、`SMTP_FROM`。不要将环境文件加入 Git 或放在网站静态目录中。

### 4. 安装 systemd 服务

```bash
sudo cp /srv/law-study/deploy/law-study.service /etc/systemd/system/law-study.service
sudo systemctl daemon-reload
sudo systemctl enable --now law-study
sudo systemctl status law-study
sudo journalctl -u law-study -f
```

### 5. 配置 Nginx

将 `deploy/nginx-location.conf` 的 `location /` 片段合并到当前站点 HTTPS `server` 块，保留原有域名和证书配置：

```bash
sudo nginx -t
sudo systemctl reload nginx
```

不要将 8787 直接开放到公网。页面和 API 同源，不需要配置宽泛 CORS。

## 管理员反馈接口

用户反馈写入 `feedback` 表，普通用户只能提交，不能查询全部反馈。管理员通过 `X-Admin-Token` 访问：

```bash
curl -H "X-Admin-Token: YOUR_ADMIN_TOKEN" \
  "https://你的域名/api/admin/feedback"
```

状态可设置为 `new`、`reviewing`、`resolved` 或 `rejected`：

```bash
curl -X PATCH \
  -H "X-Admin-Token: YOUR_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"status":"reviewing"}' \
  "https://你的域名/api/admin/feedback/FEEDBACK_ID"
```

## 数据备份

账号恢复码能在设备损坏后恢复 MySQL 中的账号数据，但不能替代数据库备份。建议为 MySQL 的 `law_study` 库配置定期备份，并测试恢复：

```bash
mysqldump --single-transaction --routines --triggers law_study > law_study_$(date +%F).sql
```

备份文件应保存在服务器以外的安全位置。恢复前先停止写入，并按你的数据库运维策略保留历史备份。

## 自检

```bash
go test ./...
go vet ./...
```

测试无需连接 MySQL，主要检查恢复码、同步记录键、HMAC 验证摘要和嵌入的首页资源。完整运行仍需配置真实 MySQL；邮箱绑定/找回还需要可用 SMTP 服务。
