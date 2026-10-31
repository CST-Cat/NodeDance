import { useLayoutEffect, useRef, useState, type ReactNode } from "react"
import { createPortal } from "react-dom"
import createGlobe, { type COBEOptions, type Globe as CobeGlobe } from "cobe"
import { useMotionValue, useSpring } from "motion/react"

import { __classes as cn } from "@/state"

const MOVEMENT_DAMPING = 1400
const GLOBE_CONFIG: COBEOptions = {
  width: 800, height: 800, devicePixelRatio: 2,
  phi: 0, theta: 0.3, dark: 0, diffuse: 0.4,
  mapSamples: 16000, mapBrightness: 1.2, mapBaseBrightness: 0,
  baseColor: [1, 1, 1], markerColor: [251 / 255, 100 / 255, 21 / 255],
  glowColor: [1, 1, 1], markers: [], arcs: [],
}

/** The shadcn Globe with its existing pointer/spring behavior, using COBE v2's
 * update lifecycle. Native options pass through unchanged; children share COBE's
 * wrapper so CSS-anchored shadcn labels can follow marker and arc ids. */
export function Globe({ className, config = GLOBE_CONFIG, onFrame, children }: {
  className?: string
  config?: COBEOptions
  onFrame?: (camera: { phi: number; theta: number }) => void
  children?: ReactNode
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const globeRef = useRef<CobeGlobe | null>(null)
  const phiRef = useRef(config.phi)
  const pointerInteracting = useRef<number | null>(null)
  const current = useRef({ config, onFrame })
  current.current = { config, onFrame }
  const [portal, setPortal] = useState<HTMLElement | null>(null)
  const r = useMotionValue(0)
  const rs = useSpring(r, { mass: 1, damping: 30, stiffness: 100 })

  const updatePointerInteraction = (value: number | null) => {
    pointerInteracting.current = value
    if (canvasRef.current) canvasRef.current.style.cursor = value !== null ? "grabbing" : "grab"
  }
  const updateMovement = (clientX: number) => {
    if (pointerInteracting.current !== null) {
      const delta = clientX - pointerInteracting.current
      r.set(r.get() + delta / MOVEMENT_DAMPING)
    }
  }

  useLayoutEffect(() => {
    const canvas = canvasRef.current!, host = canvas.parentElement!
    // COBE returns a silent no-op without WebGL. Let the page's existing error
    // boundary show the renderer-unavailable state instead of a blank canvas.
    const context = { alpha: true, stencil: false, antialias: true, depth: false, preserveDrawingBuffer: false, ...current.current.config.context }
    if (!canvas.getContext("webgl2", context) && !canvas.getContext("webgl", context)) throw new Error("WebGL unavailable")
    const size = canvas.offsetWidth
    const globe = createGlobe(canvas, { ...current.current.config, width: size, height: size })
    globeRef.current = globe
    const wrapper = canvas.parentElement!
    setPortal(wrapper)
    const resize = new ResizeObserver(() => {
      const width = canvas.offsetWidth
      if (width > 0) globe.update({ width, height: width })
    })
    resize.observe(host)
    let frame = 0
    const render = () => {
      if (!pointerInteracting.current) phiRef.current += 0.0025
      const camera = { phi: phiRef.current + rs.get(), theta: current.current.config.theta }
      globe.update(camera)
      current.current.onFrame?.(camera)
      canvas.style.opacity = "1"
      frame = requestAnimationFrame(render)
    }
    frame = requestAnimationFrame(render)
    return () => {
      cancelAnimationFrame(frame)
      resize.disconnect()
      globeRef.current = null
      globe.destroy()
      // v2 owns its anchor wrapper but does not unwrap the canvas on destroy.
      // Restore React's original DOM ownership before React removes the canvas.
      if (wrapper !== host && wrapper.parentElement === host) {
        host.insertBefore(canvas, wrapper)
        wrapper.remove()
      }
    }
  }, [rs])

  useLayoutEffect(() => {
    globeRef.current?.update({ ...config, phi: phiRef.current + rs.get(), width: canvasRef.current!.offsetWidth, height: canvasRef.current!.offsetWidth })
  }, [config, rs])

  return <div className={cn("absolute inset-0 mx-auto aspect-square w-full max-w-150", className)}>
    <canvas className="size-full opacity-0 transition-opacity duration-500 contain-[layout_paint_size]" ref={canvasRef}
      onPointerDown={(e) => updatePointerInteraction(e.clientX)}
      onPointerUp={() => updatePointerInteraction(null)}
      onPointerOut={() => updatePointerInteraction(null)}
      onMouseMove={(e) => updateMovement(e.clientX)}
      onTouchMove={(e) => e.touches[0] && updateMovement(e.touches[0].clientX)} />
    {portal && children ? createPortal(children, portal) : null}
  </div>
}
