import type { PackageSummary } from '../types'
import { StatusBadge } from './StatusBadge'

interface Props {
  packages: PackageSummary[]
  selectedId: string | null
  onSelect: (id: string) => void
  loading: boolean
  error: string | null
}

export function PackageList({ packages, selectedId, onSelect, loading, error }: Props) {
  if (loading) return <p className="hint">Cargando paquetes…</p>
  if (error) return <p className="error">{error}</p>
  if (packages.length === 0) return <p className="hint">No hay paquetes que coincidan con los filtros.</p>

  return (
    <table className="package-table">
      <thead>
        <tr>
          <th>Caso</th>
          <th>Caso de uso</th>
          <th>Estado</th>
          <th>Versión</th>
          <th>Creado</th>
        </tr>
      </thead>
      <tbody>
        {packages.map((pkg) => (
          <tr
            key={pkg.ID}
            className={pkg.ID === selectedId ? 'selected' : ''}
            onClick={() => onSelect(pkg.ID)}
          >
            <td className="mono">{pkg.CaseID.slice(0, 8)}</td>
            <td>{pkg.UseCase}</td>
            <td>
              <StatusBadge status={pkg.Status} />
            </td>
            <td>{pkg.PromptVersion}</td>
            <td>{new Date(pkg.CreatedAt).toLocaleString('es-MX')}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
