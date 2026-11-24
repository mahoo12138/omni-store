import { useStyles } from '../AppHome.styles'

// 等距收件盘：空态与无存储源引导共用。两张纸停在盘口上方，虚线与漂浮
// 把「文件会落到这里」画出来；纯装饰，不进读屏。
export function EmptyTray({ className }: { className: string }) {
  const css = useStyles()
  return (
    <svg
      className={className}
      viewBox="0 0 220 160"
      fill="none"
      stroke="currentColor"
      strokeOpacity="0.46"
      fillOpacity="0.06"
      aria-hidden="true"
      focusable="false"
    >
      <g className={css.sheetA}>
        <path
          className={css.ink}
          pathLength={1}
          d="M82 12 116 29 82 46 48 29Z"
          fill="currentColor"
          strokeWidth="1.2"
          strokeLinejoin="round"
        />
      </g>
      <g className={css.sheetB}>
        <path
          className={css.ink}
          pathLength={1}
          d="M142 2 170 16 142 30 114 16Z"
          strokeWidth="1.2"
          strokeLinejoin="round"
        />
      </g>
      <path
        d="M96 50v6M110 52v6M126 46v6"
        strokeWidth="1.2"
        strokeLinecap="round"
        strokeDasharray="2 4"
      />
      <path
        className={css.ink}
        pathLength={1}
        d="M110 58 182 94 110 130 38 94Z"
        fill="currentColor"
        strokeWidth="1.3"
        strokeLinejoin="round"
      />
      <path
        className={css.ink}
        pathLength={1}
        d="M38 94v24l72 36 72-36V94M110 130v24"
        strokeWidth="1.3"
        strokeLinejoin="round"
      />
      <path
        className={css.ink}
        pathLength={1}
        d="M110 72 166 100 110 128 54 100Z"
        strokeWidth="1"
        strokeOpacity="0.55"
        strokeLinejoin="round"
      />
      <path
        className={css.ink}
        pathLength={1}
        d="M110 58v14M38 94 54 100M182 94 166 100"
        strokeWidth="1"
        strokeLinecap="round"
      />
      <circle className={css.vertex} cx="38" cy="94" r="2" fill="currentColor" fillOpacity="0.5" />
      <circle className={css.vertex} cx="182" cy="94" r="2" fill="currentColor" fillOpacity="0.5" />
    </svg>
  )
}

// 报头右侧的等距几何：三只逐级升高的箱体，由两段虚线轨道串起来。
// 与公开首页的立方体场是同一母题，但排成「一排」——读起来是工作台上的存储位。
// 纯装饰，不进读屏；漂浮与自绘关键帧全部来自 global.css。
export function DriveConstellation({ className }: { className: string }) {
  const css = useStyles()
  return (
    <div className={className} aria-hidden="true">
      <span className={css.stageGlow} />
      <svg className={css.stageLattice} strokeOpacity="0.14" aria-hidden="true" focusable="false">
        <defs>
          <pattern id="omnistore-apphome-lattice" width="88" height="50" patternUnits="userSpaceOnUse">
            <path d="M44 0 88 25 44 50 0 25Z" fill="none" stroke="currentColor" strokeWidth="1" />
          </pattern>
        </defs>
        <rect width="100%" height="100%" fill="url(#omnistore-apphome-lattice)" />
      </svg>
      <div className={css.stageField}>
        <svg
          className={css.constellation}
          viewBox="0 0 340 240"
          fill="none"
          strokeOpacity="0.38"
          fillOpacity="0.045"
          aria-hidden="true"
          focusable="false"
        >
          <g className={css.bobA}>
            <path
              className={css.ink}
              pathLength={1}
              d="M54 150 94 170 54 190 14 170Z"
              fill="currentColor"
              stroke="currentColor"
              strokeWidth="1.2"
              strokeLinejoin="round"
            />
            <path
              className={css.ink}
              pathLength={1}
              d="M14 170v46l40 20 40-20v-46M54 190v46"
              stroke="currentColor"
              strokeWidth="1.2"
              strokeLinejoin="round"
            />
            <circle className={css.vertex} cx="54" cy="150" r="2" fill="currentColor" fillOpacity="0.5" />
          </g>
          <g className={css.bobB}>
            <path
              className={css.ink}
              pathLength={1}
              d="M168 110 218 135 168 160 118 135Z"
              fill="currentColor"
              stroke="currentColor"
              strokeWidth="1.2"
              strokeLinejoin="round"
            />
            <path
              className={css.ink}
              pathLength={1}
              d="M118 135v56l50 25 50-25v-56M168 160v56"
              stroke="currentColor"
              strokeWidth="1.2"
              strokeLinejoin="round"
            />
            <circle className={css.vertex} cx="168" cy="110" r="2.2" fill="currentColor" fillOpacity="0.5" />
            <circle className={css.vertex} cx="218" cy="135" r="2" fill="currentColor" fillOpacity="0.5" />
          </g>
          <g className={css.bobC}>
            <path
              className={css.ink}
              pathLength={1}
              d="M280 70 336 98 280 126 224 98Z"
              fill="currentColor"
              stroke="currentColor"
              strokeWidth="1.3"
              strokeLinejoin="round"
            />
            <path
              className={css.ink}
              pathLength={1}
              d="M224 98v64l56 28 56-28V98M280 126v64"
              stroke="currentColor"
              strokeWidth="1.3"
              strokeLinejoin="round"
            />
            <path
              className={css.ink}
              pathLength={1}
              d="M280 84 314 101 280 118 246 101Z"
              stroke="currentColor"
              strokeWidth="1.1"
              strokeLinejoin="round"
              strokeOpacity="0.34"
            />
            <circle className={css.vertex} cx="280" cy="70" r="2.4" fill="currentColor" fillOpacity="0.5" />
            <circle className={css.vertex} cx="336" cy="98" r="2" fill="currentColor" fillOpacity="0.5" />
          </g>
          <path
            className={css.rail}
            d="M94 170 118 135"
            stroke="currentColor"
            strokeWidth="1.2"
            strokeLinecap="round"
          />
          <path
            className={css.rail}
            d="M218 135 224 98"
            stroke="currentColor"
            strokeWidth="1.2"
            strokeLinecap="round"
          />
        </svg>
      </div>
    </div>
  )
}
