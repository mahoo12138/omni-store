import { makeStyles, shorthands, tokens } from '@fluentui/react-components'

export const useStyles = makeStyles({
  root: {
  'minWidth': '0px',
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('16px'),
  },
  // 间距由外层容器负责：TabList 与 Tabs.Panel 都不再被注入外观，
  // 因此 Fluent tab 保持默认下划线观感，panel 的 hidden 也不会被 display 打败。
  tabs: {
  'minWidth': '0px',
  'display': 'grid',
  ...shorthands.gap(tokens.spacingVerticalXXL),
  },
  panelBody: {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap(tokens.spacingVerticalXXL),
  'minWidth': '0px',
  },
  dialogSection: {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap(tokens.spacingVerticalM),
  'paddingTop': tokens.spacingVerticalL,
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  },
  dialogFieldGroup: {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap(tokens.spacingVerticalXS),
  },
  hint: {
  ...shorthands.margin('0px'),
  'fontSize': '13px',
  'lineHeight': 1.6,
  'color': 'var(--colorNeutralForeground2)',
  'overflowWrap': 'anywhere',
  },
  serviceBody: {
  'display': 'flex',
  'flexDirection': 'column',
  'gap': tokens.spacingVerticalM,
  // 与 SectionCard 卡头同一 24px 内缩。
  ...shorthands.padding(tokens.spacingVerticalL, tokens.spacingHorizontalXXL),
  },
  // 键值行：标签固定宽度在左，值在右（长路径落在值列内换行）。
  facts: {
  ...shorthands.margin('0px'),
  'display': 'grid',
  'gap': tokens.spacingVerticalS,
  '& > div': {
    'display': 'grid',
    'gridTemplateColumns': '72px minmax(0, 1fr)',
    'columnGap': tokens.spacingHorizontalM,
    'alignItems': 'baseline',
  },
  '& dt': {
    'color': tokens.colorNeutralForeground3,
    'fontSize': tokens.fontSizeBase200,
  },
  '& dd': {
    ...shorthands.margin('0px'),
    'minWidth': '0px',
  },
  },
})
