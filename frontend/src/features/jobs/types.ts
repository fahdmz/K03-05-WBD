export type JobStatus = 'queued' | 'running' | 'success' | 'failed' | 'partial' | 'uncertain'

export type Job = {
    id: number
    changeRequestId: number
    databaseId: number
    migrationId: number
    migrationTitle: string
    action: 'apply' | 'rollback' | 'forward' | 'resume' | 'revert'
    mode: 'normal' | 'batch'
    executionId: string
    status: JobStatus
    attempt: number
    maxAttempts: number
    batchesCommitted: number
    rowsCommitted: number
    lastError: string | null
    lastSqlstate: string | null
    createdAt: string
    startedAt: string | null
    finishedAt: string | null
}

const labels: Record<JobStatus, string> = {
    queued: 'Pending (Queue)',
    running: 'Running',
    success: 'Success',
    failed: 'Failed',
    partial: 'Partial',
    uncertain: 'Uncertain',
}

const styles: Record<JobStatus, string> = {
    queued: 'bg-slate-100 text-slate-700',
    running: 'bg-cyan-50 text-cyan-800',
    success: 'bg-emerald-50 text-emerald-700',
    failed: 'bg-rose-50 text-rose-700',
    partial: 'bg-amber-50 text-amber-800',
    uncertain: 'bg-violet-50 text-violet-700',
}

export function statusLabel(job: Pick<Job, 'status' | 'action'>) {
    if (job.status === 'partial' && job.action === 'revert') return 'Partially reverted'
    return labels[job.status]
}

export function statusStyle(status: JobStatus) {
    return styles[status]
}

export function isActive(status: JobStatus) {
    // uncertain tetep dipantau, worker bakal ngecek ulang otomatis
    return status === 'queued' || status === 'running' || status === 'uncertain'
}