import type { AttachResultResponse, Package, PackageSummary } from './types'

class ApiError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

const TOKEN_KEY = 'dictum_token'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? ''
}

export function setToken(token: string) {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token)
  } else {
    localStorage.removeItem(TOKEN_KEY)
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const token = getToken()
  const res = await fetch(path, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(options?.headers ?? {}),
    },
  })
  if (!res.ok) {
    const body = await res.text()
    throw new ApiError(body || res.statusText, res.status)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export interface PackageFilters {
  caseId?: string
  useCase?: string
  status?: string
}

export function listPackages(filters: PackageFilters): Promise<PackageSummary[]> {
  const params = new URLSearchParams()
  if (filters.caseId) params.set('case_id', filters.caseId)
  if (filters.useCase) params.set('use_case', filters.useCase)
  if (filters.status) params.set('status', filters.status)
  const qs = params.toString()
  return request(`/api/packages${qs ? `?${qs}` : ''}`)
}

export function getPackage(id: string): Promise<Package> {
  return request(`/api/packages/${id}`)
}

export function submitPackage(id: string): Promise<Package> {
  return request(`/api/packages/${id}/submit`, { method: 'POST' })
}

export function cancelPackage(id: string): Promise<Package> {
  return request(`/api/packages/${id}/cancel`, { method: 'POST' })
}

export function resubmitPackage(id: string): Promise<Package> {
  return request(`/api/packages/${id}/resubmit`, { method: 'POST' })
}

export function attachResult(id: string, rawResponse: unknown): Promise<AttachResultResponse> {
  return request(`/api/packages/${id}/results`, {
    method: 'POST',
    body: JSON.stringify({ raw_response: rawResponse }),
  })
}

export function archiveUrl(id: string): string {
  // Plain <a href> downloads can't set an Authorization header, so the
  // token rides as ?access_token= (the API's documented fallback).
  const token = getToken()
  const qs = token ? `?access_token=${encodeURIComponent(token)}` : ''
  return `/api/packages/${id}/archive${qs}`
}

export { ApiError }
