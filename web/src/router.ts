import { createRouter, createWebHashHistory } from 'vue-router'
import DashboardView from './views/DashboardView.vue'
import NodesView from './views/NodesView.vue'
import ContainersView from './views/ContainersView.vue'
import CreateContainerView from './views/CreateContainerView.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: DashboardView },
    { path: '/nodes', name: 'nodes', component: NodesView },
    { path: '/containers', name: 'containers', component: ContainersView },
    { path: '/containers/new', name: 'containers-new', component: CreateContainerView },
  ],
})

export default router
