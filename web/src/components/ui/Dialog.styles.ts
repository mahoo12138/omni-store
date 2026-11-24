import { makeStyles, shorthands, tokens } from '@fluentui/react-components'
import { layout } from '../../styles/layout'

// Fluent Dialog 原生观感;这里只有关闭按钮(absolute 于 surface 右上)与描述文本。
export const useStyles = makeStyles({
  // 关闭按钮的外观（含 hover/focus/圆角）由 Fluent Button 负责，
  // 这里只负责把它钉在弹窗右上角。
  close: {
    position: 'absolute',
    top: '10px',
    right: '10px',
    zIndex: 1,
  },

  description: {
    margin: '0',
    fontSize: '13px',
    lineHeight: '1.5',
    color: 'var(--colorNeutralForeground2)',
    overflowWrap: 'anywhere',
  },
  // 弹窗内容区的纵向节奏：绝大多数弹窗直接把 Field / Checkbox / Banner
  // 堆在 DialogContent 里，没有这层间距时字段会互相贴住。
  content: {
    'display': 'grid',
    'alignContent': 'start',
    'gap': tokens.spacingVerticalXL,
    'minWidth': '0px',
    // 短表单弹窗也要有舒展的内容区；矮屏用 30dvh 兜底，避免最小高度把弹窗顶出视口。
    'minHeight': `min(${layout.dialogMinContentHeight}, 30dvh)`,
    // 按钮区上方留白，内容不再贴着 footer。
    'paddingBottom': tokens.spacingVerticalXL,
  },
  titleBlock: {
    gridRow: 1,
    gridColumn: '1 / -1',
    paddingRight: tokens.spacingHorizontalXXXL,
    minWidth: '0px',
  },
  surface: {
    ...shorthands.borderColor(tokens.colorNeutralStroke2),
    '&[data-fui-focus-visible]::after': {
      ...shorthands.borderRadius(tokens.borderRadiusXLarge),
    },
  },
  // Fluent DialogActions 默认左对齐；底部操作区靠右是本产品的约定。
  'actions': {
  'justifyContent': 'flex-end',
  },
})
