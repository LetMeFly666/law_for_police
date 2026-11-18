<!--
 * @Author: LetMeFly
 * @Date: 2026-11-18 14:16:05
 * @LastEditors: LetMeFly.xyz
 * @LastEditTime: 2026-11-18 14:19:58
-->
某题库背题辅助工具，带部分解析，可跨设备同步。

# 法律基础知识 · 学习本

一个面向已知考试题库的复习工具，包含 110 道题、背题/做题模式、题号导航、自动判分、自评掌握度、笔记、收藏、题友心得、反馈入口和匿名多设备同步。

## 功能概览

- 六种题型：判断 20、填空 20、单选 30、多选 30、简答 6、论述 4。
- 背题模式：查看参考答案、解析、法律依据和助记。
- 做题模式：判断、单选、多选自动判分；填空、简答、论述不要求输入完整答案，先查看参考答案，再选择“我会了”或“还不熟”。
- 掌握度：默认连续答对 3 次后标记为“已掌握”，个人设置中可调整为 1—10 次。支持只看未掌握、只看已掌握、只看收藏和只看未自测。
- 题号导航、搜索、收藏夹、自测历史、每题私有笔记。
- 每题可发布公开心得；其他学习者能在同一道题下面查看。个人也能查看自己发布过的心得。
- 反馈建议会进入开发者后台 API。
- 自动创建匿名学习身份。配对设备后，笔记、收藏、掌握度、自测记录会自动同步，不需要用户导出或导入 JSON。
- 触屏设备会放大记笔记按钮和常用操作按钮。

## 目录

```text
law_study_system/
├── backend/
│   ├── __init__.py
│   ├── app.py
│   └── requirements.txt
├── web/
│   ├── index.html      # 前端单文件，题库与逻辑均内嵌
│   ├── app.js          # 前端源码，便于开发维护
│   └── bank.json       # 题库数据源
├── deploy/
│   ├── law-study.service
│   └── nginx-location.conf
├── .env.example
└── README.md
```

`web/index.html` 是完整前端，不依赖 CDN 或第三方 JavaScript 服务。后端 FastAPI 同源提供页面和 API。

## 本地运行

需要 Python 3.10 或更新版本。

安装依赖后，可以运行 API 冒烟测试，覆盖匿名身份、多设备配对、同步交换、心得发布、反馈提交和管理员接口：

```bash
python tests/api_smoke.py
```

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install -r backend/requirements.txt
DATA_DIR=./data ADMIN_TOKEN='请替换为长随机值' \
  uvicorn backend.app:app --host 127.0.0.1 --port 8765
```

浏览器访问 `http://127.0.0.1:8765/`。

健康检查：

```bash
curl http://127.0.0.1:8765/api/health
```

如果只想离线使用前端，也可以直接打开 `web/index.html`。本地背题、做题、笔记、收藏和自测记录可以运行；多设备同步、心得发布和反馈提交需要 API 服务可访问。

## 推荐部署方式：Ubuntu + systemd + Nginx

以下按专用低权限用户运行，应用仅监听 `127.0.0.1`，不直接暴露公网端口。Nginx 继续负责 HTTPS。

### 1. 放置项目

将整个项目目录上传到服务器 `/srv/law-study`。例如：

```bash
sudo install -d -o root -g root /srv/law-study
sudo cp -a law_study_system/. /srv/law-study/
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin lawstudy
sudo install -d -o lawstudy -g lawstudy -m 750 /srv/law-study/data
```

如果 `lawstudy` 用户已经存在，不必重复创建。

### 2. 安装依赖

```bash
cd /srv/law-study
sudo python3 -m venv .venv
sudo .venv/bin/pip install --upgrade pip
sudo .venv/bin/pip install -r backend/requirements.txt
```

代码和虚拟环境可由 root 管理，服务用户只需要读取代码、执行虚拟环境，并对 data 目录有写权限。

### 3. 配置环境变量

```bash
sudo install -d -m 700 /etc/law-study
sudo sh -c 'printf "%s\\n" \
  "DATA_DIR=/srv/law-study/data" \
  "ADMIN_TOKEN=$(openssl rand -hex 32)" \
  > /etc/law-study/law-study.env'
sudo chmod 600 /etc/law-study/law-study.env
```

请妥善保存 `ADMIN_TOKEN`。它用于读取用户提交给开发者的反馈，不要写入前端 HTML、Git 仓库或公开日志。

### 4. 安装 systemd 服务

```bash
sudo cp /srv/law-study/deploy/law-study.service /etc/systemd/system/law-study.service
sudo systemctl daemon-reload
sudo systemctl enable --now law-study
sudo systemctl status law-study
```

查看日志：

```bash
sudo journalctl -u law-study -f
```

### 5. 配置 Nginx 反向代理

把 `deploy/nginx-location.conf` 中的 `location /` 片段加入现有 HTTPS `server` 块。不要另行开放 8765 公网端口。

检查并重载：

```bash
sudo nginx -t
sudo systemctl reload nginx
```

然后访问站点 HTTPS 地址。若应用部署在子路径而不是独立域名根路径，需要同时调整前端 API 路径和反向代理配置；当前默认按独立站点根路径部署。

## 多设备同步的实际流程

1. 设备 A 打开学习本，首次访问时自动创建匿名设备身份，学习数据自动同步到服务端。
2. 设备 A 点击“多设备同步” → “同步到其他设备”，获得一次性连接码。
3. 设备 B 打开同一站点，点击“多设备同步” → “从其他设备导入”，粘贴连接码。
4. 两台设备加入同一个学习空间。之后笔记、收藏、掌握记录和自测历史会在页面操作后自动同步，并每 25 秒进行一次后台同步。

连接码随机生成、有效期 10 分钟、仅可使用一次。连接码是设备邀请凭证，只发给自己的设备，不要公开分享。配对后使用随机设备令牌认证，用户不需要注册账号或输入密码。

同步采用按记录项的变更事件：每道题的笔记、收藏、掌握记录和每条自测历史单独同步，服务端用 SQLite 保存同步事件与当前值。跨设备同时修改同一记录时，以较新的更新时间为准；自测记录按唯一 ID 合并。

## 题友心得与开发者反馈

- 题友心得通过 `POST /api/experiences` 发布，按 `question_id` 公开展示；`GET /api/experiences?question_id=judge-19` 可读取某题心得。
- 当前学习空间的个人心得通过 `GET /api/experiences/mine` 查看。
- 用户反馈通过 `POST /api/feedback` 提交，普通用户不能读取其他用户的反馈。
- 管理员通过 `X-Admin-Token` 请求头访问：

```bash
curl -H "X-Admin-Token: YOUR_ADMIN_TOKEN" \
  "https://你的域名/api/admin/feedback"
```

反馈状态可以设为 `new`、`reviewing`、`resolved` 或 `rejected`：

```bash
curl -X PATCH \
  -H "X-Admin-Token: YOUR_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status":"reviewing"}' \
  "https://你的域名/api/admin/feedback/反馈ID"
```

## 数据与隐私

- 学习数据在浏览器 localStorage 保存一份，并同步到服务端 SQLite。
- 个人笔记、收藏和掌握记录属于私人学习数据，不通过公开心得 API 暴露。
- 题友心得是公开内容；发布时会显示昵称和文字，不会公开邮箱。
- 邮箱为选填，只随反馈提交给开发者，不用于登录。
- 服务端保存随机设备令牌的 SHA-256 哈希，不保存明文令牌；连接码也只保存哈希。
- SQLite 数据库当前未做应用层字段加密。服务器管理员可访问数据库文件，应确保服务器磁盘、备份和文件权限安全。
- 建议定期备份 `/srv/law-study/data/law_study.sqlite3` 及其 WAL 文件；备份时优先使用 SQLite 在线备份工具，或在停止服务后复制数据库。

## 维护与升级

升级前备份 `data/`。更新前端可以替换 `web/index.html`、`web/app.js`、`web/bank.json`；实际运行时 `index.html` 已经内嵌题库和脚本，因此生产环境只需替换 `index.html` 即可，另外两个文件用于源码维护。后端升级后：

```bash
sudo systemctl restart law-study
sudo journalctl -u law-study -n 100 --no-pager
```

## 已知边界

- 目前没有密码登录、找回密码或跨群组迁移；设备加入同一学习空间靠一次性连接码。
- 题友心得当前是公开发布、按题展示；尚未实现编辑、删除、举报和审核后台页面。
- 开发者反馈有 API 管理端点，尚未做独立的图形化反馈管理台。
- 客观题按选项精确匹配判分。简答、论述和填空采用用户查看答案后的自评，不做自然语言自动评分。
- 新题、法律答案更新和助记内容需要维护题库 JSON 并重新生成内嵌 HTML。
