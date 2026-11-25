import { useId } from 'react'

export type BatchConfig = {
    table: string
    column: string
    size: number
}

export type BatchConfigErrors = Partial<Record<keyof BatchConfig, string>>

type Props = {
    value: BatchConfig
    onChange: (value: BatchConfig) => void
    disabled?: boolean
    errors?: BatchConfigErrors
}

const MIN_BATCH_SIZE = 1
const MAX_BATCH_SIZE = 100000

const inputCls = 'mt-1.5 w-full rounded-lg border px-3 py-2 text-sm font-normal outline-none focus:border-cyan-600 focus:ring-2 focus:ring-cyan-100 disabled:cursor-not-allowed disabled:bg-slate-50 disabled:text-slate-500'

function border(err?: string) {
    return err ? 'border-rose-400' : 'border-slate-300'
}

const upExample = 'UPDATE orders_big SET price = price * 1.1 WHERE id > {{lo}} AND id <= {{hi}};'
const downExample = 'UPDATE orders_big SET price = price / 1.1 WHERE id > {{lo}} AND id <= {{hi}};'

export default function BatchConfigFields({ value, onChange, disabled = false, errors = {} }: Props) {
    const id = useId()

    return (
        <fieldset disabled={disabled} className="space-y-4 rounded-xl border border-slate-200 p-4">
            <legend className="px-1 text-sm font-semibold text-slate-800">Konfigurasi batch</legend>

            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]">
                <div>
                    <label htmlFor={`${id}-table`} className="block text-sm font-medium text-slate-700">
                        Tabel
                    </label>
                    <input
                        id={`${id}-table`}
                        value={value.table}
                        onChange={(e) => onChange({ ...value, table: e.target.value })}
                        placeholder="orders_big atau public.orders_big"
                        autoComplete="off"
                        spellCheck={false}
                        aria-invalid={!!errors.table}
                        className={`${inputCls} ${border(errors.table)} font-mono`}
                    />
                    {errors.table && <p className="mt-1 text-xs text-rose-600">{errors.table}</p>}
                </div>
                <div>
                    <label htmlFor={`${id}-col`} className="block text-sm font-medium text-slate-700">
                        Kolom batch
                    </label>
                    <input
                        id={`${id}-col`}
                        value={value.column}
                        onChange={(e) => onChange({ ...value, column: e.target.value })}
                        placeholder="id"
                        autoComplete="off"
                        spellCheck={false}
                        aria-invalid={!!errors.column}
                        className={`${inputCls} ${border(errors.column)} font-mono`}
                    />
                    {errors.column && <p className="mt-1 text-xs text-rose-600">{errors.column}</p>}
                </div>
                <div className="sm:col-span-2 lg:col-span-1">
                    <label htmlFor={`${id}-size`} className="block text-sm font-medium text-slate-700">
                        Batch size
                    </label>
                    <input
                        id={`${id}-size`}
                        type="number"
                        inputMode="numeric"
                        min={MIN_BATCH_SIZE}
                        max={MAX_BATCH_SIZE}
                        step={1}
                        value={value.size || ''}
                        onChange={(e) => onChange({ ...value, size: e.target.value === '' ? 0 : Number(e.target.value) })}
                        placeholder="10000"
                        aria-invalid={!!errors.size}
                        className={`${inputCls} ${border(errors.size)}`}
                    />
                    {errors.size ? (
                        <p className="mt-1 text-xs text-rose-600">{errors.size}</p>
                    ) : (
                        <p className="mt-1 text-xs text-slate-500">Baris per batch, 1–100.000</p>
                    )}
                </div>
            </div>

            <div className="space-y-2 rounded-lg bg-slate-50 p-3 text-xs leading-5 text-slate-600">
                <p>
                    SQL up dan down dijalankan per batch, satu statement saja, dengan placeholder{' '}
                    <code className="rounded bg-white px-1 font-mono text-slate-800">{'{{lo}}'}</code> dan{' '}
                    <code className="rounded bg-white px-1 font-mono text-slate-800">{'{{hi}}'}</code>. Worker mengisinya dengan
                    rentang nilai kolom batch: <span className="font-medium">lo eksklusif, hi inklusif</span>.
                </p>
                <p>
                    Kolom batch harus integer, unik, dan tidak berubah selama migration (misalnya primary key{' '}
                    <code className="rounded bg-white px-1 font-mono text-slate-800">id</code>). Jangan ubah kolom ini di SQL.
                </p>
                <div className="space-y-1">
                    <p className="font-medium text-slate-700">Contoh up</p>
                    <pre className="overflow-x-auto rounded-lg bg-slate-950 px-3 py-2 font-mono text-[11px] leading-5 text-slate-100 sm:text-xs">
                        <code>{upExample}</code>
                    </pre>
                    <p className="font-medium text-slate-700">Contoh down</p>
                    <pre className="overflow-x-auto rounded-lg bg-slate-950 px-3 py-2 font-mono text-[11px] leading-5 text-slate-100 sm:text-xs">
                        <code>{downExample}</code>
                    </pre>
                </div>
                <p>
                    Tiap batch di-commit sendiri. Kalau gagal di tengah, batch yang sudah committed tetap tersimpan dan job bisa
                    dilanjutkan dari checkpoint atau dibatalkan (revert).
                </p>
            </div>
        </fieldset>
    )
}
