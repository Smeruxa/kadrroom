import type { Routes } from '@angular/router'
import { CatalogPage } from './catalog/catalog-page'

const authPage = () => import('./auth/auth-page').then((module) => module.AuthPage)

export const routes: Routes = [
    { path: '', component: CatalogPage, pathMatch: 'full' },
    { path: 'login', loadComponent: authPage, data: { mode: 'login' } },
    { path: 'register', loadComponent: authPage, data: { mode: 'register' } },
    { path: 'verify', loadComponent: authPage, data: { mode: 'verify' } },
    { path: 'forgot-password', loadComponent: authPage, data: { mode: 'forgot' } },
    { path: 'reset', loadComponent: authPage, data: { mode: 'reset' } },
    { path: 'account', loadComponent: authPage, data: { mode: 'account' } },
    {
        path: 'join',
        loadComponent: () => import('./room/join-page').then((module) => module.JoinPage),
    },
    {
        path: 'rooms/:id',
        loadComponent: () => import('./room/room-page').then((module) => module.RoomPage),
    },
    { path: '**', redirectTo: '' },
]
