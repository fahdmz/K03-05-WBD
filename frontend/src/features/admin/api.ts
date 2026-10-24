let csrfToken = ''

export function setAdminCsrfToken(token: string) {
  csrfToken = token
}

export class AdminApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export async function adminApi<T>(
  path: string,
  method = 'GET',
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {}

  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }

  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    headers['X-CSRF-Token'] = csrfToken
  }

  const response = await fetch(`/api${path}`, {
    method,
    headers,
    credentials: 'include',
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  if (!response.ok) {
    let message =
      response.status === 401
        ? 'Sesi berakhir. Silakan login kembali.'
        : `Permintaan gagal (${response.status}).`

    try {
      const data: unknown = await response.json()
      if (
        typeof data === 'object' &&
        data !== null &&
        'error' in data &&
        typeof data.error === 'string'
      ) {
        message = data.error
      }
    } catch {
      // Keep the status-based message when the response is not JSON.
    }

    throw new AdminApiError(response.status, message)
  }

  if (response.status === 204) {
    return undefined as T
  }

  return response.json() as Promise<T>
}

export function adminErrorMessage(error: unknown): string {
  return error instanceof AdminApiError
    ? error.message
    : 'Tidak dapat menghubungi server. Periksa koneksi dan coba kembali.'
}