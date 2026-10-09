import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import Dashboard from './views/Dashboard.vue'
import Presets from './views/Presets.vue'
import History from './views/History.vue'
import Models from './views/Models.vue'
import Bench from './views/Bench.vue'
import LlamaCpp from './views/LlamaCpp.vue'
import Aliases from './views/Aliases.vue'
import Usage from './views/Usage.vue'
import Alerts from './views/Alerts.vue'
import Playground from './views/Playground.vue'
import Fleet from './views/Fleet.vue'
import Health from './views/Health.vue'
import './style.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: Dashboard },
    { path: '/presets', component: Presets },
    { path: '/models', component: Models },
    { path: '/discover', redirect: (to) => ({ path: '/models', query: { ...to.query, tab: 'search' } }) },
    { path: '/bench', component: Bench },
    { path: '/llamacpp', component: LlamaCpp },
    { path: '/aliases', component: Aliases },
    { path: '/usage', component: Usage },
    { path: '/alerts', component: Alerts },
    { path: '/playground', component: Playground },
    { path: '/fleet', component: Fleet },
    { path: '/history', component: History },
    { path: '/health', component: Health },
  ],
})

createApp(App).use(router).mount('#app')
