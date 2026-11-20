# 2026-11-20 更新说明

本次在 2026-11-20 文字笔记更新版基础上继续修改。

## 修改内容

1. 左上角「法」Logo 连续点击三次，跳转到 `/admin` 管理员 Web 页面；原双击全屏行为不再绑定。
2. 掌握状态修正：已经是 `mastered` 的题目再次答对或点击「答对一次」时，连续正确次数继续增加，但状态仍保持 `mastered`，不会退回「学习中」。答错仍按原逻辑进入「需复习」。
3. 自测记录按每批 20 条渐进渲染，滚动接近列表底部时提前追加下一批，不再一次性创建全部历史记录 DOM。
4. 「我的心得」每次从服务端加载 20 条，滚动到底部附近再请求下一页；移除原先点击「显示所有心得」后循环请求全部页面的行为。
5. 「我的历史反馈」同样改为每批 20 条滚动加载。`GET /api/feedback/mine` 增加 `limit`、`offset` 分页参数，并返回 `total`、`limit`、`offset`。
6. Service Worker 缓存版本升级到 `v4`，使已安装 PWA 能更新本次前端脚本。

## 数据库

无需数据库迁移。反馈分页使用现有 `feedback.user_id` 查询和排序字段。

## 验证

- `node --check internal/web/app.js` 通过。
- `node --check internal/web/service-worker.js` 通过。
- HTML ID 重复检查通过（111 个 ID，无重复）。
- Go 测试未能运行：项目要求 Go 1.26.5，当前环境尝试下载工具链时网络 DNS 连接失败。

列表采用 `IntersectionObserver`，提前 280px 触发下一页加载；同时保留「继续下滑加载」按钮作为不支持 `IntersectionObserver` 时的手动回退。
