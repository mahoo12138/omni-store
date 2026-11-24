import { makeStyles, shorthands, tokens } from '@fluentui/react-components'
import { media } from '../../../styles/media'
import { useSharedStyles } from '../shared.styles'

export const useSectionStyles = makeStyles({
  'protocolControl': {
  'display': 'flex',
  'alignItems': 'center',
  ...shorthands.gap('8px'),
  'flexShrink': 0,
  },
  'staticBaseUrlBar': {
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'space-between',
  ...shorthands.gap('12px'),
  ...shorthands.padding('10px', '12px'),
  ...shorthands.borderWidth('1px'), ...shorthands.borderStyle('solid'), ...shorthands.borderColor('var(--colorNeutralStroke2)'),
  ...shorthands.borderRadius('var(--borderRadiusMedium)'),
  'backgroundColor': 'var(--colorNeutralBackground2)',
  'minWidth': '0px',
  },
  'staticBaseUrlMeta': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('2px'),
  'minWidth': '0px',
  },
  'staticBaseUrlCode': {
  'fontFamily': 'var(--fontFamilyMonospace)',
  'fontSize': '12px',
  'lineHeight': 1.5,
  'color': 'var(--colorNeutralForeground1)',
  'overflowWrap': 'anywhere',
  },
  'protocolCopy': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('4px'),
  'minWidth': '0px',
  },
  'protocolList': {
  'display': 'flex',
  'flexDirection': 'column',
  },
  'protocolName': {
  'fontSize': '14px',
  'fontWeight': 600,
  'color': 'var(--colorNeutralForeground1)',
  },
  'protocolRow': {
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'space-between',
  ...shorthands.gap('16px'),
  ...shorthands.padding('16px', '0'),
  'borderBottomWidth': '1px', 'borderBottomStyle': 'solid', 'borderBottomColor': 'var(--colorNeutralStroke2)',
  '&:first-child': {
    'paddingTop': '0px',
  },
  '&:last-child': {
    ...shorthands.borderBottom('0px'),
    'paddingBottom': '0px',
  },
  },
  sourceActionRow: {
    'display': 'flex',
    'flexWrap': 'nowrap',
    'alignItems': 'center',
    ...shorthands.gap(tokens.spacingHorizontalXS),
  },
  // 间距全部由外层容器给：TabList 与 Tabs.Panel 不再被注入外观，
  // Fluent tab 保持默认观感，panel 的 hidden 也不会被 display 打败。
  'sourceConfig': {
  'minWidth': '0px',
  'flexShrink': 0,
  'display': 'grid',
  ...shorthands.gap('var(--spacingVerticalXXL)'),
  },
  'sourceConfigNote': {
  ...shorthands.margin('0px'),
  'fontSize': '12px',
  'lineHeight': 1.6,
  'color': 'var(--colorNeutralForeground2)',
  },
  'sourceConfigPanelBody': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('var(--spacingVerticalXXL)'),
  'minWidth': '0px',
  },
  // 两行截断走 global.css 的 omnistore-clamp-2(与 className 组合使用)
  'sourceConfigPath': {

  'display': '-webkit-box',
  'overflow': 'hidden',
  'fontFamily': 'var(--fontFamilyMonospace)',
  'fontSize': '12px',
  'lineHeight': 1.6,
  'overflowWrap': 'anywhere',
  },
  'sourceConfigTitle': {

  'display': '-webkit-box',
  'overflow': 'hidden',
  'overflowWrap': 'anywhere',
  },
  sourceCellMuted: {
    'color': 'var(--colorNeutralForeground2)',
  },
  sourceCellName: {
    'fontWeight': 600,
    'overflow': 'hidden',
    'textOverflow': 'ellipsis',
    'whiteSpace': 'nowrap',
  },
  sourceCellPath: {
    'display': 'block',
    'color': 'var(--colorNeutralForeground2)',
    'fontFamily': tokens.fontFamilyMonospace,
    'fontSize': tokens.fontSizeBase200,
    'lineHeight': tokens.lineHeightBase200,
    'overflow': 'hidden',
    'textOverflow': 'ellipsis',
    'whiteSpace': 'nowrap',
  },
  sourceIdentityCell: {
    'maxWidth': '380px',
  },
  'sourcePreview': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('16px'),
  ...shorthands.padding('16px'),
  ...shorthands.borderWidth('1px'), ...shorthands.borderStyle('solid'), ...shorthands.borderColor('var(--colorNeutralStroke2)'),
  ...shorthands.borderRadius('var(--borderRadiusMedium)'),
  'backgroundColor': 'var(--colorNeutralBackground2)',
  },
  'sourcePreviewEmpty': {
  ...shorthands.margin('0px'),
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '13px',
  },
  'sourcePreviewEntries': {
  'maxHeight': '190px',
  'overflowY': 'auto',
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  'borderBottomWidth': '1px', 'borderBottomStyle': 'solid', 'borderBottomColor': 'var(--colorNeutralStroke2)',
  },
  'sourcePreviewEntry': {
  'display': 'grid',
  'gridTemplateColumns': 'minmax(0, 1fr) auto',
  'alignItems': 'center',
  ...shorthands.gap('8px'),
  'minHeight': '36px',
  ...shorthands.padding('6px', '4px'),
  'borderBottomWidth': '1px', 'borderBottomStyle': 'solid', 'borderBottomColor': 'var(--colorNeutralStroke2)',
  'color': 'var(--colorNeutralForeground1)',
  'fontFamily': 'var(--fontFamilyMonospace)',
  'fontSize': '12px',
  '&:last-child': {
    ...shorthands.borderBottom('none'),
  },
  },
  'sourcePreviewEntryName': {

  'overflow': 'hidden',
  ...shorthands.overflow('hidden'),
  'textOverflow': 'ellipsis',
  'whiteSpace': 'nowrap',
  },
  'sourcePreviewEyebrow': {
  'display': 'block',
  'marginBottom': '4px',
  'color': 'var(--colorPaletteGreenForeground1)',
  'fontSize': '12px',
  'fontWeight': 600,
  },
  'sourcePreviewHeader': {
  'display': 'flex',
  'alignItems': 'flex-start',
  'justifyContent': 'space-between',
  ...shorthands.gap('16px'),
  },
  'sourcePreviewPath': {
  'color': 'var(--colorNeutralForeground1)',
  'fontFamily': 'var(--fontFamilyMonospace)',
  'fontSize': '13px',
  'overflowWrap': 'anywhere',
  },
  'sourcePreviewStatValue': {
  'display': 'block',
  'marginBottom': '2px',
  'color': 'var(--colorNeutralForeground1)',
  'fontSize': '16px',
  },
  'sourcePreviewStats': {

  'gridTemplateColumns': 'repeat(4, minmax(0, 1fr))',
  'display': 'grid',
  ...shorthands.gap('8px'),
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '12px',
  [media.xs]: {
    'gridTemplateColumns': 'repeat(2, minmax(0, 1fr))',
  },
  },
  'sourcePreviewWarnings': {
  ...shorthands.margin('0px'),
  'paddingLeft': '18px',
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '12px',
  'lineHeight': 1.55,
  },
  sourceProtocols: {
    'display': 'flex',
    'flexWrap': 'nowrap',
    'alignItems': 'center',
    ...shorthands.gap(tokens.spacingHorizontalXS),
  },
  'sourceQuotaInput': {
  'display': 'flex',
  'alignItems': 'center',
  ...shorthands.gap('8px'),
  },
  'staticAccessStack': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('24px'),
  'flexShrink': 0,
  'minWidth': '0px',
  'paddingTop': '24px',
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  },
  'staticProtocolActions': {
  'display': 'flex',
  'flexWrap': 'wrap',
  ...shorthands.gap('8px'),
  },
  'staticProtocolBody': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('16px'),
  'minWidth': '0px',
  },
  'staticProtocolHeader': {
  'display': 'flex',
  'flexWrap': 'wrap',
  'alignItems': 'center',
  'justifyContent': 'space-between',
  ...shorthands.gap('16px'),
  },
  'staticProtocolSection': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('16px'),
  'minWidth': '0px',
  'flexShrink': 0,
  },
  staticProtocolSettings: {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('16px'),
  'minWidth': '0px',
  'flexShrink': 0,
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  'paddingTop': '24px',
  }
})

export function useStyles() {
  return { ...useSharedStyles(), ...useSectionStyles() }
}
