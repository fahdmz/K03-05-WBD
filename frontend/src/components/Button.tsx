import type { ButtonHTMLAttributes } from "react"


type Variant = 'primary' | 'secondary' | 'danger'

const styles: Record<Variant, string> = {
    primary: 'bg-slate-900 text-white hover:bg-slate-700',
    secondary: 'border border-slate-300 bg-white text-slate-800 hover:bg-slate-100',
    danger: 'bg-red-600 text-white hover:bg-red-500',
}

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: Variant
    loading?: boolean
}

export default function Button({
    variant = 'primary',
    loading = false,
    type = 'button',
    disabled,
    className = '',
    children,
    ...rest
}: Props) {
    return (
        <button
            {...rest}
        >
            {loading ? 'Memproses...' : children}
        </button>
    )
}