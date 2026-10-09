import type { NodeFileEntry } from '../api'

export function joinNodePath(parent: string, name: string): string {
  return parent === '/' ? `/${name}` : `${parent.replace(/\/$/, '')}/${name}`
}

export function parentNodePath(path: string): string | null {
  if (path === '/') return null
  const trimmed = path.replace(/\/$/, '')
  const separator = trimmed.lastIndexOf('/')
  return separator <= 0 ? '/' : trimmed.slice(0, separator)
}

export function formatFileSize(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 * 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)} MB`
  return `${(size / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

export function isEditableTextFile(entry: NodeFileEntry): boolean {
  return entry.kind === 'file' && entry.size <= 32 * 1024
}

export function fileModifiedDate(entry: NodeFileEntry): Date | null {
  if (!Number.isFinite(entry.modifiedAt) || entry.modifiedAt <= 0) return null
  const date = new Date(entry.modifiedAt / 1_000_000)
  return Number.isFinite(date.getTime()) ? date : null
}
