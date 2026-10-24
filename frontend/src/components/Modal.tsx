import { useEffect, type ReactNode } from 'react'

type Props = {
    title: string
    open: boolean
    onClose: () => void
    children: ReactNode
    footer?: ReactNode
}

export default function Modal({ title, open, onClose, children, footer }: Props) {
    useEffect(() => {
        if (!open) return
        function onKey(e: KeyboardEvent) {
            if (e.key === 'Escape') onClose()
        }
        window.addEventListener('keydown', onKey)
        return () => window.removeEventListener('keydown', onKey)
    }, [open, onClose])

    if (!open) return null

    return (
        <div
            className="fixed inset-0 z-40 flex items-center justify-center bg-black/40 px-4"
            onClick={onClose}
        >
            <div
                role="dialog"
                aria-modal="true"
                aria-label={title}
                className="w-full max-w-lg rounded-lg bg-white shadow-lg"
                onClick={(e) => e.stopPropagation()}
            >
                <div className="border-b border-slate-200 px-5 py-3">
                    <h2 className="text-lg font-semibold">{title}</h2>
                </div>
                <div className="px-5 py-4">{children}</div>
                {footer && (
                    <div className="flex justify-end gap-2 border-t border-slate-200 px-5 py-3">{footer}</div>
                )}
            </div>
        </div>
    )
}