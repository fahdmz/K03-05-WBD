import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, errorMessage } from '../../lib/api'

type ReviewItem = {
    id: number
    databaseId: number
    databaseName: string
    migrationId: number
    migrationTitle: string
    revisionId: number
    revisionNo: number
    action: string
    status: string
    requestedBy: string
    createdAt: string
    pendingGroups: string[]
}

function formatDate(value: string) {
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return value
    return new Intl.DateTimeFormat('id-ID', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function actionBadge(action: string) {
    switch (action) {
        case 'rollback':
        case 'revert':
            return 'bg-rose-50 text-rose-700'
        case 'forward':
        case 'resume':
            return 'bg-amber-50 text-amber-800'
        default:
            return 'bg-cyan-50 text-cyan-800'
    }
}

export default function ReviewsPage() {
    const [reviews, setReviews] = useState<ReviewItem[]>([])
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')

    async function loadReviews() {
        setLoading(true)
        setError('')
        try {
            const data = await api<ReviewItem[]>('/reviews')
            setReviews(data)
        } catch (err) {
            setError(errorMessage(err))
        } finally {
            setLoading(false)
        }
    }

    useEffect(() => {
        void loadReviews()
    }, [])

    return (
        <section className="space-y-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
                <div>
                    <p className="text-xs font-semibold uppercase tracking-wide text-cyan-700">Approval Leader</p>
                    <h1 className="mt-1 text-2xl font-semibold text-slate-900">Menunggu Review</h1>
                    <p className="mt-1 text-sm text-slate-500">
                        Daftar pengajuan perubahan database yang membutuhkan persetujuan Anda sebagai leader group.
                    </p>
                </div>
                <button
                    type="button"
                    onClick={() => void loadReviews()}
                    disabled={loading}
                    className="rounded-lg border border-slate-300 bg-white px-3.5 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50"
                >
                    {loading ? 'Memuat…' : 'Muat ulang'}
                </button>
            </div>

            {error && (
                <div role="alert" className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
                    {error}
                </div>
            )}

            <div className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm">
                {loading ? (
                    <p className="px-6 py-12 text-center text-sm text-slate-400">Memuat daftar review…</p>
                ) : reviews.length === 0 ? (
                    <div className="px-6 py-12 text-center">
                        <p className="text-base font-semibold text-slate-800">Tidak ada review yang menunggu</p>
                        <p className="mt-1 text-sm text-slate-500">
                            Semua change request pada database yang Anda pimpin sudah diproses.
                        </p>
                    </div>
                ) : (
                    <ul className="divide-y divide-slate-200">
                        {reviews.map((item) => (
                            <li key={item.id} className="flex flex-col gap-4 p-5 sm:flex-row sm:items-center sm:justify-between">
                                <div className="min-w-0 space-y-1.5">
                                    <div className="flex flex-wrap items-center gap-2">
                                        <span className={`rounded-full px-2.5 py-0.5 text-xs font-semibold uppercase ${actionBadge(item.action)}`}>
                                            {item.action}
                                        </span>
                                        <span className="rounded-full bg-slate-100 px-2.5 py-0.5 text-xs font-semibold text-slate-700">
                                            {item.databaseName}
                                        </span>
                                        <span className="text-xs text-slate-500">Revisi #{item.revisionNo}</span>
                                    </div>
                                    <h2 className="break-words text-base font-semibold text-slate-900">
                                        {item.migrationTitle}
                                    </h2>
                                    <p className="text-xs text-slate-500">
                                        Diajukan oleh <span className="font-medium text-slate-700">{item.requestedBy}</span> · {formatDate(item.createdAt)}
                                    </p>
                                    <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                                        <span className="text-xs text-slate-500">Group Anda:</span>
                                        {item.pendingGroups.map((groupName) => (
                                            <span
                                                key={groupName}
                                                className="rounded-md border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-800"
                                            >
                                                {groupName}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                                <div className="shrink-0">
                                    <Link
                                        to={`/change-requests/${item.id}`}
                                        className="inline-flex w-full items-center justify-center rounded-lg bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-700 sm:w-auto"
                                    >
                                        Tinjau
                                    </Link>
                                </div>
                            </li>
                        ))}
                    </ul>
                )}
            </div>
        </section>
    )
}
