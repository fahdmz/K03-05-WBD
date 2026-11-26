import { useEffect, useState } from 'react'
import Button from '../../components/Button'
import { api, errorMessage } from '../../lib/api'
import { formatDate } from '../../lib/format'
import { useToast } from '../../lib/toast'
import { computePositions } from './position'
import type { CreatedRequest, HistoryAction, HistoryEntry, HistoryStatus } from './types'

const actionLabel: Record<HistoryAction, string> = {
    apply: 'Apply',
    rollback: 'Rollback',
    forward: 'Forward',
    revert: 'Revert batch',
}

const actionStyle: Record<HistoryAction, string> = {
    apply: 'bg-emerald-50 text-emerald-700',
    rollback: 'bg-amber-50 text-amber-800',
    forward: 'bg-cyan-50 text-cyan-800',
    revert: 'bg-violet-50 text-violet-800',
}

const statusBadge: Record<Exclude<HistoryStatus, 'success'>, { label: string; style: string }> = {
    failed: { label: 'Failed', style: 'bg-rose-50 text-rose-700' },
    partial: { label: 'Partial', style: 'bg-amber-50 text-amber-800' },
}

const cardBorder: Record<HistoryStatus, string> = {
    success: 'border-slate-200',
    failed: 'border-rose-300',
    partial: 'border-amber-300',
}

type Props = {
    databaseId: number
    canEdit: boolean
}

export default function HistoryPanel({ databaseId, canEdit }: Props) {
    const toast = useToast()
    const [hideRollback, setHideRollback] = useState(false)
    const [entries, setEntries] = useState<HistoryEntry[]>([])
    const [fullHistory, setFullHistory] = useState<HistoryEntry[]>([])
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')
    const [submitting, setSubmitting] = useState('')
    const [expanded, setExpanded] = useState<string | null>(null)

    useEffect(() => {
        let cancelled = false

        async function load() {
            try {
                const [visible, full] = await Promise.all([
                    api<HistoryEntry[]>(`/databases/${databaseId}/history?hideRollback=${hideRollback}`),
                    api<HistoryEntry[]>(`/databases/${databaseId}/history`),
                ])
                if (cancelled) return
                setEntries(visible)
                setFullHistory(full)
                setError('')
            } catch (cause) {
                if (!cancelled) setError(errorMessage(cause))
            } finally {
                if (!cancelled) setLoading(false)
            }
        }

        void load()
        return () => {
            cancelled = true
        }
    }, [databaseId, hideRollback])

    async function createRequest(kind: 'rollback' | 'forward', migrationId: number) {
        setSubmitting(kind)
        try {
            const result = await api<CreatedRequest>(`/migrations/${migrationId}/${kind}`, { method: 'POST' })
            toast.success(`Change request #${result.changeRequestId} dibuat dan menunggu review`)
        } catch (cause) {
            toast.error(errorMessage(cause))
        } finally {
            setSubmitting('')
        }
    }

    const { top, rollbackTarget, forwardTarget } = computePositions(fullHistory)

    return (
        <div className="space-y-4">
            <div className="flex flex-col gap-3 rounded-2xl border border-slate-200 bg-white p-4 shadow-sm sm:flex-row sm:items-center sm:justify-between">
                <div>
                    <p className="text-xs font-medium uppercase tracking-wide text-slate-400">Posisi aktif</p>
                    <p className="mt-1 text-lg font-semibold text-slate-900">
                        {top === 0 ? 'Belum ada migration aktif' : `Versi ${top}`}
                    </p>
                </div>
                {canEdit && (
                    <div className="flex flex-col gap-2 sm:flex-row">
                        <Button
                            variant="secondary"
                            disabled={!rollbackTarget || submitting !== ''}
                            loading={submitting === 'rollback'}
                            onClick={() => rollbackTarget && void createRequest('rollback', rollbackTarget.migrationId)}
                        >
                            {rollbackTarget ? `Rollback v${rollbackTarget.version} ${rollbackTarget.title}` : 'Rollback'}
                        </Button>
                        <Button
                            variant="secondary"
                            disabled={!forwardTarget || submitting !== ''}
                            loading={submitting === 'forward'}
                            onClick={() => forwardTarget && void createRequest('forward', forwardTarget.migrationId)}
                        >
                            {forwardTarget ? `Forward v${forwardTarget.version} ${forwardTarget.title}` : 'Forward'}
                        </Button>
                    </div>
                )}
            </div>

            <label className="inline-flex items-center gap-2 text-sm text-slate-700">
                <input
                    type="checkbox"
                    checked={hideRollback}
                    onChange={(e) => {
                        setLoading(true)
                        setHideRollback(e.target.checked)
                    }}
                    className="h-4 w-4 rounded border-slate-300"
                />
                Hide rollback
            </label>

            {error && (
                <div role="alert" className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
                    {error}
                </div>
            )}

            {loading ? (
                <p className="text-sm text-slate-500">Memuat migration history...</p>
            ) : entries.length === 0 ? (
                <p className="rounded-xl border border-dashed border-slate-300 px-4 py-8 text-center text-sm text-slate-500">
                    Belum ada eksekusi yang tercatat.
                </p>
            ) : (
                <ol className="space-y-3">
                    {entries.map((entry) => {
                        const key = `${entry.executionId}-${entry.attempt}`
                        return (
                            <li key={key} className={`rounded-xl border bg-white p-4 ${cardBorder[entry.status]}`}>
                                <div className="flex flex-wrap items-center gap-2">
                                    <span className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${actionStyle[entry.action]}`}>
                                        {actionLabel[entry.action]}
                                    </span>
                                    {entry.status !== 'success' && (
                                        <span
                                            className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${statusBadge[entry.status].style}`}
                                        >
                                            {statusBadge[entry.status].label}
                                        </span>
                                    )}
                                    <span className="font-semibold text-slate-900">
                                        {entry.version !== null ? `v${entry.version} ` : ''}
                                        {entry.title}
                                    </span>
                                    <span className="text-xs text-slate-500">
                                        revisi {entry.revisionNo} · attempt {entry.attempt}
                                        {entry.mode === 'batch' ? ` · ${entry.batches ?? 0} batch` : ''}
                                    </span>
                                </div>
                                <p className="mt-2 text-xs text-slate-500">
                                    {entry.requestedBy} · {formatDate(entry.finishedAt)}
                                    {entry.rowsAffected !== null ? ` · ${entry.rowsAffected.toLocaleString('id-ID')} baris` : ''}
                                </p>
                                <p className="mt-1 break-all font-mono text-[11px] text-slate-400">
                                    execution {entry.executionId}
                                    {entry.revertsExecutionId ? ` · membatalkan ${entry.revertsExecutionId}` : ''}
                                </p>
                                {entry.errorMessage && (
                                    <div className="mt-2 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-800">
                                        {entry.sqlstate ? `SQLSTATE ${entry.sqlstate}: ` : ''}
                                        {entry.errorMessage}
                                    </div>
                                )}
                                <button
                                    onClick={() => setExpanded(expanded === key ? null : key)}
                                    className="mt-2 text-xs font-semibold text-cyan-800"
                                >
                                    {expanded === key ? 'Sembunyikan SQL' : 'Lihat SQL'}
                                </button>
                                {expanded === key && (
                                    <pre className="mt-2 max-h-64 overflow-auto rounded-lg bg-slate-900 p-3 text-xs text-slate-100">
                                        {entry.executedSql}
                                    </pre>
                                )}
                            </li>
                        )
                    })}
                </ol>
            )}
        </div>
    )
}