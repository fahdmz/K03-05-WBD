import { useEffect, useState, type FormEvent } from 'react'
import { api, ApiError } from '../../lib/api'
import ExplainPanel from './ExplainPanel'

type DatabaseSummary = {
    id: number
    name: string
    level: 'view' | 'edit'
}

type Migration = {
    id: number
    databaseId: number
    version: number | null
    title: string
    sql: string
    status: 'draft' | 'success' | 'failed'
    createdBy: string
    executedBy: string | null
    createdAt: string
    executedAt: string | null
    errorMessage: string | null
}

type FormMode = 'create' | 'edit'

function getErrorMessage(error: unknown) {
    if (error instanceof ApiError) return error.message
    return 'Tidak dapat terhubung ke server. Coba lagi.'
}

function formatDate(value: string | null) {
    if (!value) return '—'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return value
    return new Intl.DateTimeFormat('id-ID', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function statusStyle(status: Migration['status']) {
    if (status === 'success') return 'bg-emerald-50 text-emerald-700'
    if (status === 'failed') return 'bg-rose-50 text-rose-700'
    return 'bg-amber-50 text-amber-800'
}

export default function MigrationWorkspace({ database }: { database: DatabaseSummary }) {
    const canEdit = database.level === 'edit'
    const [migrations, setMigrations] = useState<Migration[]>([])
    const [selected, setSelected] = useState<Migration | null>(null)
    const [formMode, setFormMode] = useState<FormMode | null>(null)
    const [title, setTitle] = useState('')
    const [sql, setSQL] = useState('')
    const [loading, setLoading] = useState(true)
    const [saving, setSaving] = useState(false)
    const [executing, setExecuting] = useState(false)
    const [error, setError] = useState('')
    const [notice, setNotice] = useState('')

    async function loadHistory() {
        setLoading(true)
        setError('')
        try {
            const result = await api<Migration[]>(`/databases/${database.id}/migrations`)
            setMigrations(result)
            setSelected((current) => current ? result.find((item) => item.id === current.id) ?? null : null)
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setLoading(false)
        }
    }

    useEffect(() => {
        setSelected(null)
        setFormMode(null)
        setNotice('')
        void loadHistory()
    }, [database.id])

    async function openMigration(migration: Migration) {
        setFormMode(null)
        setError('')
        setNotice('')
        try {
            const detail = await api<Migration>(`/migrations/${migration.id}`)
            setSelected(detail)
        } catch (cause) {
            setError(getErrorMessage(cause))
        }
    }

    function startCreate() {
        setSelected(null)
        setTitle('')
        setSQL('')
        setFormMode('create')
        setError('')
        setNotice('')
    }

    function startEdit(migration: Migration) {
        setSelected(migration)
        setTitle(migration.title)
        setSQL(migration.sql)
        setFormMode('edit')
        setError('')
        setNotice('')
    }

    function cancelForm() {
        setFormMode(null)
        setError('')
    }

    async function saveDraft(event: FormEvent<HTMLFormElement>) {
        event.preventDefault()
        setSaving(true)
        setError('')
        setNotice('')
        try {
            const result = formMode === 'create'
                ? await api<Migration>(`/databases/${database.id}/migrations`, { method: 'POST', body: { title, sql } })
                : await api<Migration>(`/migrations/${selected?.id}`, { method: 'PATCH', body: { title, sql } })
            setSelected(result)
            setFormMode(null)
            setNotice(formMode === 'create' ? 'Draft migration berhasil dibuat.' : 'Draft migration berhasil diperbarui.')
            await loadHistory()
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setSaving(false)
        }
    }

    async function executeMigration(migration: Migration) {
        if (!window.confirm(`Jalankan migration “${migration.title}” pada ${database.name}?`)) return
        setExecuting(true)
        setError('')
        setNotice('')
        try {
            const result = await api<Migration>(`/migrations/${migration.id}/execute`, { method: 'POST' })
            setSelected(result)
            if (result.status === 'success') {
                setNotice(`Migration berhasil dijalankan sebagai versi ${result.version}.`)
            } else {
                setError(result.errorMessage || 'Migration gagal dan sudah di-rollback.')
            }
            await loadHistory()
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setExecuting(false)
        }
    }

    return (
        <div className="grid gap-5 xl:grid-cols-[320px_minmax(0,1fr)]">
            <aside className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm">
                <div className="flex items-center justify-between gap-3 border-b border-slate-200 px-4 py-4">
                    <div>
                        <h2 className="font-semibold text-slate-900">Riwayat migration</h2>
                        <p className="mt-1 text-xs text-slate-500">Versi sukses berurutan per database</p>
                    </div>
                    <button onClick={() => void loadHistory()} disabled={loading} className="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50">Muat ulang</button>
                </div>
                {canEdit && <div className="border-b border-slate-100 p-3"><button onClick={startCreate} className="w-full rounded-lg bg-slate-900 px-3 py-2 text-sm font-semibold text-white hover:bg-slate-700">+ Buat migration</button></div>}
                <div className="max-h-[650px] space-y-1 overflow-y-auto p-2">
                    {loading ? <p className="px-3 py-5 text-center text-sm text-slate-400">Memuat riwayat…</p> : migrations.length === 0 ? (
                        <p className="px-3 py-7 text-center text-sm text-slate-400">Belum ada migration.</p>
                    ) : migrations.map((migration) => (
                        <button key={migration.id} onClick={() => void openMigration(migration)} className={`w-full rounded-xl p-3 text-left ${selected?.id === migration.id ? 'bg-cyan-50 ring-1 ring-cyan-200' : 'hover:bg-slate-50'}`}>
                            <div className="flex items-center justify-between gap-2">
                                <span className="truncate text-sm font-semibold text-slate-800">{migration.title}</span>
                                <span className={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-semibold ${statusStyle(migration.status)}`}>{migration.status}</span>
                            </div>
                            <div className="mt-1.5 flex items-center justify-between gap-2 text-xs text-slate-500">
                                <span>{migration.version === null ? 'Belum berversi' : `Versi ${migration.version}`}</span>
                                <span>{formatDate(migration.createdAt)}</span>
                            </div>
                        </button>
                    ))}
                </div>
                {!canEdit && <p className="border-t border-slate-100 px-4 py-3 text-xs text-slate-500">Akses View only: riwayat dan detail dapat dilihat, perubahan dan eksekusi dinonaktifkan.</p>}
            </aside>

            <section className="min-w-0 rounded-2xl border border-slate-200 bg-white p-5 shadow-sm">
                {error && <div role="alert" className="mb-4 rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">{error}</div>}
                {notice && <div role="status" className="mb-4 rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800">{notice}</div>}

                {formMode ? (
                    <form onSubmit={saveDraft} className="space-y-4">
                        <div className="flex items-start justify-between gap-4">
                            <div>
                                <p className="text-xs font-semibold uppercase tracking-wide text-cyan-700">{formMode === 'create' ? 'Draft baru' : 'Edit draft'}</p>
                                <h2 className="mt-1 text-xl font-semibold text-slate-900">{formMode === 'create' ? 'Susun migration' : selected?.title}</h2>
                            </div>
                            <button type="button" onClick={cancelForm} className="rounded-lg border border-slate-300 px-3 py-2 text-sm text-slate-600 hover:bg-slate-50">Batal</button>
                        </div>
                        <label className="block text-sm font-medium text-slate-700">
                            Judul
                            <input value={title} onChange={(event) => setTitle(event.target.value)} required maxLength={200} className="mt-1.5 w-full rounded-lg border border-slate-300 px-3 py-2 font-normal outline-none focus:border-cyan-600 focus:ring-2 focus:ring-cyan-100" placeholder="Contoh: Tambah email ke customers" />
                        </label>
                        <label className="block text-sm font-medium text-slate-700">
                            SQL
                            <textarea value={sql} onChange={(event) => setSQL(event.target.value)} required rows={16} spellCheck={false} className="mt-1.5 w-full resize-y rounded-xl border border-slate-300 bg-slate-950 px-4 py-3 font-mono text-sm leading-6 text-slate-100 outline-none focus:border-cyan-500 focus:ring-2 focus:ring-cyan-100" placeholder="ALTER TABLE customers ADD COLUMN email TEXT;" />
                        </label>
                        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-slate-100 pt-4">
                            <p className="text-xs text-slate-500">Draft belum mengubah database target. Perubahan berlaku setelah dieksekusi.</p>
                            <button type="submit" disabled={saving || !title.trim() || !sql.trim()} className="rounded-lg bg-cyan-800 px-4 py-2 text-sm font-semibold text-white hover:bg-cyan-700 disabled:cursor-not-allowed disabled:opacity-50">{saving ? 'Menyimpan…' : 'Simpan draft'}</button>
                        </div>
                        {canEdit && <ExplainPanel key={`${database.id}:${sql}`} databaseId={database.id} sql={sql} />}
                    </form>
                ) : selected ? (
                    <div className="space-y-5">
                        <div className="flex flex-wrap items-start justify-between gap-4">
                            <div className="min-w-0">
                                <div className="flex flex-wrap items-center gap-2">
                                    <span className={`rounded-full px-2.5 py-1 text-xs font-semibold ${statusStyle(selected.status)}`}>{selected.status}</span>
                                    {selected.version !== null && <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-600">Versi {selected.version}</span>}
                                </div>
                                <h2 className="mt-3 break-words text-2xl font-semibold text-slate-900">{selected.title}</h2>
                                <p className="mt-1 text-sm text-slate-500">Dibuat oleh {selected.createdBy} · {formatDate(selected.createdAt)}</p>
                            </div>
                            <div className="flex gap-2">
                                {canEdit && selected.status === 'draft' && <>
                                    <button onClick={() => startEdit(selected)} className="rounded-lg border border-slate-300 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50">Edit draft</button>
                                    <button onClick={() => void executeMigration(selected)} disabled={executing} className="rounded-lg bg-emerald-700 px-3 py-2 text-sm font-semibold text-white hover:bg-emerald-600 disabled:opacity-50">{executing ? 'Menjalankan…' : 'Jalankan'}</button>
                                </>}
                            </div>
                        </div>
                        {selected.executedAt && <div className="rounded-xl bg-slate-50 px-4 py-3 text-sm text-slate-600">Dieksekusi oleh <span className="font-semibold text-slate-800">{selected.executedBy ?? '—'}</span> · {formatDate(selected.executedAt)}</div>}
                        {selected.status === 'failed' && selected.errorMessage && <div className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3"><p className="text-sm font-semibold text-rose-800">Migration gagal; perubahan SQL telah di-rollback.</p><p className="mt-1 break-words font-mono text-xs text-rose-700">{selected.errorMessage}</p></div>}
                        <div>
                            <h3 className="mb-2 text-sm font-semibold text-slate-700">SQL migration</h3>
                            <pre className="max-h-[560px] overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-sm leading-6 text-slate-100"><code>{selected.sql}</code></pre>
                        </div>
                        {canEdit && <ExplainPanel key={`${database.id}:${selected.id}:${selected.sql}`} databaseId={database.id} sql={selected.sql} />}
                    </div>
                ) : (
                    <div className="flex min-h-64 flex-col items-center justify-center text-center">
                        <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-cyan-50 text-xl text-cyan-800">↻</div>
                        <h2 className="mt-4 text-lg font-semibold text-slate-900">Pilih migration</h2>
                        <p className="mt-1 max-w-sm text-sm text-slate-500">Buka item di riwayat untuk melihat SQL, versi, pelaksana, dan hasil eksekusi.</p>
                        {canEdit && migrations.length === 0 && <button onClick={startCreate} className="mt-5 rounded-lg bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-700">Buat draft pertama</button>}
                    </div>
                )}
            </section>
        </div>
    )
}
