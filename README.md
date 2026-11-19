<!--
 * @Author: LetMeFly
 * @Date: 2026-11-18 14:16:05
 * @LastEditors: LetMeFly.xyz
 * @LastEditTime: 2026-11-18 14:23:52
-->
某题库背题辅助工具，带部分解析，可跨设备同步。

# 圈画修复补丁

替换 Go 项目中的：
- `internal/web/app.js`
- `internal/web/index.html`

本补丁修正圈画输入：避免重复指针事件覆盖当前笔画，检查指针 ID，补充 `lostpointercapture` 结束处理，并为不支持 Pointer Events 的旧 Safari/WebView 增加触摸事件回退。画布强制 `touch-action:none` 并提升绘制层级。

验证：`node --check app.js` 和从 index.html 提取的内嵌脚本 `node --check` 均通过。当前环境的 Chromium 打开本地页面被系统策略拦截，因此没有完成真实触摸/鼠标绘制端到端测试。
