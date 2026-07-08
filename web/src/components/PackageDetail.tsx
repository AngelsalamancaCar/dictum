import { useState } from 'react'
import { ApiError, archiveUrl, attachResult, cancelPackage, resubmitPackage, submitPackage } from '../api'
import type { Package } from '../types'
import { StatusBadge } from './StatusBadge'

const CANCELLABLE = new Set(['draft', 'ready', 'submitted'])
const RESUBMITTABLE = new Set(['failed', 'completed', 'cancelled'])

interface Props {
  pkg: Package
  onChanged: () => void
}

export function PackageDetail({ pkg, onChanged }: Props) {
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [rawResponse, setRawResponse] = useState('')
  const [validationErrors, setValidationErrors] = useState<string[] | null>(null)
  const [attachOk, setAttachOk] = useState(false)

  async function runAction(action: () => Promise<Package>) {
    setBusy(true)
    setActionError(null)
    try {
      await action()
      onChanged()
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  async function handleAttach() {
    setValidationErrors(null)
    setAttachOk(false)
    setActionError(null)
    let parsed: unknown
    try {
      parsed = JSON.parse(rawResponse)
    } catch {
      setActionError('El resultado no es JSON válido.')
      return
    }
    setBusy(true)
    try {
      const res = await attachResult(pkg.ID, parsed)
      if (res.ValidationErrors && res.ValidationErrors.length > 0) {
        setValidationErrors(res.ValidationErrors)
      } else {
        setAttachOk(true)
        setRawResponse('')
      }
      onChanged()
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="package-detail">
      <div className="detail-header">
        <h2>
          {pkg.UseCase} <StatusBadge status={pkg.Status} />
        </h2>
        <p className="mono hint">
          {pkg.ID} · caso {pkg.CaseID} · versión de prompt {pkg.PromptVersion}
        </p>
      </div>

      <div className="actions">
        <button disabled={busy || pkg.Status !== 'ready'} onClick={() => runAction(() => submitPackage(pkg.ID))}>
          Enviar
        </button>
        <button disabled={busy || !CANCELLABLE.has(pkg.Status)} onClick={() => runAction(() => cancelPackage(pkg.ID))}>
          Cancelar
        </button>
        <button disabled={busy || !RESUBMITTABLE.has(pkg.Status)} onClick={() => runAction(() => resubmitPackage(pkg.ID))}>
          Reenviar (nuevo paquete)
        </button>
        <a className="button-link" href={archiveUrl(pkg.ID)}>
          Descargar archivo
        </a>
      </div>
      {actionError && <p className="error">{actionError}</p>}
      {pkg.Error && <p className="error">Error registrado: {pkg.Error}</p>}

      <section>
        <h3>Prompt renderizado</h3>
        <pre className="prompt-block">{pkg.Bundle.prompt}</pre>
      </section>

      <section>
        <h3>Contexto</h3>
        <pre className="json-block">{JSON.stringify(pkg.Bundle.context, null, 2)}</pre>
      </section>

      <section>
        <h3>Esquema de salida esperado</h3>
        <pre className="json-block">{JSON.stringify(pkg.Bundle.output_schema, null, 2)}</pre>
      </section>

      <section>
        <h3>Adjuntar resultado del harness</h3>
        {pkg.Status !== 'submitted' && (
          <p className="hint">Solo se puede adjuntar un resultado a un paquete en estado "Enviado".</p>
        )}
        <textarea
          rows={8}
          placeholder="Pegue aquí la respuesta JSON del harness"
          value={rawResponse}
          onChange={(e) => setRawResponse(e.target.value)}
          disabled={pkg.Status !== 'submitted' || busy}
        />
        <button disabled={pkg.Status !== 'submitted' || busy || rawResponse.trim() === ''} onClick={handleAttach}>
          Adjuntar resultado
        </button>
        {attachOk && <p className="success">Resultado válido; el paquete se completó.</p>}
        {validationErrors && (
          <div className="error">
            <p>El resultado no cumple el esquema esperado:</p>
            <ul>
              {validationErrors.map((e, i) => (
                <li key={i}>{e}</li>
              ))}
            </ul>
          </div>
        )}
      </section>
    </div>
  )
}
