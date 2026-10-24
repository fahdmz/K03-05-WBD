import type { InputHTMLAttributes } from "react";

type Props = InputHTMLAttributes<HTMLInputElement> & {
    label: string
    error?: string
}

export default function Input({ label, error, className = '', id, ...rest }: Props) {
    const inputId = id ?? `input-${label.toLowerCase().replace(/\s+/g, '-')}`
    return (
        <div className="space-y-1">
            <label htmlFor={inputId} className="block text-sm font-medium text-slate-700">
                {label}
            </label>
            <input
                id={inputId}
                {...rest}
                className={`w-full rounded border px-3 py-2 text-sm ${error ? 'border-red-500' : 'border-slate-300'} ${className}`}
            />
            {error && <p className="text-xs text-red-600">{error}</p>}
        </div>
    )
}