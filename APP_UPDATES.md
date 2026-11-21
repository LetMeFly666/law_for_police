# 应用版本与更新内容维护

当前应用版本：`1.0.4`（2026-11-21）。版本号和用户可见更新说明集中维护在 `internal/server.go`：

- `currentAppVersion`：当前正式版本号。每次有新的用户可见功能更新时递增。
- `appUpdateReleases`：按版本追加更新内容，支持 `highlights` 重点词字段。一个版本可包含多条更新说明。
- 当前正式版本依次包含 `1.0.0`、`1.0.1-beta`、`1.0.2`、`1.0.3` 和 `1.0.4` 的历史记录。

发布新版本时：

1. 将 `currentAppVersion` 更新为新版本号。Beta 版本可使用 `1.0.5-beta`，对应正式版使用更高版本号，例如 `1.0.6`。版本比较支持 prerelease 后缀，正式版高于相同核心版本的 Beta 版。
2. 在 `appUpdateReleases` 末尾追加对应版本、日期、标题、`Highlights` 重点词和项目列表，不要覆盖历史记录。
3. 若前端静态资源更新，递增 `internal/web/service-worker.js` 的 `CACHE_NAME`。
4. 部署后，已有账号的 `users.last_seen_app_version` 会在用户领取更新弹窗时推进到当前版本；新建账号在创建时直接记录当前版本，因此新用户不会看到首次更新弹窗。

`users.last_seen_app_version` 是账号级字段，不是设备级字段。`POST /api/app-updates/claim` 在事务中锁定用户行并推进已读版本，因此同一账号同时在多台设备启动时，只有一台设备能领取到待展示内容。

用户关闭“登录后显示新版本更新内容”时，服务端不会推进已读版本；日后重新开启时，会立即检查并展示此前尚未查看的更新。历史版本说明由 `GET /api/app-updates/history` 提供。历史列表会过滤掉本次更新弹窗已展示的版本，避免重复。
