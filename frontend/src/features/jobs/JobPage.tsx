import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import Button from '../../components/Button'
import { api, ApiError, errorMessage } from '../../lib/api'
import { formatDate } from '../../lib/format'
import { useToast } from '../../lib/toast'
import { isActive, statusLabel, statusStyle, type Job } from './types'

type CreatedRequest = { changeRequestId: number }

function Field({ label, value }: { label: string; value: string }) {
    return (
        <div>
            <dt className="text-xs font-medium uppercase tracking-wide text-slate-400">{label}</dt>
            <dd className="mt-1 break-all text-sm text-slate-800">{value}</dd>
        </div>
    )
}

export default function JobPage() {
    const { id } = useParams()
    const toast = useToast()
    const [job, setJob] = useState<Job | null>(null)
    const [error, setError] = useState('')
    const [submitting, setSubmitting] = useState('')

    useEffect(() => {
        let cancelled = false
        let timer: ReturnType<typeof setTimeout> | undefined

        async function poll() {
            try {
                const result = await api<Job>(`/jobs/${id}`)
                if (cancelled) return
                setJob(result)
                setError('')
                if (isActive(result.status)) {
                    timer = setTimeout(poll, 1500)
                }
            } catch (cause) {
                if (cancelled) return
                setError(errorMessage(cause))
                const permanent = cause instanceof ApiError && cause.status < 500
                if (!permanent) {
                    timer = setTimeout(poll, 3000)
                }
            }
        }

        void poll()
        return () => {
            cancelled = true
            clearTimeout(timer)
        }
    }, [id])

    async function createRequest(kind: 'resume' | 'revert') {
        setSubmitting(kind)
        try {
            const result = await api<CreatedRequest>(`/jobs/${id}/${kind}`, { method: 'POST' })
            toast.success(`Change request #${result.changeRequestId} dibuat dan menunggu review`)
        } catch (cause) {
            toast.error(errorMessage(cause))
        } finally {
            setSubmitting('')
        }
    }

    if (!job) {
        return (
            <div className="space-y-4">
                <Link to="/" className="text-sm font-semibold text-cyan-800">
                    ← Kembali
                </Link>
                <p className={`text-sm ${error ? 'text-rose-700' : 'text-slate-500'}`}>{error || 'Memuat job...'}</p>
            </div>
        )
    }

    const active = isActive(job.status)

    return (
        <div className="space-y-6">
            <Link to="/" className="text-sm font-semibold text-cyan-800">
                ← Kembali
            </Link>

            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0">
                    <p className="text-sm font-medium uppercase tracking-[0.18em] text-cyan-700">Job #{job.id}</p>
                    <h1 className="mt-2 break-words text-2xl font-semibold text-slate-950 sm:text-3xl">{job.migrationTitle}</h1>
                    <p className="mt-1 text-sm text-slate-500">
                        {job.action} · mode {job.mode}
                    </p>
                </div>
                <span className={`self-start rounded-full px-3 py-1 text-sm font-semibold ${statusStyle(job.status)}`}>
                    {statusLabel(job)}
                </span>
            </div>

            {active && (
                <p className="flex items-center gap-2 text-sm text-slate-500">
                    <span className="h-2 w-2 animate-pulse rounded-full bg-cyan-600" />
                    Memantau status otomatis...
                </p>
            )}
            {error && <p className="text-sm text-rose-700">{error}</p>}

            {job.mode === 'batch' && (
                <div className="grid gap-3 sm:grid-cols-2">
                    <div className="rounded-xl border border-slate-200 bg-white p-4">
                        <p className="text-xs font-medium uppercase tracking-wide text-slate-400">Batch committed</p>
                        <p className="mt-1 text-2xl font-semibold text-slate-900">{job.batchesCommitted.toLocaleString('id-ID')}</p>
                    </div>
                    <div className="rounded-xl border border-slate-200 bg-white p-4">
                        <p className="text-xs font-medium uppercase tracking-wide text-slate-400">Baris committed</p>
                        <p className="mt-1 text-2xl font-semibold text-slate-900">{job.rowsCommitted.toLocaleString('id-ID')}</p>
                    </div>
                </div>
            )}

            {job.status === 'partial' && (
                <div className="space-y-3 rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
                    <p>
                        Proses berhenti di tengah. Batch yang sudah committed tetap tersimpan, dan migration berikutnya di
                        database ini ditahan sampai job ini dilanjutkan atau dibatalkan. Kedua aksi tetap melewati review dan
                        approval.
                    </p>
                    {job.mode === 'batch' && (
                        <div className="flex flex-col gap-2 sm:flex-row">
                            <Button disabled={submitting !== ''} loading={submitting === 'resume'} onClick={() => void createRequest('resume')}>
                                Lanjutkan dari checkpoint
                            </Button>
                            <Button
                                variant="danger"
                                disabled={submitting !== ''}
                                loading={submitting === 'revert'}
                                onClick={() => void createRequest('revert')}
                            >
                                Batalkan batch committed
                            </Button>
                        </div>
                    )}
                </div>
            )}

            {job.status === 'uncertain' && (
                <p className="rounded-xl border border-violet-200 bg-violet-50 p-4 text-sm text-violet-900">
                    Hasil commit belum pasti. Worker akan memeriksa bukti eksekusi di database target sebelum mencoba ulang, dan
                    migration berikutnya ditahan sampai statusnya jelas.
                </p>
            )}

            {job.lastError && (
                <div className="rounded-xl border border-rose-200 bg-rose-50 p-4">
                    <p className="text-sm font-semibold text-rose-800">
                        Error terakhir{job.lastSqlstate ? ` (SQLSTATE ${job.lastSqlstate})` : ''}
                    </p>
                    <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words text-xs text-rose-900">{job.lastError}</pre>
                </div>
            )}

            <dl className="grid gap-4 rounded-xl border border-slate-200 bg-white p-4 sm:grid-cols-2">
                <Field label="Attempt" value={`${job.attempt} dari ${job.maxAttempts}`} />
                <Field label="Change request" value={`#${job.changeRequestId}`} />
                <Field label="Dibuat" value={formatDate(job.createdAt)} />
                <Field label="Mulai" value={formatDate(job.startedAt)} />
                <Field label="Selesai" value={formatDate(job.finishedAt)} />
                <Field label="Execution ID" value={job.executionId} />
            </dl>
        </div>
    )
}