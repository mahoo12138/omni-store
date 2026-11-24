import { useEffect, useRef, useState } from 'react'
import { mergeClasses } from '@fluentui/react-components'
import type { UserSource } from '../../api/sources'
import { IconChevronRight, IconFolderFilled } from '../../components/ui/Icon'
import { useStyles } from '../AppHome.styles'

function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() => (
    typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
  ))

  useEffect(() => {
    if (typeof window === 'undefined') return
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => setReduced(mq.matches)
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
  }, [])

  return reduced
}

function applyEdgeScale(viewport: HTMLDivElement, containers: Array<HTMLElement | null>, active: boolean) {
  for (const container of containers) {
    if (!container) continue
    for (const child of Array.from(container.children)) {
      if (!(child instanceof HTMLElement)) continue
      if (!active) {
        child.style.scale = ''
        continue
      }
      const center = child.offsetTop - viewport.scrollTop + child.offsetHeight / 2
      const zone = child.offsetHeight * 0.45 + 36
      const topFactor = Math.max(0, Math.min(1, center / zone))
      const bottomFactor = Math.max(0, Math.min(1, (viewport.clientHeight - center) / zone))
      const visibility = Math.min(topFactor, bottomFactor)
      const scale = 0.94 + 0.06 * visibility
      child.style.scale = visibility >= 0.999 ? '' : scale.toFixed(4)
    }
  }
}

function capabilities(source: UserSource): string {
  const parts = [source.permission === 'read_write' ? '读写' : '只读']
  if (source.webdav_enabled) parts.push('WebDAV')
  if (source.s3_enabled) parts.push('S3')
  return parts.join(' · ')
}

export function LoopingSourceList({
  sources,
  onOpen,
}: {
  sources: UserSource[]
  onOpen: (sourceKey: string) => void
}) {
  const css = useStyles()
  const viewportRef = useRef<HTMLDivElement>(null)
  const primaryRef = useRef<HTMLDivElement>(null)
  const cloneRef = useRef<HTMLDivElement>(null)
  const virtualScrollRef = useRef(0)
  const targetScrollRef = useRef(0)
  const rafRef = useRef<number | null>(null)
  const programmaticRef = useRef(false)
  const [overflows, setOverflows] = useState(false)
  const reducedMotion = useReducedMotion()

  useEffect(() => {
    const viewport = viewportRef.current
    const primary = primaryRef.current
    if (!viewport || !primary) return

    const measure = () => {
      // 只有当原始列表超出固定容器高度时才开启循环滚动。
      const next = primary.offsetHeight > viewport.clientHeight + 4
      setOverflows(next)
    }
    measure()

    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(viewport)
    observer.observe(primary)
    return () => observer.disconnect()
  }, [sources.length])

  const canLoop = overflows && !reducedMotion

  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const stopRaf = () => {
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }

    if (!canLoop) {
      stopRaf()
      viewport.scrollTop = 0
      virtualScrollRef.current = 0
      targetScrollRef.current = 0
      applyEdgeScale(viewport, [primaryRef.current, cloneRef.current], false)
      return
    }

    virtualScrollRef.current = viewport.scrollTop
    targetScrollRef.current = viewport.scrollTop
    applyEdgeScale(viewport, [primaryRef.current, cloneRef.current], true)

    let lastTime = performance.now()
    const step = (now: number) => {
      const dt = Math.min(now - lastTime, 64)
      lastTime = now
      const primary = primaryRef.current
      const clone = cloneRef.current
      if (!primary || !clone) {
        rafRef.current = null
        return
      }
      const cycleHeight = clone.offsetTop - primary.offsetTop
      if (cycleHeight <= 0) {
        rafRef.current = null
        return
      }

      const diff = targetScrollRef.current - virtualScrollRef.current
      if (Math.abs(diff) < 0.35) {
        virtualScrollRef.current = targetScrollRef.current
        rafRef.current = null
      } else {
        const alpha = 1 - Math.exp(-dt * 0.015)
        virtualScrollRef.current += diff * alpha
        rafRef.current = requestAnimationFrame(step)
      }

      while (virtualScrollRef.current >= cycleHeight) {
        virtualScrollRef.current -= cycleHeight
        targetScrollRef.current -= cycleHeight
      }
      while (virtualScrollRef.current < 0) {
        virtualScrollRef.current += cycleHeight
        targetScrollRef.current += cycleHeight
      }

      programmaticRef.current = true
      viewport.scrollTop = virtualScrollRef.current
      applyEdgeScale(viewport, [primary, clone], true)
    }

    const onWheel = (event: WheelEvent) => {
      if (Math.abs(event.deltaX) > Math.abs(event.deltaY)) return
      const primary = primaryRef.current
      const clone = cloneRef.current
      if (!primary || !clone) return
      const cycleHeight = clone.offsetTop - primary.offsetTop
      if (cycleHeight <= 0) return

      event.preventDefault()
      const unit = event.deltaMode === 1 ? 32 : event.deltaMode === 2 ? viewport.clientHeight : 1
      targetScrollRef.current += event.deltaY * unit

      if (rafRef.current === null) {
        lastTime = performance.now()
        rafRef.current = requestAnimationFrame(step)
      }
    }

    viewport.addEventListener('wheel', onWheel, { passive: false })
    return () => {
      viewport.removeEventListener('wheel', onWheel)
      stopRaf()
    }
  }, [canLoop, sources.length])

  function handleNativeScroll() {
    const viewport = viewportRef.current
    if (!viewport) return
    if (programmaticRef.current) {
      programmaticRef.current = false
      return
    }
    const primary = primaryRef.current
    const clone = cloneRef.current
    if (canLoop && primary && clone) {
      const cycleHeight = clone.offsetTop - primary.offsetTop
      if (cycleHeight > 0 && viewport.scrollTop >= cycleHeight) {
        programmaticRef.current = true
        viewport.scrollTop -= cycleHeight
      }
    }
    virtualScrollRef.current = viewport.scrollTop
    targetScrollRef.current = viewport.scrollTop
    applyEdgeScale(viewport, [primaryRef.current, cloneRef.current], canLoop)
  }

  function renderCardContent(source: UserSource, index: number) {
    return (
      <>
        <span className={css.sourceTop}>
          <span className={css.sourceGlyph} data-source-glyph aria-hidden="true">
            <IconFolderFilled size={24} />
          </span>
          <span className={css.sourceIndex} data-source-index aria-hidden="true">
            {String(index + 1).padStart(2, '0')}
          </span>
        </span>
        <span className={css.sourceName} data-source-name>{source.name}</span>
        <span
          className={mergeClasses(css.sourceDescription, 'omnistore-clamp-2')}
          data-source-desc
        >
          {source.description || '本地目录'}
        </span>
        <span className={css.sourceFoot} data-source-foot>
          <span className={css.sourceCaps}>{capabilities(source)}</span>
          <span className={css.openAction} data-open-action>
            打开 <IconChevronRight size={14} />
          </span>
        </span>
      </>
    )
  }

  return (
    <div
      ref={viewportRef}
      className={css.sourceViewport}
      data-loop={canLoop ? '' : undefined}
      onScroll={handleNativeScroll}
    >
      <div className={css.sourceViewportInner}>
        <div ref={primaryRef} className={css.sourceList}>
          {sources.map((source, index) => (
            <button
              type="button"
              key={source.key}
              className={css.sourcePlate}
              style={{ animationDelay: `${140 + Math.min(index, 8) * 60}ms` }}
              onClick={() => onOpen(source.key)}
              aria-label={`打开存储源 ${source.name}`}
            >
              {renderCardContent(source, index)}
            </button>
          ))}
        </div>
        {canLoop ? (
          // 循环衔接副本：纯视觉占位，从读屏与角色树中隐藏，确保「打开存储源 {name}」唯一可定位。
          <div ref={cloneRef} className={css.sourceList} aria-hidden="true">
            {sources.map((source, index) => (
              <div
                key={`clone-${source.key}`}
                role="presentation"
                className={css.sourcePlate}
                data-clone=""
                onClick={() => onOpen(source.key)}
              >
                {renderCardContent(source, index)}
              </div>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  )
}
