import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'

type ToastKind = 'success' | 'error'
type ToastItem = { id: number; kind: ToastKind; message: string }

type ToastApi = {
    success: (message: string) => void
    error: (message: string) => void
}

const ToastContext = createContext<ToastApi | null>(null)

let nextId = 1

export function ToastProvider({ children }: { children: ReactNode }) {
    const [items, setItems] = useState<ToastItem[]>([])

    const dismiss = useCallback((id: number) => {
        setItems((prev) => prev.filter((t) => t.id !== id))
    }, [])

    const push = useCallback(
        (kind: ToastKind, message: string) => {
            const id = nextId++
            setItems((prev) => [...prev, { id, kind, message }])
            setTimeout(() => dismiss(id), 4000)
        },
        [dismiss],
    )

    const api = useMemo<ToastApi>(
        () => ({
            success: (message) => push('success', message),
            error: (message) => push('error', message),
        }),
        [push],
    )

    return (
        <ToastContext.Provider value={api}>
            {children}
            <div className="fixed bottom-4 right-4 z-50 space-y-2">
                {items.map((t) => (
                    <div
                        key={t.id}
                        role="status"
                        className={`flex items-start gap-3 rounded px-4 py-3 text-sm text-white shadow ${t.kind === 'success' ? 'bg-emerald-600' : 'bg-red-600'
                            }`}
                    >
                        <span>{t.message}</span>
                        <button
                            onClick={() => dismiss(t.id)}
                            aria-label="Tutup"
                            className="ml-auto opacity-80 hover:opacity-100"
                        >
                            ×
                        </button>
                    </div>
                ))}
            </div>
        </ToastContext.Provider>
    )
}

export function useToast() {
    const ctx = useContext(ToastContext)
    if (!ctx) throw new Error('useToast harus dipakai di dalam ToastProvider')
    return ctx
}