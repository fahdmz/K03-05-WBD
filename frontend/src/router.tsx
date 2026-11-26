import { createBrowserRouter } from 'react-router-dom'
import { NotFoundPage } from './components/ErrorPages'
import { RequireAdmin, RequireAuth } from './components/guards'
import Layout from './components/Layout'
import AdminPage from './features/admin/AdminPage'
import LoginPage from './features/auth/LoginPage'
import DatabasesPage from './features/databases/DatabasesPage'
import JobPage from './features/jobs/JobPage'
import ChangeRequestPage from './features/reviews/ChangeRequestPage'
import ReviewsPage from './features/reviews/ReviewsPage'
import { useAuth } from './lib/auth'

function AdminRoute() {
    const { user } = useAuth()
    return <AdminPage currentUserId={user?.id} />
}

export const router = createBrowserRouter([
    { path: '/login', element: <LoginPage /> },
    {
        element: <RequireAuth />,
        children: [
            {
                element: <Layout />,
                children: [
                    { path: '/', element: <DatabasesPage /> },
                    { path: '/reviews', element: <ReviewsPage /> },
                    { path: '/change-requests/:id', element: <ChangeRequestPage /> },
                    { path: '/jobs/:id', element: <JobPage /> },
                    {
                        element: <RequireAdmin />,
                        children: [{ path: '/admin', element: <AdminRoute /> }],
                    },
                    { path: '*', element: <NotFoundPage /> },
                ],
            },
        ],
    },
])