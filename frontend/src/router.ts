import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: () => import('@/views/OverviewView.vue') },
    { path: '/groups', name: 'groups', component: () => import('@/views/GroupsView.vue') },
    { path: '/channels', name: 'channels', component: () => import('@/views/ChannelsView.vue') },
    { path: '/policy', name: 'policy', component: () => import('@/views/PolicyView.vue') },
    { path: '/events', name: 'events', component: () => import('@/views/EventsView.vue') },
    {
      path: '/connection',
      name: 'connection',
      component: () => import('@/views/ConnectionView.vue')
    },
    {
      path: '/tools/image2',
      name: 'image2',
      component: () => import('@/views/Image2View.vue')
    },
    { path: '/account', name: 'account', component: () => import('@/views/AccountView.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' }
  ]
})

export default router
