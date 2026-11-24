import { makeStyles, shorthands, tokens } from '@fluentui/react-components'
import { media } from '../../../styles/media'
import { useSharedStyles } from '../shared.styles'

export const useSectionStyles = makeStyles({
  'accountActions': {
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'flex-end',
  'flexShrink': 0,
  },
  'accountMenuTrigger': {
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': tokens.spacingHorizontalXXXL,
  'height': tokens.spacingHorizontalXXXL,
  ...shorthands.padding('0px'),
  ...shorthands.borderWidth('0px'), ...shorthands.borderStyle('solid'),
  ...shorthands.borderRadius(tokens.borderRadiusMedium),
  'backgroundColor': 'transparent',
  'color': tokens.colorNeutralForeground2,
  'cursor': 'pointer',
  '&:hover': {
    'backgroundColor': tokens.colorNeutralBackground1Hover,
    'color': tokens.colorNeutralForeground1,
  },
  '&:focus-visible': {
    'outline': `${tokens.strokeWidthThick} solid ${tokens.colorStrokeFocus2}`,
    'outlineOffset': '2px',
  },
  },
  'accountAvatar': {
  'display': 'grid',
  'placeItems': 'center',
  'width': '52px',
  'height': '52px',
  'flexShrink': 0,
  ...shorthands.borderRadius('var(--borderRadiusMedium)'),
  'backgroundColor': 'var(--colorBrandBackground2)',
  'color': 'var(--colorBrandForeground1)',
  'fontSize': '20px',
  'fontWeight': 700,
  },
  'accountCopy': {
  'minWidth': '0px',
  },
  'accountIdentity': {
  'display': 'flex',
  'alignItems': 'center',
  ...shorthands.gap('16px'),
  'minWidth': '0px',
  },
  'accountMeta': {
  'display': 'flex',
  'alignItems': 'center',
  'flexWrap': 'wrap',
  ...shorthands.gap('8px'),
  ...shorthands.margin('6px', '0', '0'),
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '13px',
  },
  'accountName': {
  ...shorthands.margin('0px'),
  'color': 'var(--colorNeutralForeground1)',
  'fontSize': '20px',
  'lineHeight': 1.25,
  'letterSpacing': '-0.02em',
  'overflowWrap': 'anywhere',
  },
  'accountPanel': {

  'gridTemplateColumns': 'minmax(0, 1fr) auto',
  'display': 'grid',
  'alignItems': 'center',
  ...shorthands.gap('24px'),
  ...shorthands.padding('24px'),
  'backgroundColor': 'var(--colorNeutralBackground1)',
  ...shorthands.borderWidth('1px'), ...shorthands.borderStyle('solid'), ...shorthands.borderColor('var(--colorNeutralStroke2)'),
  ...shorthands.borderRadius('var(--borderRadiusLarge)'),
  },
  'bucketList': {
  'display': 'flex',
  'flexDirection': 'column',
  },
  'bucketRow': {

  'gridTemplateColumns': 'minmax(0, 1fr) auto',
  'display': 'grid',
  'alignItems': 'center',
  ...shorthands.gap('16px'),
  'minHeight': '60px',
  ...shorthands.padding('8px', '0'),
  'borderBottomWidth': '1px', 'borderBottomStyle': 'solid', 'borderBottomColor': 'var(--colorNeutralStroke2)',
  '&:last-child': {
    ...shorthands.borderBottom('none'),
  },
  [media.xs]: {
    'gridTemplateColumns': 'minmax(0, 1fr)',
  },
  },
  'credentialBody': {
  'marginTop': '24px',
  },
  'credentialFact': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('4px'),
  'minWidth': '0px',
  },
  'credentialFactLabel': {
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '12px',
  'fontWeight': 500,
  },
  'credentialFactValue': {
  'color': 'var(--colorNeutralForeground1)',
  'fontFamily': 'var(--fontFamilyMonospace)',
  'fontSize': '12px',
  'lineHeight': 1.5,
  'overflowWrap': 'anywhere',
  },
  'credentialFacts': {

  'gridTemplateColumns': 'repeat(3, minmax(0, 1fr))',
  'display': 'grid',
  ...shorthands.gap('16px'),
  'paddingTop': '16px',
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  [media.md]: {
    'gridTemplateColumns': 'repeat(2, minmax(0, 1fr))',
  },
  [media.xs]: {
    'gridTemplateColumns': 'minmax(0, 1fr)',
  },
  },
  'credentialGroup': {
  'minWidth': '0px',
  },
  'credentialGroupAction': {

  'justifyContent': 'flex-end',
  'display': 'flex',
  'flexShrink': 0,
  [media.sm]: {
    'justifyContent': 'flex-start',
  },
  },
  'credentialGroupCopy': {
  'minWidth': '0px',
  },
  'credentialGroupHeader': {

  'gridTemplateColumns': 'minmax(0, 1fr) auto',
  'display': 'grid',
  ...shorthands.gap('16px'),
  'alignItems': 'start',
  [media.sm]: {
    'gridTemplateColumns': 'minmax(0, 1fr)',
  },
  },
  'credentialGroupHint': {
  ...shorthands.margin('4px', '0', '0'),
  'maxWidth': '64ch',
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '13px',
  'lineHeight': 1.55,
  },
  'credentialItem': {

  'gridTemplateColumns': 'minmax(180px, 1fr) minmax(170px, 0.75fr) auto',
  'alignItems': 'center',
  'display': 'grid',
  ...shorthands.gap('16px'),
  'minHeight': '72px',
  ...shorthands.padding('16px', '0'),
  'borderBottomWidth': '1px', 'borderBottomStyle': 'solid', 'borderBottomColor': 'var(--colorNeutralStroke2)',
  '&:last-child': {
    ...shorthands.borderBottom('none'),
  },
  [media.md]: {
    'gridTemplateColumns': 'minmax(0, 1fr) auto',
  },
  [media.xs]: {
    'gridTemplateColumns': 'minmax(0, 1fr)',
    'alignItems': 'start',
  },
  },
  'credentialItemActions': {
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'flex-end',
  ...shorthands.gap('4px'),
  },
  'credentialItemId': {
  'color': 'var(--colorNeutralForeground2)',
  'fontFamily': 'var(--fontFamilyMonospace)',
  'fontSize': '12px',
  'overflowWrap': 'anywhere',
  },
  'credentialItemIdentity': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('4px'),
  'minWidth': '0px',
  },
  'credentialItemMeta': {
  'display': 'flex',
  'flexDirection': 'column',
  'alignItems': 'flex-start',
  ...shorthands.gap('4px'),
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '12px',
  'overflowWrap': 'anywhere',
  [media.md]: {
    'gridColumn': '1',
    'gridRow': 2,
  },
  [media.xs]: {
    'gridColumn': '1',
    'gridRow': 'auto',
  },
  },
  'credentialItemName': {
  'color': 'var(--colorNeutralForeground1)',
  'fontSize': '13px',
  'fontWeight': 600,
  },
  'credentialList': {
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  },
  'credentialModalContent': {
  'minWidth': '0px',
  },
  'credentialOverview': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('4px'),
  // 行内 12px + 容器 12px = 卡头 24px 的内容基线。
  ...shorthands.padding('8px', '12px'),
  // 悬停时让全组发丝线退场，hover 面成为唯一焦点。
  '&:hover [data-credential-row]::after, &:focus-within [data-credential-row]::after': {
    'opacity': 0,
  },
  },
  'credentialOverviewCopy': {
  'gridArea': 'copy',
  'display': 'flex',
  'alignItems': 'center',
  'flexWrap': 'wrap',
  'columnGap': '8px',
  'rowGap': '2px',
  'minWidth': '0px',
  },
  'credentialOverviewMetaGroup': {
  'display': 'inline-flex',
  'alignItems': 'center',
  ...shorthands.gap('8px'),
  'minWidth': '0px',
  },
  'credentialOverviewDot': {
  'alignSelf': 'center',
  'width': '3px',
  'height': '3px',
  'flexShrink': 0,
  ...shorthands.borderRadius('999px'),
  'backgroundColor': tokens.colorNeutralStroke1,
  },
  'credentialOverviewStatus': {
  'gridArea': 'status',
  'justifySelf': 'end',
  'display': 'inline-flex',
  'alignItems': 'center',
  ...shorthands.gap('6px'),
  'fontSize': tokens.fontSizeBase200,
  'lineHeight': tokens.lineHeightBase200,
  'fontWeight': tokens.fontWeightMedium,
  'whiteSpace': 'nowrap',
  },
  'credentialOverviewStatusActive': {
  'color': tokens.colorPaletteGreenForeground1,
  },
  'credentialOverviewStatusMuted': {
  'color': tokens.colorNeutralForeground2,
  'fontWeight': tokens.fontWeightRegular,
  },
  'credentialOverviewStatusError': {
  'color': tokens.colorPaletteRedForeground1,
  },
  'credentialOverviewChevron': {
  'gridArea': 'chevron',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': '20px',
  'height': '20px',
  'color': tokens.colorNeutralForeground2,
  'transitionProperty': 'color, transform',
  'transitionDuration': tokens.durationNormal,
  'transitionTimingFunction': tokens.curveDecelerateMax,
  },
  // 32px 实底图标位：与卡头同一种语言，三种协议各用一支 Fluent 色区分身份。
  'credentialOverviewIcon': {
  'gridArea': 'icon',
  'alignSelf': 'center',
  'display': 'grid',
  'placeItems': 'center',
  'width': tokens.spacingHorizontalXXXL,
  'height': tokens.spacingHorizontalXXXL,
  'flexShrink': 0,
  ...shorthands.borderRadius(tokens.borderRadiusMedium),
  '&[data-kind="webdav"]': {
    'backgroundColor': tokens.colorPaletteBlueBackground2,
    'color': tokens.colorPaletteBlueForeground2,
  },
  '&[data-kind="image-api"]': {
    'backgroundColor': tokens.colorPaletteLavenderBackground2,
    'color': tokens.colorPaletteLavenderForeground2,
  },
  '&[data-kind="s3"]': {
    'backgroundColor': tokens.colorPaletteLightTealBackground2,
    'color': tokens.colorPaletteLightTealForeground2,
  },
  '&[data-kind="static-http"]': {
    'backgroundColor': tokens.colorPaletteGreenBackground2,
    'color': tokens.colorPaletteGreenForeground2,
  },
  },
  'credentialOverviewItem': {
  'position': 'relative',
  'gridTemplateColumns': '32px minmax(0, 1fr) auto 20px',
  'gridTemplateAreas': '"icon copy status chevron"',
  'display': 'grid',
  'alignItems': 'center',
  ...shorthands.gap('12px'),
  'minWidth': '0px',
  'minHeight': '64px',
  ...shorthands.padding('14px', '12px'),
  'backgroundColor': 'transparent',
  'color': tokens.colorNeutralForeground1,
  ...shorthands.border('0px', 'solid'),
  ...shorthands.borderRadius(tokens.borderRadiusMedium),
  'fontFamily': 'var(--fontFamilyBase)',
  'textAlign': 'left',
  'cursor': 'pointer',
  'transitionProperty': 'background-color',
  'transitionDuration': tokens.durationNormal,
  'transitionTimingFunction': tokens.curveDecelerateMax,
  // 分隔线从标题列起，不穿过图标；悬停面的圆角不被线切断。
  '&:not(:last-child)::after': {
    'content': '""',
    'position': 'absolute',
    'insetInlineStart': '56px',
    'insetInlineEnd': '12px',
    'bottom': '0px',
    'height': tokens.strokeWidthThin,
    'backgroundColor': tokens.colorNeutralStroke2,
    'pointerEvents': 'none',
    'transitionProperty': 'opacity',
    'transitionDuration': tokens.durationFaster,
    'transitionTimingFunction': tokens.curveLinear,
  },
  '&:hover, &:focus-visible': {
    'backgroundColor': tokens.colorNeutralBackground1Hover,
  },
  '&:active': {
    'backgroundColor': tokens.colorNeutralBackground1Pressed,
  },
  '&:hover [data-credential-chevron], &:focus-visible [data-credential-chevron]': {
    'color': tokens.colorNeutralForeground1,
    'transform': 'translateX(2px)',
  },
  '&:focus-visible': {
    'outline': `2px solid ${tokens.colorStrokeFocus2}`,
    'outlineOffset': '-2px',
  },
  // 手机：状态落到副标题下方；图标与箭头跨两行居中。
  [media.xs]: {
    'gridTemplateColumns': '32px minmax(0, 1fr) 20px',
    'gridTemplateAreas': '"icon copy chevron" "icon status chevron"',
    'rowGap': tokens.spacingVerticalXS,
    'padding': '12px',
    '& [data-credential-status]': {
      'justifySelf': 'start',
    },
  },
  },
  'credentialOverviewMeta': {
  'color': tokens.colorNeutralForeground2,
  'fontSize': tokens.fontSizeBase200,
  'lineHeight': tokens.lineHeightBase200,
  },
  'credentialOverviewTitle': {
  'color': tokens.colorNeutralForeground1,
  'fontSize': tokens.fontSizeBase300,
  'lineHeight': tokens.lineHeightBase300,
  'fontWeight': tokens.fontWeightSemibold,
  },
  'credentialStatusError': {
  'color': 'var(--colorPaletteRedForeground1)',
  'fontSize': '12px',
  'fontWeight': 600,
  },
  'credentialSubsection': {
  'marginTop': '24px',
  'paddingTop': '24px',
  'borderTopWidth': '1px', 'borderTopStyle': 'solid', 'borderTopColor': 'var(--colorNeutralStroke2)',
  },
  'credentialSubsectionHeader': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('4px'),
  'marginBottom': '8px',
  },
  'credentialSubsectionTitle': {
  ...shorthands.margin('0px'),
  'color': 'var(--colorNeutralForeground1)',
  'fontSize': '13px',
  'fontWeight': 600,
  },
  'credentialTitleLine': {
  'display': 'flex',
  'alignItems': 'center',
  'flexWrap': 'wrap',
  ...shorthands.gap('8px'),
  'minHeight': '24px',
  },
  'eyebrow': {
  'display': 'block',
  'marginBottom': '5px',
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '12px',
  'fontWeight': 600,
  'letterSpacing': '0.06em',
  'textTransform': 'uppercase',
  },
  'metaDivider': {
  'width': '3px',
  'height': '3px',
  ...shorthands.borderRadius('999px'),
  'backgroundColor': 'var(--colorNeutralStroke1)',
  },
  'profilePage': {
  'display': 'flex',
  'flexDirection': 'column',
  ...shorthands.gap('24px'),
  },
  'statusBadge': {
  'display': 'inline-flex',
  'alignItems': 'center',
  ...shorthands.gap('6px'),
  'color': 'var(--colorPaletteGreenForeground1)',
  'fontSize': '12px',
  'fontWeight': 600,
  },
  'statusBadgeMuted': {
  'color': 'var(--colorNeutralForeground2)',
  'fontSize': '12px',
  'fontWeight': 600,
  },
  'statusDotSmall': {
  'width': '6px',
  'height': '6px',
  ...shorthands.borderRadius('999px'),
  'backgroundColor': 'var(--colorPaletteGreenForeground1)',
  },
  'tokenRevealRow': {
  'display': 'flex',
  'alignItems': 'center',
  ...shorthands.gap('8px'),
  'minWidth': '0px',
  [media.xs]: {
    'flexDirection': 'column',
    'alignItems': 'stretch',
  },
  },
  'tokenRevealInput': {
  'flex': '1 1 0%',
  'minWidth': '0px',
  '& input': {
    'fontFamily': 'var(--fontFamilyMonospace)',
  },
  }
})

export function useStyles() {
  return { ...useSharedStyles(), ...useSectionStyles() }
}
