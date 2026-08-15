# 实施计划: 前端页面红色主题美化

**分支**: `001-beautify-red-theme` | **日期**: 2025-12-17 | **规范**: [spec.md](spec.md)
**输入**: 来自 `/specs/001-beautify-red-theme/spec.md` 的功能规范

## 摘要

将 NoneLane Dashboard 前端页面中所有绿色的成功状态元素（按钮、文本、徽章）替换为红色主题配色，实现视觉统一性。技术方法：更新 CSS 变量和样式类定义，修改 HTML 中的硬编码样式类，更新 JavaScript 中的动态样式类逻辑。不涉及后端或数据模型更改，纯前端样式修改。

## 技术背景

**语言/版本**: HTML5, CSS3, JavaScript (ES6+)
**主要依赖**: Bootstrap 5.x (CSS framework), Bootstrap Icons, Chart.js 4.4.0
**存储**: N/A（纯静态前端资源）
**测试**: 手动视觉测试 + 浏览器兼容性测试（Chrome, Safari, Firefox）
**目标平台**: Web 浏览器（桌面端为主，响应式设计）
**项目类型**: Web 应用（单页面 Dashboard，前端静态资源）
**性能目标**: 页面加载时间 < 2秒，样式渲染即时（无闪烁）
**约束条件**: 不修改 Bootstrap 核心库文件，仅通过自定义 CSS 覆盖；保持现有页面布局和功能不变
**规模/范围**: 7个页面（仪表板、代理管理、运行控制、规则引擎、日志、用户信息、DNS缓存），约15处绿色元素需要替换

## 章程检查

*门控: 项目章程尚未初始化（仍为模板），因此此功能无章程约束。基于代码审查，项目遵循简单性原则（无过度工程）。*

**检查结果**: ✅ 通过
- 无需新增依赖或复杂架构
- 纯样式修改，不引入技术债务
- 保持现有代码结构和模式
- 可独立测试和回滚

## 项目结构

### 文档（此功能）

```
specs/001-beautify-red-theme/
├── spec.md              # 功能规范
├── plan.md              # 此文件（/speckit.plan 命令输出）
├── research.md          # 阶段 0 输出：CSS 变量和样式类研究
├── quickstart.md        # 阶段 1 输出：测试和验证指南
├── checklists/
│   └── requirements.md  # 规范质量检查清单
└── tasks.md             # 阶段 2 输出（/speckit.tasks 命令生成）
```

### 源代码（仓库根目录）

```
app/website/
├── index.html                    # 主 HTML 文件（包含所有页面的 DOM 结构）
├── assets/
│   ├── styles.css                # 自定义样式表（需要修改）
│   ├── app.js                    # 主 JavaScript 文件（需要修改动态样式类）
│   ├── bootstrap.min.css         # Bootstrap 核心（不修改）
│   ├── bootstrap-icons.css       # 图标库（不修改）
│   └── fonts/                    # 字体文件（不修改）
```

**结构决策**:
- 项目采用单一 HTML 文件 + 多页面 SPA 结构，所有页面通过 JavaScript 切换显示
- 样式通过 CSS 变量 + Bootstrap 类系统管理
- 现有的 `--bs-success` 变量已设置为红色 `#f24646`，但 HTML 和 JS 中仍使用 `btn-success` 等 Bootstrap 绿色类
- 修改策略：将 `btn-success` → `btn-danger`，`text-success` → `text-danger`，`bg-success` → `bg-danger`

## 复杂度跟踪

*无违规项。此功能为简单的样式替换，不涉及架构复杂度。*

| 违规 | 为什么需要 | 拒绝更简单替代方案的原因 |
|------|-----------|------------------------|
| N/A  | N/A       | N/A                    |
