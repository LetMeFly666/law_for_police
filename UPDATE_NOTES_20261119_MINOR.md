# 2026-11-19 小更新部署说明

覆盖以下文件：
- `internal/server.go`
- `internal/web/app.js`
- `internal/web/index.html`
- `internal/web/service-worker.js`

无需数据库迁移，保留现有 `.env`、`APP_SECRET` 和 MySQL 数据。

更新后重启 Go 服务，并在浏览器刷新页面。已安装 PWA 的 Service Worker cache 已升级为 v2；如仍看到旧内容，关闭旧标签页后重新打开，必要时从主屏幕退出并重新启动网页 App。

本次反馈历史 API 只返回当前登录账号自己的反馈，路由要求身份认证。旧反馈如果创建时没有关联 `user_id`，无法自动归属到某个账号，因此不会显示在个人历史中。
