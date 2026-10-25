let csrfToken = '' // menyimpan token csrf
let onUnauthorized: () => void = () => { } // 

export function setCsrfToken(token: string) {
    csrfToken = token
}

export function setUnauthorizedHandler(fn: () => void) {
    onUnauthorized = fn
}

export class ApiError extends Error {
    status: number

    constructor(status: number, message: string) {
        super(message)
        this.status = status
    }
}

type Options = {
    method?: string
    body?: unknown
}

export async function api<T>(path: string, options: Options = {}): Promise<T> {
    const method = options.method ?? 'GET'
    const headers: Record<string, string> = {}

    if (options.body !== undefined) {
        headers['Content-Type'] = 'application/json'
    }

    if (method !== 'GET') {
        headers['X-CSRF-Token'] = csrfToken
    }

    const res = await fetch('/api' + path, {
        method,
        headers,
        credentials: 'include',
        body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
    })

    if (res.status === 401) {
        onUnauthorized()
    }

    if (!res.ok) {
        let message = 'Terjadi Kesalahan'
        try {
            const data = await res.json()
            if (data.error) message = data.error
        } catch {
            message = res.statusText || message
        }
        throw new ApiError(res.status, message)
    }

    if (res.status === 204) {
        return undefined as T
    }
    return res.json()
}

export function errorMessage(err: unknown): string {
    return err instanceof ApiError ? err.message : 'Tidak dapat terhubung ke server'
}