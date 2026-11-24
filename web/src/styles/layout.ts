// 32px 天花板之外的尺寸常量。
//
// Fluent 的间距 token 最大只到 spacingHorizontalXXXL / spacingVerticalXXXL = 32px
// (实测 webLightTheme:0/2/4/6/8/10/12/16/20/24/32)。侧栏宽度、版面最大宽度、页面外边距
// 这类结构性尺寸超过 32px,只能用显式值 —— 集中在这里,别处不再出现裸 px。
//
// 同 media.ts:本文件被构建期 AOT 求值,必须保持纯 JS 语法。
export const layout = {
  // 应用外壳
  sidebarWidth: '240px',
  // 设置页里主侧栏收成只放图标的窄栏（Outlook 式两级导航的第一级）。
  navRailWidth: '56px',
  navRailHeight: '52px',
  settingsPaneWidth: '232px',
  contentMaxWidth: '1440px',
  contentMaxWideWidth: '1600px',

  // 页面度量
  pageGutter: 'clamp(28px, 4vw, 72px)',
  pageGutterWide: 'clamp(20px, 2vw, 32px)',
  readingMaxWidth: '68ch',
  // 仪表盘指标砖的列宽下限：auto-fit 按容器宽度排 3/2/1 列，
  // 避免 max-width 断点重叠时小档规则被大档覆盖。
  dashboardTileMinWidth: '260px',
  stateMinHeight: '270px',
  // 图床上传区的拖放高度（>32px，按约定集中在这里）
  imageBedDropMinHeight: '180px',
  // 状态面的内边距与品牌水印尺寸（>32px，按约定放这里）
  statePadding: 'clamp(24px, 4vw, 48px)',
  stateMaxWidth: '960px',
  stateWatermarkWidth: '300px',
  stateWatermarkHeight: '264px',
  // 空状态里的图标块（>32px，按约定放这里）
  stateIconSize: '48px',

  // 公开网盘首页：正文栏与右侧等距立方体场的尺寸（>32px）
  entryCopyMaxWidth: '62ch',
  entryFieldWidth: '360px',
  entryFieldHeight: '296px',

  // 工作台首页（/app）：报头节奏、双栏与右侧几何场（>32px）
  appHomeHeaderGap: '40px',
  appHomeColumnGap: '48px',
  appHomeSectionGap: '40px',
  appHomeStageWidth: '300px',
  appHomeStageHeight: '216px',
  appHomeSearchMaxWidth: '420px',
  appHomeSearchHeight: '40px',
  appHomeSearchHeightTouch: '44px',
  appHomeSourceColumnMinWidth: '320px',
  appHomeSourceCardMinWidth: '280px',
  appHomeSourcePlateMinHeight: '176px',
  appHomeSourceGlyphSize: '44px',
  appHomePanelHeight: 'clamp(320px, calc(100dvh - 420px), 500px)',
  appHomePanelHeightCompact: '360px',
  appHomeRecentListMaxHeight: '560px',
  appHomeRecentEmptyMinHeight: '360px',
  appHomeRecentActionSize: '36px',
  appHomeRecentActionSizeTouch: '44px',
  appHomeRecentIconPx: 36,
  appHomeRecentSkeletonMetaWidth: '96px',
  appHomeEmptyArtWidth: '200px',
  appHomeEmptyArtHeight: '145px',
  appHomeOnboardingMinHeight: '300px',
  appHomeOnboardingArtWidth: '220px',
  appHomeOnboardingArtHeight: '160px',

  // 弹层。数值用 px 结尾的数字形式：同一常量既喂 CSS（`${x}px`），
  // 也喂 Fluent 组件的数值 props（如 Toaster 的 offset），只有一处事实。
  dialogWidthPx: 480,
  dialogWideWidthPx: 640,
  // 短表单弹窗的内容区最小高度：标题与按钮区之外仍保留呼吸空间。
  dialogMinContentHeight: '140px',
  toastMaxWidthPx: 420,
  toastOffsetPx: 24,
  toastGapPx: 8,
  // 浮层与视口边缘的最小留白（窄屏上弹窗/任务卡不得贴边）。
  viewportInsetPx: 32,

  // 堆叠层级(仅外壳与浮层允许,其余页面写 zIndex 说明堆叠模型已被破坏)
  // 预览弹层的 40 在 global.css 的 --omnistore-z-preview（plain CSS 读不到本文件）。
  zIndexSidebar: 20,
  zIndexDialog: 40,
  zIndexTaskLayer: 50,
}
