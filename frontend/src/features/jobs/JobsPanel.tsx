import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, errorMessage } from '../../lib/api'
import { formatDate } from '../../lib/format'
import { isActive, statusLabel, statusStyle, type Job } from './types'

export default function JobsPanel({ databaseId }: { databaseId: number }) {
    const [jobs, setJobs] = useState<Job[]>([])
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')

    useEffect(() => {
        let cancelled = false
        let timer: ReturnType<typeof setTimeout> | undefined

        async function load() {
            try {
                const result = await api<Job[]>(`/databases/${databaseId}/jobs`)
                if (cancelled) return
                setJobs(result)
                setError('')
                if (result.some((job) => isActive(job.status))) {
                    timer = setTimeout(load, 3000)
                }
            } catch (cause) {
                if (!cancelled) setError(errorMessage(cause))
            } finally {
                if (!cancelled) setLoading(false)
            }
        }

        void load()
        return () => {
            cancelled = true
            clearTimeout(timer)
        }
    }, [databaseId])

    if (loading) return <p className="text-sm text-slate-500">Memuat daftar job...</p>

    return (
        <div className="space-y-3">
            {error && (
                <div role="alert" className="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
                    {error}
                </div>
            )}
            {jobs.length === 0 ? (
                <p className="rounded-xl border border-dashed border-slate-300 px-4 py-8 text-center text-sm text-slate-500">
                    Belum ada job untuk database ini.
                </p>
            ) : (
                <ul className="space-y-2">
                    {jobs.map((job) => (
                        <li key={job.id}>
                            <Link
                                to={`/jobs/${job.id}`}
                                className="flex flex-col gap-2 rounded-xl border border-slate-200 bg-white p-4 hover:border-cyan-300 sm:flex-row sm:items-center sm:justify-between"
                            >
                                <div className="min-w-0">
                                    <p className="truncate font-semibold text-slate-900">
                                        #{job.id} {job.migrationTitle}
                                    </p>
                                    <p className="text-xs text-slate-500">
                                        {job.action} · {job.mode} · {formatDate(job.createdAt)}
                                    </p>
                                </div>
                                <span className={`self-start rounded-full px-2.5 py-1 text-xs font-semibold sm:self-auto ${statusStyle(job.status)}`}>
                                    {statusLabel(job)}
                                </span>
                            </Link>
                        </li>
                    ))}
                </ul>
            )}
        </div>
    )
}