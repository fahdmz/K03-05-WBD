import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, errorMessage } from '../../lib/api'
import { useAuth } from '../../lib/auth'

type GroupApproval = {
    groupId: number
    groupName: string
    leaderId: number | null
    leaderUsername: string | null
    decision: 'approve' | 'reject' | null
    comment: string | null
    decidedAt: string | null
}

type ApprovalStatus = {
    approved: boolean
    rejected: boolean
    missingLeaders: string[]
    groups: GroupApproval[]
}

type ValidationResult = {
    valid: boolean
    errors: string[]
    warnings: string[]
}

type ChangeRequestDetail = {
    id: number
    databaseId: number
    databaseName: string
    migrationId: number
    migrationTitle: string
    revisionId: number
    revisionNo: number
    action: string
    targetExecutionId: string | null
    status: string
    validation: ValidationResult | null
    requestedBy: string
    createdAt: string
    updatedAt: string
    mode: 'normal' | 'batch'
    sqlUp: string
    sqlDown: string | null
    irreversible: boolean
    irreversibleReason: string | null
    batchTable: string | null
    batchColumn: string | null
    batchSize: number | null
    approval: ApprovalStatus
    canRun: boolean
}

function formatDate(value: string | null) {
    if (!value) return '—'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return value
    return new Intl.DateTimeFormat('id-ID', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function statusLabel(status: string, action: string) {
    switch (status) {
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
            return 'Success'
        case 'failed':
            return 'Failed'
        case 'partial':
            return action === 'revert' ? 'Partially Reverted' : 'Partial'
        case 'uncertain':
            return 'Uncertain'
        default:
            return status
    }
}

function statusBadge(status: string) {
    switch (status) {
        case 'success':
        case 'approved':
            return 'bg-emerald-50 text-emerald-700'
        case 'failed':
        case 'rejected':
            return 'bg-rose-50 text-rose-700'
        case 'queued':
        case 'running':
            return 'bg-cyan-50 text-cyan-800'
        default:
            return 'bg-amber-50 text-amber-800'
    }
}

export default function ChangeRequestPage() {
    const { id } = useParams()
    const { user } = useAuth()
    const [detail, setDetail] = useState<ChangeRequestDetail | null>(null)
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')
    const [notice, setNotice] = useState('')
    const [comment, setComment] = useState('')
    const [submitting, setSubmitting] = useState<'approve' | 'reject' | 'run' | null>(null)
    const [queuedJob, setQueuedJob] = useState<{ jobId: number; executionId: string } | null>(null)

    async function loadDetail() {
        if (!id) return
        setLoading(true)
        setError('')
        try {
            const data = await api<ChangeRequestDetail>(`/change-requests/${id}`)
            setDetail(data)
        } catch (err) {
            setError(errorMessage(err))
        } finally {
            setLoading(false)
        }
    }

    useEffect(() => {
        setQueuedJob(null)
        setNotice('')
        void loadDetail()
    }, [id])

    async function handleDecision(decision: 'approve' | 'reject') {
        if (!detail) return
        if (decision === 'reject' && !comment.trim()) {
            setError('Alasan penolakan wajib diisi.')
            return
        }
        setSubmitting(decision)
        setError('')
        setNotice('')
        try {
            const updated = await api<ChangeRequestDetail>(`/change-requests/${detail.id}/${decision}`, {
                method: 'POST',
                body: { comment: comment.trim() },
            })
            setDetail(updated)
            setComment('')
            setNotice(decision === 'approve' ? 'Persetujuan berhasil dicatat.' : 'Change request berhasil ditolak.')
        } catch (err) {
            setError(errorMessage(err))
        } finally {
            setSubmitting(null)
        }
    }

    async function handleRun() {
        if (!detail) return
        if (!window.confirm(`Antrekan eksekusi (${detail.action}) untuk "${detail.migrationTitle}"?`)) return
        setSubmitting('run')
        setError('')
        setNotice('')
        try {
            const res = await api<{ jobId: number; executionId: string }>(`/change-requests/${detail.id}/run`, {
                method: 'POST',
            })
            setQueuedJob(res)
            setNotice(`Job #${res.jobId} berhasil dimasukkan ke antrean (202 Accepted).`)
            await loadDetail()
        } catch (err) {
            setError(errorMessage(err))
            await loadDetail()
        } finally {
            setSubmitting(null)
        }
    }

    if (loading) {
        return <p className="py-12 text-center text-sm text-slate-400">Memuat detail change request…</p>
    }

    if (!detail) {
        return (
            <div className="space-y-4">
                {error && <div role="alert" className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">{error}</div>}
                <Link to="/reviews" className="text-sm font-semibold text-cyan-800 hover:underline">
                    ← Kembali ke daftar review
                </Link>
            </div>
        )
    }

    const canReview =
        (detail.status === 'pending_review' || detail.status === 'approved') &&
        user !== null &&
        detail.approval.groups.some((g) => g.leaderId === user.id)

    const canRun = detail.status === 'approved' && detail.approval.approved && detail.canRun

    return (
        <section className="space-y-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
                <Link to="/reviews" className="text-sm font-semibold text-cyan-800 hover:underline">
                    ← Kembali ke Reviews
                </Link>
                <button
                    type="button"
                    onClick={() => void loadDetail()}
                    className="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50"
                >
                    Muat ulang
                </button>
            </div>

            {error && (
                <div role="alert" className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
                    {error}
                </div>
            )}
            {notice && (
                <div role="status" className="rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800">
                    {notice}
                </div>
            )}
            {queuedJob && (
                <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-cyan-200 bg-cyan-50 px-4 py-3 text-sm text-cyan-900">
                    <div>
                        <p className="font-semibold">Job #{queuedJob.jobId} masuk antrean</p>
                        <p className="font-mono text-xs text-cyan-800">Execution ID: {queuedJob.executionId}</p>
                    </div>
                    <Link
                        to={`/jobs/${queuedJob.jobId}`}
                        className="rounded-lg bg-cyan-800 px-3.5 py-1.5 text-xs font-semibold text-white hover:bg-cyan-700"
                    >
                        Pantau Job
                    </Link>
                </div>
            )}

            <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-sm">
                <div className="flex flex-wrap items-start justify-between gap-4">
                    <div className="min-w-0 space-y-2">
                        <div className="flex flex-wrap items-center gap-2">
                            <span className={`rounded-full px-2.5 py-1 text-xs font-semibold ${statusBadge(detail.status)}`}>
                                {statusLabel(detail.status, detail.action)}
                            </span>
                            <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold uppercase text-slate-700">
                                {detail.action}
                            </span>
                            <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-700">
                                {detail.databaseName}
                            </span>
                            <span className="text-xs text-slate-500">Revisi #{detail.revisionNo} · Mode {detail.mode}</span>
                        </div>
                        <h1 className="break-words text-2xl font-semibold text-slate-900">{detail.migrationTitle}</h1>
                        <p className="text-sm text-slate-500">
                            Diajukan oleh <span className="font-medium text-slate-700">{detail.requestedBy}</span> · {formatDate(detail.createdAt)}
                        </p>
                    </div>

                    {canRun && (
                        <button
                            type="button"
                            onClick={() => void handleRun()}
                            disabled={submitting !== null}
                            className="rounded-lg bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
                        >
                            {submitting === 'run' ? 'Mengantrekan…' : 'Jalankan'}
                        </button>
                    )}
                </div>

                {detail.approval.missingLeaders.length > 0 && (
                    <div role="alert" className="mt-4 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
                        <strong>Leader belum ditetapkan:</strong> Group{' '}
                        <span className="font-semibold">{detail.approval.missingLeaders.join(', ')}</span> belum memiliki leader aktif.
                        Minta admin untuk menetapkan leader agar pengajuan ini dapat disetujui.
                    </div>
                )}
            </div>

            {/* Daftar Approval Group */}
            <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-sm">
                <h2 className="text-base font-semibold text-slate-900">Status Persetujuan Group</h2>
                <p className="mt-1 text-xs text-slate-500">
                    Semua leader dari setiap group yang memiliki akses (View maupun Edit) ke database ini wajib menyetujui.
                </p>
                <div className="mt-4 overflow-x-auto">
                    <table className="w-full min-w-[540px] border-collapse text-left text-sm">
                        <thead>
                            <tr className="border-b border-slate-200 text-xs uppercase text-slate-500">
                                <th className="py-2 pr-4">Group</th>
                                <th className="py-2 pr-4">Leader</th>
                                <th className="py-2 pr-4">Keputusan</th>
                                <th className="py-2 pr-4">Komentar</th>
                                <th className="py-2">Waktu</th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-slate-100">
                            {detail.approval.groups.map((g) => (
                                <tr key={g.groupId}>
                                    <td className="py-2.5 pr-4 font-medium text-slate-900">{g.groupName}</td>
                                    <td className="py-2.5 pr-4 text-slate-700">
                                        {g.leaderUsername ?? <span className="text-amber-700">Belum ditetapkan</span>}
                                    </td>
                                    <td className="py-2.5 pr-4">
                                        {g.decision === 'approve' ? (
                                            <span className="rounded-full bg-emerald-50 px-2.5 py-0.5 text-xs font-semibold text-emerald-700">
                                                Approved
                                            </span>
                                        ) : g.decision === 'reject' ? (
                                            <span className="rounded-full bg-rose-50 px-2.5 py-0.5 text-xs font-semibold text-rose-700">
                                                Rejected
                                            </span>
                                        ) : (
                                            <span className="rounded-full bg-slate-100 px-2.5 py-0.5 text-xs font-semibold text-slate-600">
                                                Menunggu
                                            </span>
                                        )}
                                    </td>
                                    <td className="py-2.5 pr-4 text-slate-600">{g.comment || '—'}</td>
                                    <td className="py-2.5 text-xs text-slate-500">{formatDate(g.decidedAt)}</td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                </div>

                {canReview && (
                    <div className="mt-5 space-y-3 border-t border-slate-100 pt-4">
                        <label className="block text-sm font-medium text-slate-700">
                            Catatan review (opsional untuk Approve, wajib untuk Reject)
                            <textarea
                                value={comment}
                                onChange={(e) => setComment(e.target.value)}
                                rows={3}
                                maxLength={2000}
                                placeholder="Tuliskan catatan kompatibilitas atau alasan penolakan…"
                                className="mt-1.5 w-full rounded-lg border border-slate-300 px-3 py-2 text-sm outline-none focus:border-cyan-600 focus:ring-2 focus:ring-cyan-100"
                            />
                        </label>
                        <div className="flex flex-wrap gap-2">
                            <button
                                type="button"
                                onClick={() => void handleDecision('approve')}
                                disabled={submitting !== null}
                                className="rounded-lg bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600 disabled:opacity-50"
                            >
                                {submitting === 'approve' ? 'Menyimpan…' : 'Setujui (Approve)'}
                            </button>
                            <button
                                type="button"
                                onClick={() => void handleDecision('reject')}
                                disabled={submitting !== null}
                                className="rounded-lg bg-rose-600 px-4 py-2 text-sm font-semibold text-white hover:bg-rose-500 disabled:opacity-50"
                            >
                                {submitting === 'reject' ? 'Menyimpan…' : 'Tolak (Reject)'}
                            </button>
                        </div>
                    </div>
                )}
            </div>

            {/* Hasil Validasi */}
            {detail.validation && (
                <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-sm">
                    <div className="flex items-center justify-between gap-2">
                        <h2 className="text-base font-semibold text-slate-900">Hasil Validasi</h2>
                        <span className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${detail.validation.valid ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'}`}>
                            {detail.validation.valid ? 'Lolos validasi' : 'Validasi gagal'}
                        </span>
                    </div>
                    {detail.validation.warnings?.length > 0 && (
                        <ul className="mt-3 list-disc space-y-1 pl-5 text-sm text-amber-800">
                            {detail.validation.warnings.map((w, i) => (
                                <li key={i}>{w}</li>
                            ))}
                        </ul>
                    )}
                    {detail.validation.errors?.length > 0 && (
                        <ul className="mt-3 list-disc space-y-1 pl-5 text-sm text-rose-700">
                            {detail.validation.errors.map((e, i) => (
                                <li key={i}>{e}</li>
                            ))}
                        </ul>
                    )}
                </div>
            )}

            {/* Preview SQL */}
            <div className="space-y-4 rounded-2xl border border-slate-200 bg-white p-5 shadow-sm">
                <h2 className="text-base font-semibold text-slate-900">Rencana Perubahan SQL</h2>
                {detail.mode === 'batch' && (
                    <div className="grid gap-2 rounded-xl bg-slate-50 p-3 text-xs text-slate-700 sm:grid-cols-3">
                        <div>Tabel: <strong className="font-mono">{detail.batchTable}</strong></div>
                        <div>Kolom cursor: <strong className="font-mono">{detail.batchColumn}</strong></div>
                        <div>Batch size: <strong className="font-mono">{detail.batchSize}</strong></div>
                    </div>
                )}
                <div>
                    <h3 className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-slate-500">SQL Up</h3>
                    <pre className="max-h-96 overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-sm leading-6 text-slate-100">
                        <code>{detail.sqlUp}</code>
                    </pre>
                </div>
                {detail.irreversible ? (
                    <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
                        <strong>Irreversible:</strong> {detail.irreversibleReason}
                    </div>
                ) : (
                    <div>
                        <h3 className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-slate-500">SQL Down</h3>
                        <pre className="max-h-96 overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-sm leading-6 text-slate-100">
                            <code>{detail.sqlDown || '—'}</code>
                        </pre>
                    </div>
                )}
            </div>
        </section>
    )
}
