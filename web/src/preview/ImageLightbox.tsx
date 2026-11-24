import { useCallback, useEffect, useRef, useState, type PointerEvent, type WheelEvent } from 'react'
import { createPortal } from 'react-dom'
import { IconClose } from '../components/ui/Icon'
import * as css from './Preview.classes'

export const MIN_LIGHTBOX_ZOOM = 0.25
export const MAX_LIGHTBOX_ZOOM = 6

export function clampLightboxZoom(value: number): number {
  return Math.min(MAX_LIGHTBOX_ZOOM, Math.max(MIN_LIGHTBOX_ZOOM, Math.round(value * 100) / 100))
}

export function stepLightboxZoom(current: number, direction: 'in' | 'out'): number {
  return clampLightboxZoom(direction === 'in' ? current * 1.25 : current / 1.25)
}

export interface ImageLightboxProps {
  src: string
  alt: string
  onClose: () => void
}

// 轻量原生 React 19 图片放大灯箱，替代不兼容 React 19 的 react-viewer。
// 在捕获阶段拦截 Escape，确保关闭灯箱时不会把外层 PreviewModal 一并关掉。
export function ImageLightbox({ src, alt, onClose }: ImageLightboxProps) {
  const [scale, setScale] = useState(1)
  const [offset, setOffset] = useState({ x: 0, y: 0 })
  const [dragging, setDragging] = useState(false)
  const dragOrigin = useRef<{ pointerId: number; x: number; y: number; ox: number; oy: number } | null>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  const resetView = useCallback(() => {
    setScale(1)
    setOffset({ x: 0, y: 0 })
  }, [])

  const zoomIn = useCallback(() => {
    setScale((s) => stepLightboxZoom(s, 'in'))
  }, [])

  const zoomOut = useCallback(() => {
    setScale((s) => {
      const next = stepLightboxZoom(s, 'out')
      if (next <= 1) setOffset({ x: 0, y: 0 })
      return next
    })
  }, [])

  useEffect(() => {
    closeRef.current?.focus()
  }, [])

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        event.stopPropagation()
        event.stopImmediatePropagation()
        onClose()
      } else if (event.key === '+' || event.key === '=') {
        event.preventDefault()
        zoomIn()
      } else if (event.key === '-') {
        event.preventDefault()
        zoomOut()
      } else if (event.key === '0') {
        event.preventDefault()
        resetView()
      }
    }
    window.addEventListener('keydown', onKeyDown, true)
    return () => window.removeEventListener('keydown', onKeyDown, true)
  }, [onClose, zoomIn, zoomOut, resetView])

  function handleWheel(event: WheelEvent<HTMLDivElement>) {
    event.preventDefault()
    if (event.deltaY < 0) zoomIn()
    else if (event.deltaY > 0) zoomOut()
  }

  function handlePointerDown(event: PointerEvent<HTMLImageElement>) {
    if (scale <= 1) return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    dragOrigin.current = {
      pointerId: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      ox: offset.x,
      oy: offset.y,
    }
    setDragging(true)
  }

  function handlePointerMove(event: PointerEvent<HTMLImageElement>) {
    const origin = dragOrigin.current
    if (!origin || origin.pointerId !== event.pointerId) return
    setOffset({
      x: origin.ox + (event.clientX - origin.x),
      y: origin.oy + (event.clientY - origin.y),
    })
  }

  function handlePointerEnd(event: PointerEvent<HTMLImageElement>) {
    const origin = dragOrigin.current
    if (!origin || origin.pointerId !== event.pointerId) return
    dragOrigin.current = null
    setDragging(false)
  }

  function handleDoubleClick() {
    if (scale > 1) {
      resetView()
    } else {
      setScale(2)
    }
  }

  const content = (
    <div
      className={css.lightbox}
      role="dialog"
      aria-modal="true"
      aria-label={`放大查看 ${alt}`}
      onWheel={handleWheel}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className={css.lightboxToolbar}>
        <button type="button" className={css.lightboxBtn} onClick={zoomOut} disabled={scale <= MIN_LIGHTBOX_ZOOM} aria-label="缩小">
          −
        </button>
        <button type="button" className={css.lightboxBtn} onClick={resetView} aria-label="重置缩放">
          {Math.round(scale * 100)}%
        </button>
        <button type="button" className={css.lightboxBtn} onClick={zoomIn} disabled={scale >= MAX_LIGHTBOX_ZOOM} aria-label="放大">
          +
        </button>
        <button ref={closeRef} type="button" className={css.lightboxBtn} onClick={onClose} aria-label="关闭放大查看">
          <IconClose size={18} />
        </button>
      </div>
      <div
        className={css.lightboxStage}
        onClick={(event) => {
          if (event.target === event.currentTarget) onClose()
        }}
      >
        <img
          src={src}
          alt={alt}
          draggable={false}
          className={css.lightboxImage}
          style={{
            transform: `translate3d(${offset.x}px, ${offset.y}px, 0) scale(${scale})`,
            cursor: scale > 1 ? (dragging ? 'grabbing' : 'grab') : 'zoom-in',
            transition: dragging ? 'none' : 'transform 120ms ease-out',
          }}
          onDoubleClick={handleDoubleClick}
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={handlePointerEnd}
          onPointerCancel={handlePointerEnd}
        />
      </div>
    </div>
  )

  if (typeof document === 'undefined') return content
  return createPortal(content, document.body)
}
