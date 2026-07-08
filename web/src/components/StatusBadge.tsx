import type { PackageStatus } from '../types'

const LABELS: Record<PackageStatus, string> = {
  draft: 'Borrador',
  ready: 'Listo',
  submitted: 'Enviado',
  completed: 'Completado',
  failed: 'Fallido',
  cancelled: 'Cancelado',
}

export function StatusBadge({ status }: { status: PackageStatus }) {
  return <span className={`badge badge-${status}`}>{LABELS[status] ?? status}</span>
}
