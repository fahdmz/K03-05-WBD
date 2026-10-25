import { useState } from 'react'
import { api, errorMessage } from '../../lib/api'

type PlanNode = {
    'Node Type': string
    'Relation Name'?: string
    'Index Name'?: string
    'Join Type'?: string
    'Startup Cost': number
    'Total Cost': number
    'Plan Rows': number
    Plans?: PlanNode[]
}

type ExplainResult = { databaseId: number; plan: { Plan: PlanNode }[] }

function PlanTree({ node }: { node: PlanNode }) {
    return (
        <li className="py-2">
            <p className="font-semibold text-slate-900">{node['Node Type']}</p>
            <dl className="flex flex-wrap gap-x-5 gap-y-1 text-sm text-slate-700">
                <div><dt className="inline">Estimated cost: </dt><dd className="inline">{node['Startup Cost']}–{node['Total Cost']}</dd></div>
                <div><dt className="inline">Estimated rows: </dt><dd className="inline">{node['Plan Rows']}</dd></div>
                {node['Relation Name'] && <div><dt className="inline">Tabel: </dt><dd className="inline">{node['Relation Name']}</dd></div>}
                {node['Index Name'] && <div><dt className="inline">Index: </dt><dd className="inline">{node['Index Name']}</dd></div>}
                {node['Join Type'] && <div><dt className="inline">Join: </dt><dd className="inline">{node['Join Type']}</dd></div>}
            </dl>
            {node.Plans && <ul className="ml-3 border-l border-slate-300 pl-3">{node.Plans.map((child, index) => <PlanTree key={index} node={child} />)}</ul>}
        </li>
    )
}

export default function ExplainPanel({ databaseId, sql }: { databaseId: number; sql: string }) {
    const [result, setResult] = useState<ExplainResult | null>(null)
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState('')

    async function explain() {
        setLoading(true)
        setResult(null)
        setError('')
        try {
            setResult(await api<ExplainResult>(`/databases/${databaseId}/explain`, {
                method: 'POST', body: { sql },
            }))
        } catch (cause) {
            setError(errorMessage(cause))
        } finally {
            setLoading(false)
        }
    }

    return (
        <section aria-label="Execution plan" className="min-w-0 border-t border-slate-200 pt-4">
            <button type="button" onClick={() => void explain()} disabled={loading || !sql.trim()} className="rounded-lg border border-slate-300 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-cyan-700 disabled:cursor-not-allowed disabled:opacity-50">
                {loading ? 'Menganalisis…' : 'Lihat execution plan'}
            </button>
            <p className="mt-2 text-xs text-slate-600">Satu statement SELECT, INSERT, UPDATE, atau DELETE. CREATE TABLE dan ALTER TABLE tidak didukung. Hasil berupa perkiraan; ANALYZE tidak dijalankan dan cost bukan waktu dalam milidetik.</p>
            {loading && <p role="status" className="mt-3 text-sm text-slate-600">Memuat execution plan…</p>}
            {error && <p role="alert" className="mt-3 break-words text-sm text-rose-800">{error}</p>}
            {result && <div className="mt-4 min-w-0" role="status">
                <h3 className="font-semibold text-slate-900">Execution plan</h3>
                <div className="overflow-auto"><ul>{result.plan.map((item, index) => <PlanTree key={index} node={item.Plan} />)}</ul></div>
                <details className="mt-3 text-sm text-slate-700">
                    <summary className="cursor-pointer">JSON lengkap</summary>
                    <pre className="mt-2 max-h-80 overflow-auto rounded-lg bg-slate-950 p-3 text-xs text-slate-100">{JSON.stringify(result.plan, null, 2)}</pre>
                </details>
            </div>}
        </section>
    )
}
