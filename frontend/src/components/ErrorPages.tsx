import { Link } from 'react-router-dom'

function ErrorPage({ code, message }: { code: string, message: string }) {
    return (
        <div className='py-20 text-center'>
            <p className='text-5xl font-bold text-slate-300'>{code}</p>
            <p className='mt-3 text-slate-600'>{message}</p>
            <Link to="/" className='mt-6 inline-block text-sm text-blue-600 hover:underline'>
                Kembali ke beranda
            </Link>
        </div>
    )
}

export function ForbiddenPage() {
    return <ErrorPage code="403" message="You don't have access to this page" />
}

export function NotFoundPage() {
    return <ErrorPage code="404" message="Page Not Found" />
}