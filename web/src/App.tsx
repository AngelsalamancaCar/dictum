import { useCallback, useEffect, useState } from 'react'
import { ApiError, getPackage, getToken, listPackages, setToken } from './api'
import { PackageDetail } from './components/PackageDetail'
import { PackageList } from './components/PackageList'
import type { Package, PackageSummary } from './types'
import './App.css'

export default function App() {
  const [caseId, setCaseId] = useState('')
  const [useCase, setUseCase] = useState('')
  const [status, setStatus] = useState('')
  const [token, setTokenState] = useState(getToken)

  const [packages, setPackages] = useState<PackageSummary[]>([])
  const [listLoading, setListLoading] = useState(false)
  const [listError, setListError] = useState<string | null>(null)

  const [selectedId, setSelectedId] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get('package'),
  )
  const [selectedPackage, setSelectedPackage] = useState<Package | null>(null)
  const [detailError, setDetailError] = useState<string | null>(null)

  function selectPackage(id: string) {
    setSelectedId(id)
    const url = new URL(window.location.href)
    url.searchParams.set('package', id)
    window.history.replaceState(null, '', url)
  }

  function updateToken(value: string) {
    setToken(value)
    setTokenState(value)
  }

  const refreshList = useCallback(() => {
    setListLoading(true)
    setListError(null)
    listPackages({ caseId, useCase, status })
      .then(setPackages)
      .catch((err) =>
        setListError(
          err instanceof ApiError && err.status === 401
            ? 'No autorizado: ingrese un token de acceso válido.'
            : err instanceof ApiError
              ? err.message
              : String(err),
        ),
      )
      .finally(() => setListLoading(false))
    // token isn't read here directly (api.ts reads localStorage), but a
    // token change must re-fire the fetch.
  }, [caseId, useCase, status, token])

  useEffect(() => {
    refreshList()
  }, [refreshList])

  useEffect(() => {
    if (!selectedId) {
      setSelectedPackage(null)
      return
    }
    setDetailError(null)
    getPackage(selectedId)
      .then(setSelectedPackage)
      .catch((err) => setDetailError(err instanceof ApiError ? err.message : String(err)))
  }, [selectedId])

  const refreshSelected = useCallback(() => {
    if (selectedId) {
      getPackage(selectedId)
        .then(setSelectedPackage)
        .catch((err) => setDetailError(err instanceof ApiError ? err.message : String(err)))
    }
    refreshList()
  }, [selectedId, refreshList])

  return (
    <div className="app">
      <header>
        <h1>Dictum — Administrador de paquetes</h1>
        <p className="hint">
          Gestión de paquetes preparados (prompt + contexto + esquema) para el harness de LLM externo.
        </p>
        <input
          type="password"
          placeholder="Token de acceso"
          value={token}
          onChange={(e) => updateToken(e.target.value)}
          className="mono token-input"
          autoComplete="off"
        />
      </header>

      <div className="filters">
        <input
          placeholder="ID de caso (UUID)"
          value={caseId}
          onChange={(e) => setCaseId(e.target.value)}
          className="mono"
        />
        <select value={useCase} onChange={(e) => setUseCase(e.target.value)}>
          <option value="">Todos los casos de uso</option>
          <option value="classify">classify</option>
          <option value="draft">draft</option>
          <option value="risk_explain">risk_explain</option>
          <option value="similar_explain">similar_explain</option>
        </select>
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">Todos los estados</option>
          <option value="draft">draft</option>
          <option value="ready">ready</option>
          <option value="submitted">submitted</option>
          <option value="completed">completed</option>
          <option value="failed">failed</option>
          <option value="cancelled">cancelled</option>
        </select>
        <button onClick={refreshList}>Actualizar</button>
      </div>

      <div className="layout">
        <div className="list-pane">
          <PackageList
            packages={packages}
            selectedId={selectedId}
            onSelect={selectPackage}
            loading={listLoading}
            error={listError}
          />
        </div>
        <div className="detail-pane">
          {!selectedId && <p className="hint">Seleccione un paquete de la lista para inspeccionarlo.</p>}
          {detailError && <p className="error">{detailError}</p>}
          {selectedPackage && <PackageDetail pkg={selectedPackage} onChanged={refreshSelected} />}
        </div>
      </div>
    </div>
  )
}
