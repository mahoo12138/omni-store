# OmniStore Web 前端开发规范（AGENTS.md）

本文件适用于 `web/` 前端项目，作为 [docs/AGENT_GUIDE.md](../docs/AGENT_GUIDE.md) 与 [docs/DESIGN_SYSTEM.md](../docs/DESIGN_SYSTEM.md) 在前端工程与交互上的补充约定。

## 1. 表单与编辑交互规范

- **禁止在卡片或页面内平铺/展开输入表单**：设置页与详情页中的卡片（`SectionCard`）仅用于只读摘要展示与操作入口触发，不得在卡片内部直接渲染或切换展开编辑表单。
- **修改与新增统一走弹窗（`DialogWrap`）**：所有配置修改、名称编辑、凭据生成与资源创建操作，均应通过 `src/components/ui/Dialog.tsx` 提供的 `DialogWrap` 弹窗承接。
- **表单骨架统一**：弹窗内部表单字段使用 `FormField` / `FormLayout`（`src/components/shell/FormLayout.tsx`），多列字段使用 `<FormLayout columns={2}>`；底部操作区统一右对齐（「取消」+ 主操作按钮）。
- **只读凭据/地址复制行**：使用 flex 横向排列（窄屏自动换行/堆叠），只读 `<Input>` 设置 `flex: 1 1 0%; min-width: 0`，确保长 Token/URL 完整展示不截断。

## 2. 列表与表格规范

- **列表容器**：管理端资源列表（如存储源、访问策略、用户、审计日志）统一使用 `SectionCard`（`bodyPadding="none"`）+ `HorizontalDataRegion` + Fluent `Table`（`compactTable` / `compactTableWide`），保证窄屏局部横向滚动而不撑破页面。
- **长文本控制**：列表中的名称与路径单元格应限制最大宽度并启用单行省略（`text-overflow: ellipsis` + `title` 提示）。
- **表格操作列**：行尾操作统一使用 `IconAction`（`src/components/ui/IconAction.tsx`，带 `Tooltip` 与 `aria-label`），破坏性操作（如删除）使用 `danger` 置于末尾。

## 3. 样式与 Griffel 规范

- **使用 Fluent 主题 Token**：颜色、字号、圆角、间距优先使用 `@fluentui/react-components` 的 `tokens` 或 CSS 变量，确保浅色/深色主题一致。
- **禁止双参数 `shorthands.gap(a, b)`**：Griffel 的 `shorthands.gap(columnGap, rowGap)` 与 CSS 原生 `gap: row column` 参数顺序相反。需要分别指定行列间距时，务必显式书写 `rowGap` 与 `columnGap`。
- **文案克制**：遵循 [docs/DESIGN_SYSTEM.md §7](../docs/DESIGN_SYSTEM.md)，不添加解释性副标题或系统行为自述。

## 4. 构建与验证

修改前端代码后，需在提交前完成验证（且重新构建嵌入到 Go 服务端的 `web/dist`）：

- 类型检查：`./node_modules/.bin/tsc -b tsconfig.json`
- 生产构建（含 Griffel AOT 提取）：`./node_modules/.bin/vite build`
- 单元测试：`./node_modules/.bin/vitest run src`
