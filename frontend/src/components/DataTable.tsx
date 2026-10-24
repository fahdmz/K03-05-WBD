import type { ReactNode } from "react"

export type Column<T> = {
    header: string
    render: (row: T) => ReactNode
    className?: string
}

type Props<T> = {
    columns: Column<T>[]
    rows: T[]
    rowKey: (row: T) => string | number
    empty?: string
}

export default function DataTable<T>({ columns, rows, rowKey, empty = 'Belum ada data' }: Props<T>) {
    return (
        <div className="overflow-x-auto rounded border border-slate-200 bg-white">
            <table className="min-w-full text-sm">
                <thead className="bg-slate-100 text-left text-slate-600">
                    <tr>
                        {columns.map((c) => (
                            <th key={c.header} className={`px-4 py-2 font-medium ${c.className ?? ''}`}>
                                {c.header}
                            </th>
                        ))}
                    </tr>
                </thead>
                <tbody>
                    {rows.length === 0 && (
                        <tr>
                            <td colSpan={columns.length} className="px-4 py-6 text-center text-slate-500">
                                {empty}
                            </td>
                        </tr>
                    )}
                    {rows.map((row) => (
                        <tr key={rowKey(row)} className="border-t border-slate-100">
                            {columns.map((c) => (
                                <td key={c.header} className={`px-4 py-2 ${c.className ?? ''}`}>
                                    {c.render(row)}
                                </td>
                            ))}
                        </tr>
                    ))}
                </tbody>
            </table>
        </div>
    )
}
