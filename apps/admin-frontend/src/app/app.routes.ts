import type { Routes } from '@angular/router'
import { AdminApi } from './admin/admin-api'

export const routes: Routes = [
    {
        path: '',
        pathMatch: 'full',
        providers: [AdminApi],
        loadComponent: () => import('./admin/admin-page').then((module) => module.AdminPage),
        title: 'Управление каталогом — KadrRoom',
    },
    {
        path: 'login',
        loadComponent: () => import('./auth/login-page').then((module) => module.LoginPage),
        title: 'Вход для операторов — KadrRoom',
    },
    { path: '**', redirectTo: '' },
]
