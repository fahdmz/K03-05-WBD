import { statusLabel, statusStyle, type Job } from './types'

type Props = {
    job: Pick<Job, 'status' | 'action' | 'batchesCommitted' | 'rowsCommitted'>
}

function hint(job: Props['job']) {
    const revert = job.action === 'revert'
    switch (job.status) {
        case 'queued':
            return 'Menunggu giliran worker.'
        case 'running':
            return 'Batch sedang diproses. Angka bertambah setiap kali satu batch selesai commit.'
        case 'partial':
            return revert
                ? 'Revert berhenti di tengah: sebagian batch sudah dibatalkan, sisanya masih dalam kondisi setelah migration.'
                : 'Proses berhenti di tengah: hanya sebagian batch yang sudah diterapkan.'
        case 'uncertain':
            return 'Hasil batch terakhir belum pasti. Worker akan mencocokkan checkpoint di database target.'
        case 'success':
            return revert ? 'Semua batch committed berhasil dibatalkan.' : 'Semua batch selesai.'
        case 'failed':
            return 'Job gagal.'
    }
}

export default function BatchProgress({ job }: Props) {
    const revert = job.action === 'revert'
    const running = job.status === 'running'

    return (
        <section className="space-y-3 rounded-xl border border-slate-200 bg-white p-4">
            <div className="flex flex-wrap items-center justify-between gap-2">
                <h2 className="text-sm font-semibold text-slate-800">Progres batch</h2>
                <span className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold ${statusStyle(job.status)}`}>
                    {running && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />}
                    {statusLabel(job)}
                </span>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
                <div className="rounded-lg bg-slate-50 p-3">
                    <p className="text-xs font-medium uppercase tracking-wide text-slate-400">
                        {revert ? 'Batch di-revert' : 'Batch committed'}
                    </p>
                    <p className="mt-1 text-2xl font-semibold tabular-nums text-slate-900">
                        {job.batchesCommitted.toLocaleString('id-ID')}
                    </p>
                </div>
                <div className="rounded-lg bg-slate-50 p-3">
                    <p className="text-xs font-medium uppercase tracking-wide text-slate-400">
                        {revert ? 'Baris di-revert' : 'Baris committed'}
                    </p>
                    <p className="mt-1 text-2xl font-semibold tabular-nums text-slate-900">
                        {job.rowsCommitted.toLocaleString('id-ID')}
                    </p>
                </div>
            </div>

            {running && (
                <div className="h-1.5 overflow-hidden rounded-full bg-cyan-100" aria-hidden="true">
                    <div className="h-full w-1/3 animate-pulse rounded-full bg-cyan-600" />
                </div>
            )}

            <p className="text-sm text-slate-600">{hint(job)}</p>
            <p className="text-xs text-slate-500">
                Tiap batch di-commit sendiri. Batch yang sudah committed tetap tersimpan walaupun batch berikutnya gagal atau
                job berhenti.
            </p>
        </section>
    )
}
