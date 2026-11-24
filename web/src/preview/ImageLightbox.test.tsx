import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import {
  clampLightboxZoom,
  ImageLightbox,
  MAX_LIGHTBOX_ZOOM,
  MIN_LIGHTBOX_ZOOM,
  stepLightboxZoom,
} from './ImageLightbox'

describe('ImageLightbox', () => {
  it('clamps and steps zoom within bounds', () => {
    expect(clampLightboxZoom(0.01)).toBe(MIN_LIGHTBOX_ZOOM)
    expect(clampLightboxZoom(99)).toBe(MAX_LIGHTBOX_ZOOM)
    expect(stepLightboxZoom(1, 'in')).toBe(1.25)
    expect(stepLightboxZoom(1, 'out')).toBe(0.8)
  })

  it('renders accessible dialog with zoom controls and image', () => {
    const html = renderToStaticMarkup(
      <ImageLightbox src="/i/demo.png" alt="示例图片.png" onClose={() => undefined} />,
    )
    expect(html).toContain('role="dialog"')
    expect(html).toContain('aria-modal="true"')
    expect(html).toContain('aria-label="放大查看 示例图片.png"')
    expect(html).toContain('aria-label="放大"')
    expect(html).toContain('aria-label="缩小"')
    expect(html).toContain('aria-label="重置缩放"')
    expect(html).toContain('aria-label="关闭放大查看"')
    expect(html).toContain('src="/i/demo.png"')
    expect(html).toContain('100%')
  })
})
