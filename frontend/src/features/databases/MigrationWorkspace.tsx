import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api, ApiError } from '../../lib/api'
import ExplainPanel from './ExplainPanel'

type DatabaseSummary = {
    id: number
    name: string
    level: 'view' | 'edit'
}

type Revision = {
    id: number
    migrationId: number
    revisionNo: number
    status: 'draft' | 'submitted'
    sqlUp: string
    sqlDown: string | null
    irreversible: boolean
    irreversibleReason: string | null
    mode: 'normal' | 'batch'
    batchTable: string | null
    batchColumn: string | null
    batchSize: number | null
    createdBy: string
    createdAt: string
    submittedAt: string | null
}

type ChangeRequestSummary = {
    id: number
    revisionId: number
    action: string
    targetExecutionId: string | null
    status: string
    requestedBy: string
    createdAt: string
    updatedAt: string
}

type MigrationSummary = {
    id: number
    databaseId: number
    version: number | null
    isApplied: boolean
    title: string
    createdBy: string
    createdAt: string
    currentRevision: Revision | null
    latestRequestStatus: string | null
}

type MigrationDetail = MigrationSummary & {
    revisions: Revision[]
    changeRequests: ChangeRequestSummary[]
}

type ValidationResult = {
    valid: boolean
    errors: string[]
    warnings: string[]
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

function displayStatus(m: MigrationSummary) {
    if (m.latestRequestStatus) {
        switch (m.latestRequestStatus) {
            case 'pending_review':
                return 'Pending Review'
            case 'approved':
                return 'Approved'
            case 'rejected':
                return 'Rejected'
            case 'queued':
                return 'Pending (Queue)'
            case 'running':
                return 'Running'
            case 'success':
                return m.isApplied ? 'Success' : 'Rolled Back'
            case 'failed':
                return 'Failed'
            case 'partial':
                return 'Partial'
            case 'uncertain':
                return 'Uncertain'
        }
    }
    if (m.isApplied) return 'Success'
    return 'Draft'
}

function statusStyle(label: string) {
    if (label === 'Success' || label === 'Approved') return 'bg-emerald-50 text-emerald-700'
    if (label === 'Failed' || label === 'Rejected') return 'bg-rose-50 text-rose-700'
    if (label === 'Pending (Queue)' || label === 'Running') return 'bg-cyan-50 text-cyan-800'
    return 'bg-amber-50 text-amber-800'
}

export default function MigrationWorkspace({ database }: { database: DatabaseSummary }) {
    const canEdit = database.level === 'edit'
    const [migrations, setMigrations] = useState<MigrationSummary[]>([])
    const [selected, setSelected] = useState<MigrationDetail | null>(null)
    const [viewingRevId, setViewingRevId] = useState<number | null>(null)
    const [formMode, setFormMode] = useState<FormMode | null>(null)

    // Form state
    const [title, setTitle] = useState('')
    const [mode, setMode] = useState<'normal' | 'batch'>('normal')
    const [sqlUp, setSQLUp] = useState('')
    const [sqlDown, setSQLDown] = useState('')
    const [irreversible, setIrreversible] = useState(false)
    const [irreversibleReason, setIrreversibleReason] = useState('')
    const [batchTable, setBatchTable] = useState('')
    const [batchColumn, setBatchColumn] = useState('id')
    const [batchSize, setBatchSize] = useState('1000')

    const [loading, setLoading] = useState(true)
    const [saving, setSaving] = useState(false)
    const [validating, setValidating] = useState(false)
    const [submitting, setSubmitting] = useState(false)
    const [validation, setValidation] = useState<ValidationResult | null>(null)
    const [error, setError] = useState('')
    const [notice, setNotice] = useState('')

    async function loadList() {
        setLoading(true)
        setError('')
        try {
            const result = await api<MigrationSummary[]>(`/databases/${database.id}/migrations`)
            setMigrations(result)
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setLoading(false)
        }
    }

    useEffect(() => {
        setSelected(null)
        setViewingRevId(null)
        setFormMode(null)
        setValidation(null)
        setNotice('')
        void loadList()
    }, [database.id])

    async function openMigration(id: number) {
        setFormMode(null)
        setValidation(null)
        setError('')
        setNotice('')
        try {
            const detail = await api<MigrationDetail>(`/migrations/${id}`)
            setSelected(detail)
            setViewingRevId(detail.currentRevision?.id ?? null)
        } catch (cause) {
            setError(getErrorMessage(cause))
        }
    }

    function startCreate() {
        setSelected(null)
        setViewingRevId(null)
        setValidation(null)
        setTitle('')
        setMode('normal')
        setSQLUp('')
        setSQLDown('')
        setIrreversible(false)
        setIrreversibleReason('')
        setBatchTable('')
        setBatchColumn('id')
        setBatchSize('1000')
        setFormMode('create')
        setError('')
        setNotice('')
    }

    function startEdit(detail: MigrationDetail) {
        const rev = detail.currentRevision
        setSelected(detail)
        setValidation(null)
        setTitle(detail.title)
        setMode(rev?.mode ?? 'normal')
        setSQLUp(rev?.sqlUp ?? '')
        setSQLDown(rev?.sqlDown ?? '')
        setIrreversible(rev?.irreversible ?? false)
        setIrreversibleReason(rev?.irreversibleReason ?? '')
        setBatchTable(rev?.batchTable ?? '')
        setBatchColumn(rev?.batchColumn ?? 'id')
        setBatchSize(rev?.batchSize ? String(rev.batchSize) : '1000')
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
        setValidation(null)
        try {
            const body: Record<string, unknown> = {
                title,
                mode,
                sqlUp,
                irreversible,
                sqlDown: irreversible ? null : sqlDown,
                irreversibleReason: irreversible ? irreversibleReason : null,
            }
            if (mode === 'batch') {
                body.batchTable = batchTable.trim()
                body.batchColumn = batchColumn.trim()
                body.batchSize = Number(batchSize)
            }

            const result =
                formMode === 'create'
                    ? await api<MigrationDetail>(`/databases/${database.id}/migrations`, { method: 'POST', body })
                    : await api<MigrationDetail>(`/migrations/${selected?.id}`, { method: 'PATCH', body })
            setSelected(result)
            setViewingRevId(result.currentRevision?.id ?? null)
            setFormMode(null)
            setNotice(
                formMode === 'create'
                    ? 'Draft migration berhasil dibuat.'
                    : `Revisi #${result.currentRevision?.revisionNo ?? 1} berhasil disimpan.`,
            )
            await loadList()
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setSaving(false)
        }
    }

    async function handleValidate(detail: MigrationDetail) {
        setValidating(true)
        setError('')
        setNotice('')
        setValidation(null)
        try {
            const res = await api<ValidationResult>(`/migrations/${detail.id}/validate`, { method: 'POST' })
            setValidation(res)
            if (res.valid) {
                setNotice('Validasi berhasil. Revisi siap diajukan untuk review.')
            }
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setValidating(false)
        }
    }

    async function handleSubmit(detail: MigrationDetail) {
        if (!window.confirm(`Ajukan migration "${detail.title}" untuk direview oleh semua leader group?`)) return
        setSubmitting(true)
        setError('')
        setNotice('')
        try {
            const res = await api<{
                changeRequestId: number
                migration: MigrationDetail
                validation: ValidationResult
            }>(`/migrations/${detail.id}/submit`, { method: 'POST' })
            setSelected(res.migration)
            setViewingRevId(res.migration.currentRevision?.id ?? null)
            setValidation(res.validation)
            setNotice(`Pengajuan berhasil dibuat (Change Request #${res.changeRequestId}). Menunggu approval leader.`)
            await loadList()
        } catch (cause) {
            setError(getErrorMessage(cause))
        } finally {
            setSubmitting(false)
        }
    }

    const activeRev =
        selected?.revisions.find((r) => r.id === viewingRevId) ?? selected?.currentRevision ?? null
    const isCurrentDraft = selected?.currentRevision?.status === 'draft'
    const canCreateNewRevision =
        canEdit &&
        selected !== null &&
        selected.currentRevision?.status === 'submitted' &&
        selected.version === null

    return (
        <div className="grid gap-5 xl:grid-cols-[320px_minmax(0,1fr)]">
            <aside className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm">
                <div className="flex items-center justify-between gap-3 border-b border-slate-200 px-4 py-4">
                    <div>
                        <h2 className="font-semibold text-slate-900">Daftar migration</h2>
                        <p className="mt-1 text-xs text-slate-500">Revisi & pengajuan per database</p>
                    </div>
                    <button
                        type="button"
                        onClick={() => void loadList()}
                        disabled={loading}
                        className="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50"
                    >
                        Muat ulang
                    </button>
                </div>
                {canEdit && (
                    <div className="border-b border-slate-100 p-3">
                        <button
                            type="button"
                            onClick={startCreate}
                            className="w-full rounded-lg bg-slate-900 px-3 py-2 text-sm font-semibold text-white hover:bg-slate-700"
                        >
                            + Buat migration
                        </button>
                    </div>
                )}
                <div className="max-h-[650px] space-y-1 overflow-y-auto p-2">
                    {loading ? (
                        <p className="px-3 py-5 text-center text-sm text-slate-400">Memuat daftar…</p>
                    ) : migrations.length === 0 ? (
                        <p className="px-3 py-7 text-center text-sm text-slate-400">Belum ada migration.</p>
                    ) : (
                        migrations.map((m) => {
                            const label = displayStatus(m)
                            return (
                                <button
                                    key={m.id}
                                    type="button"
                                    onClick={() => void openMigration(m.id)}
                                    className={`w-full rounded-xl p-3 text-left ${selected?.id === m.id ? 'bg-cyan-50 ring-1 ring-cyan-200' : 'hover:bg-slate-50'}`}
                                >
                                    <div className="flex items-center justify-between gap-2">
                                        <span className="truncate text-sm font-semibold text-slate-800">{m.title}</span>
                                        <span className={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-semibold ${statusStyle(label)}`}>
                                            {label}
                                        </span>
                                    </div>
                                    <div className="mt-1.5 flex items-center justify-between gap-2 text-xs text-slate-500">
                                        <span>
                                            {m.version === null ? `Rev #${m.currentRevision?.revisionNo ?? 1}` : `Versi ${m.version}`}
                                        </span>
                                        <span>{formatDate(m.createdAt)}</span>
                                    </div>
                                </button>
                            )
                        })
                    )}
                </div>
                {!canEdit && (
                    <p className="border-t border-slate-100 px-4 py-3 text-xs text-slate-500">
                        Akses View only: daftar dan detail dapat dilihat, pembuatan dan pengajuan dinonaktifkan.
                    </p>
                )}
            </aside>

            <section className="min-w-0 rounded-2xl border border-slate-200 bg-white p-5 shadow-sm">
                {error && (
                    <div role="alert" className="mb-4 rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
                        {error}
                    </div>
                )}
                {notice && (
                    <div role="status" className="mb-4 rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800">
                        {notice}
                    </div>
                )}

                {formMode ? (
                    <form onSubmit={saveDraft} className="space-y-4">
                        <div className="flex items-start justify-between gap-4">
                            <div>
                                <p className="text-xs font-semibold uppercase tracking-wide text-cyan-700">
                                    {formMode === 'create'
                                        ? 'Draft baru'
                                        : isCurrentDraft
                                            ? 'Edit draft'
                                            : 'Buat revisi baru'}
                                </p>
                                <h2 className="mt-1 text-xl font-semibold text-slate-900">
                                    {formMode === 'create' ? 'Susun migration' : selected?.title}
                                </h2>
                            </div>
                            <button
                                type="button"
                                onClick={cancelForm}
                                className="rounded-lg border border-slate-300 px-3 py-2 text-sm text-slate-600 hover:bg-slate-50"
                            >
                                Batal
                            </button>
                        </div>

                        <label className="block text-sm font-medium text-slate-700">
                            Judul
                            <input
                                value={title}
                                onChange={(e) => setTitle(e.target.value)}
                                required
                                maxLength={200}
                                className="mt-1.5 w-full rounded-lg border border-slate-300 px-3 py-2 font-normal outline-none focus:border-cyan-600 focus:ring-2 focus:ring-cyan-100"
                                placeholder="Contoh: Tambah email ke customers"
                            />
                        </label>

                        {/* Pilihan mode */}
                        <fieldset className="space-y-2">
                            <legend className="text-sm font-medium text-slate-700">Mode eksekusi</legend>
                            <div className="flex flex-wrap gap-3">
                                <label className="flex cursor-pointer items-center gap-2 rounded-lg border border-slate-200 px-3 py-2 text-sm">
                                    <input
                                        type="radio"
                                        name="mode"
                                        value="normal"
                                        checked={mode === 'normal'}
                                        onChange={() => setMode('normal')}
                                    />
                                    <span>Normal (1 transaksi)</span>
                                </label>
                                <label className="flex cursor-pointer items-center gap-2 rounded-lg border border-slate-200 px-3 py-2 text-sm">
                                    <input
                                        type="radio"
                                        name="mode"
                                        value="batch"
                                        checked={mode === 'batch'}
                                        onChange={() => setMode('batch')}
                                    />
                                    <span>Per batch (data besar)</span>
                                </label>
                            </div>
                        </fieldset>

                        {mode === 'batch' && (
                            <div className="grid gap-3 rounded-xl border border-slate-200 bg-slate-50 p-4 sm:grid-cols-3">
                                <label className="block text-xs font-medium text-slate-700">
                                    Tabel target
                                    <input
                                        value={batchTable}
                                        onChange={(e) => setBatchTable(e.target.value)}
                                        required
                                        placeholder="orders_big"
                                        className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-3 py-1.5 font-mono text-sm outline-none focus:border-cyan-600"
                                    />
                                </label>
                                <label className="block text-xs font-medium text-slate-700">
                                    Kolom unik (cursor)
                                    <input
                                        value={batchColumn}
                                        onChange={(e) => setBatchColumn(e.target.value)}
                                        required
                                        placeholder="id"
                                        className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-3 py-1.5 font-mono text-sm outline-none focus:border-cyan-600"
                                    />
                                </label>
                                <label className="block text-xs font-medium text-slate-700">
                                    Batch size (1–100000)
                                    <input
                                        type="number"
                                        min={1}
                                        max={100000}
                                        value={batchSize}
                                        onChange={(e) => setBatchSize(e.target.value)}
                                        required
                                        className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-3 py-1.5 font-mono text-sm outline-none focus:border-cyan-600"
                                    />
                                </label>
                                <p className="text-xs text-slate-500 sm:col-span-3">
                                    Gunakan placeholder <code>{'{{lo}}'}</code> dan <code>{'{{hi}}'}</code> dalam satu statement SQL.
                                </p>
                            </div>
                        )}

                        <label className="block text-sm font-medium text-slate-700">
                            SQL Up
                            <textarea
                                value={sqlUp}
                                onChange={(e) => setSQLUp(e.target.value)}
                                required
                                rows={10}
                                spellCheck={false}
                                className="mt-1.5 w-full resize-y overflow-x-auto rounded-xl border border-slate-300 bg-slate-950 px-4 py-3 font-mono text-sm leading-6 text-slate-100 outline-none focus:border-cyan-500 focus:ring-2 focus:ring-cyan-100"
                                placeholder={
                                    mode === 'batch'
                                        ? 'UPDATE orders_big SET price = price * 1.1 WHERE id > {{lo}} AND id <= {{hi}};'
                                        : 'ALTER TABLE customers ADD COLUMN email TEXT;'
                                }
                            />
                        </label>

                        <label className="flex cursor-pointer items-center gap-2 text-sm font-medium text-slate-700">
                            <input
                                type="checkbox"
                                checked={irreversible}
                                onChange={(e) => setIrreversible(e.target.checked)}
                            />
                            <span>Tandai sebagai irreversible (tidak dapat di-rollback)</span>
                        </label>

                        {irreversible ? (
                            <label className="block text-sm font-medium text-slate-700">
                                Alasan irreversible
                                <textarea
                                    value={irreversibleReason}
                                    onChange={(e) => setIrreversibleReason(e.target.value)}
                                    rows={3}
                                    placeholder="Jelaskan mengapa perubahan ini tidak dapat dibatalkan (mis. penghapusan data)."
                                    className="mt-1.5 w-full rounded-lg border border-slate-300 px-3 py-2 font-normal outline-none focus:border-cyan-600 focus:ring-2 focus:ring-cyan-100"
                                />
                            </label>
                        ) : (
                            <label className="block text-sm font-medium text-slate-700">
                                SQL Down (untuk rollback)
                                <textarea
                                    value={sqlDown}
                                    onChange={(e) => setSQLDown(e.target.value)}
                                    rows={7}
                                    spellCheck={false}
                                    className="mt-1.5 w-full resize-y overflow-x-auto rounded-xl border border-slate-300 bg-slate-950 px-4 py-3 font-mono text-sm leading-6 text-slate-100 outline-none focus:border-cyan-500 focus:ring-2 focus:ring-cyan-100"
                                    placeholder={
                                        mode === 'batch'
                                            ? 'UPDATE orders_big SET price = price / 1.1 WHERE id > {{lo}} AND id <= {{hi}};'
                                            : 'ALTER TABLE customers DROP COLUMN email;'
                                    }
                                />
                            </label>
                        )}

                        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-slate-100 pt-4">
                            <p className="text-xs text-slate-500">
                                Simpan draft dulu, lalu jalankan Validasi dan Ajukan untuk review leader.
                            </p>
                            <button
                                type="submit"
                                disabled={saving || !title.trim() || !sqlUp.trim()}
                                className="rounded-lg bg-cyan-800 px-4 py-2 text-sm font-semibold text-white hover:bg-cyan-700 disabled:cursor-not-allowed disabled:opacity-50"
                            >
                                {saving ? 'Menyimpan…' : 'Simpan draft'}
                            </button>
                        </div>

                        {canEdit && mode === 'normal' && (
                            <ExplainPanel key={`${database.id}:${sqlUp}`} databaseId={database.id} sql={sqlUp} />
                        )}
                    </form>
                ) : selected && activeRev ? (
                    <div className="space-y-5">
                        <div className="flex flex-wrap items-start justify-between gap-4">
                            <div className="min-w-0">
                                <div className="flex flex-wrap items-center gap-2">
                                    <span className={`rounded-full px-2.5 py-1 text-xs font-semibold ${statusStyle(displayStatus(selected))}`}>
                                        {displayStatus(selected)}
                                    </span>
                                    <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-700">
                                        Revisi #{activeRev.revisionNo} ({activeRev.status})
                                    </span>
                                    {selected.version !== null && (
                                        <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-600">
                                            Versi {selected.version}
                                        </span>
                                    )}
                                </div>
                                <h2 className="mt-3 break-words text-2xl font-semibold text-slate-900">{selected.title}</h2>
                                <p className="mt-1 text-sm text-slate-500">
                                    Dibuat oleh {selected.createdBy} · {formatDate(selected.createdAt)}
                                </p>
                            </div>

                            <div className="flex flex-wrap gap-2">
                                {canEdit && isCurrentDraft && (
                                    <>
                                        <button
                                            type="button"
                                            onClick={() => startEdit(selected)}
                                            className="rounded-lg border border-slate-300 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50"
                                        >
                                            Edit draft
                                        </button>
                                        <button
                                            type="button"
                                            onClick={() => void handleValidate(selected)}
                                            disabled={validating || submitting}
                                            className="rounded-lg border border-cyan-700 px-3 py-2 text-sm font-semibold text-cyan-800 hover:bg-cyan-50 disabled:opacity-50"
                                        >
                                            {validating ? 'Memvalidasi…' : 'Validasi'}
                                        </button>
                                        <button
                                            type="button"
                                            onClick={() => void handleSubmit(selected)}
                                            disabled={submitting || validating}
                                            className="rounded-lg bg-emerald-700 px-3.5 py-2 text-sm font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
                                        >
                                            {submitting ? 'Mengajukan…' : 'Ajukan'}
                                        </button>
                                    </>
                                )}
                                {canCreateNewRevision && (
                                    <button
                                        type="button"
                                        onClick={() => startEdit(selected)}
                                        className="rounded-lg border border-slate-300 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50"
                                    >
                                        Buat revisi baru
                                    </button>
                                )}
                            </div>
                        </div>

                        {validation && (
                            <div
                                className={`rounded-xl border px-4 py-3 text-sm ${
                                    validation.valid
                                        ? 'border-emerald-200 bg-emerald-50 text-emerald-900'
                                        : 'border-rose-200 bg-rose-50 text-rose-900'
                                }`}
                            >
                                <p className="font-semibold">
                                    {validation.valid ? 'Hasil validasi: Lolos' : 'Hasil validasi: Gagal'}
                                </p>
                                {validation.errors.length > 0 && (
                                    <ul className="mt-1.5 list-disc space-y-1 pl-5 text-xs">
                                        {validation.errors.map((e, i) => (
                                            <li key={i}>{e}</li>
                                        ))}
                                    </ul>
                                )}
                                {validation.warnings.length > 0 && (
                                    <ul className="mt-1.5 list-disc space-y-1 pl-5 text-xs text-amber-800">
                                        {validation.warnings.map((w, i) => (
                                            <li key={i}>Peringatan: {w}</li>
                                        ))}
                                    </ul>
                                )}
                            </div>
                        )}

                        {selected.revisions.length > 1 && (
                            <div className="flex flex-wrap items-center gap-2 border-b border-slate-100 pb-3">
                                <span className="text-xs font-medium text-slate-500">Revisi:</span>
                                {selected.revisions.map((r) => (
                                    <button
                                        key={r.id}
                                        type="button"
                                        onClick={() => setViewingRevId(r.id)}
                                        className={`rounded-lg px-2.5 py-1 text-xs font-semibold ${
                                            activeRev.id === r.id
                                                ? 'bg-slate-900 text-white'
                                                : 'bg-slate-100 text-slate-700 hover:bg-slate-200'
                                        }`}
                                    >
                                        #{r.revisionNo} ({r.status})
                                    </button>
                                ))}
                            </div>
                        )}

                        {activeRev.mode === 'batch' && (
                            <div className="grid gap-2 rounded-xl bg-slate-50 p-3 text-xs text-slate-700 sm:grid-cols-3">
                                <div>Tabel: <strong className="font-mono">{activeRev.batchTable}</strong></div>
                                <div>Kolom: <strong className="font-mono">{activeRev.batchColumn}</strong></div>
                                <div>Batch size: <strong className="font-mono">{activeRev.batchSize}</strong></div>
                            </div>
                        )}

                        <div>
                            <h3 className="mb-2 text-sm font-semibold text-slate-700">SQL Up</h3>
                            <pre className="max-h-96 overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-sm leading-6 text-slate-100">
                                <code>{activeRev.sqlUp}</code>
                            </pre>
                        </div>

                        {activeRev.irreversible ? (
                            <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
                                <strong>Irreversible:</strong> {activeRev.irreversibleReason || 'Tanpa alasan'}
                            </div>
                        ) : (
                            <div>
                                <h3 className="mb-2 text-sm font-semibold text-slate-700">SQL Down</h3>
                                <pre className="max-h-80 overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-sm leading-6 text-slate-100">
                                    <code>{activeRev.sqlDown || '—'}</code>
                                </pre>
                            </div>
                        )}

                        {selected.changeRequests.length > 0 && (
                            <div className="space-y-2 border-t border-slate-100 pt-4">
                                <h3 className="text-sm font-semibold text-slate-800">Change Requests</h3>
                                <ul className="divide-y divide-slate-100 rounded-xl border border-slate-200">
                                    {selected.changeRequests.map((cr) => (
                                        <li key={cr.id} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 text-sm">
                                            <div className="flex flex-wrap items-center gap-2">
                                                <span className="font-semibold text-slate-800">#{cr.id}</span>
                                                <span className="rounded bg-slate-100 px-2 py-0.5 text-xs font-medium uppercase text-slate-700">
                                                    {cr.action}
                                                </span>
                                                <span className="text-xs text-slate-500">
                                                    {cr.status} · oleh {cr.requestedBy}
                                                </span>
                                            </div>
                                            <Link
                                                to={`/change-requests/${cr.id}`}
                                                className="text-xs font-semibold text-cyan-800 hover:underline"
                                            >
                                                Lihat detail →
                                            </Link>
                                        </li>
                                    ))}
                                </ul>
                            </div>
                        )}

                        {canEdit && activeRev.mode === 'normal' && (
                            <ExplainPanel key={`${database.id}:${activeRev.id}:${activeRev.sqlUp}`} databaseId={database.id} sql={activeRev.sqlUp} />
                        )}
                    </div>
                ) : (
                    <div className="flex min-h-64 flex-col items-center justify-center text-center">
                        <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-cyan-50 text-xl text-cyan-800">↻</div>
                        <h2 className="mt-4 text-lg font-semibold text-slate-900">Pilih migration</h2>
                        <p className="mt-1 max-w-sm text-sm text-slate-500">
                            Buka item di daftar untuk melihat revisi SQL, hasil validasi, dan status approval.
                        </p>
                        {canEdit && migrations.length === 0 && (
                            <button
                                type="button"
                                onClick={startCreate}
                                className="mt-5 rounded-lg bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-700"
                            >
                                Buat draft pertama
                            </button>
                        )}
                    </div>
                )}
            </section>
        </div>
    )
}
