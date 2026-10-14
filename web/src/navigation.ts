import { computed, inject, type InjectionKey, type Ref } from 'vue'

const viewScreens = ['guest', 'guest-settings', 'setup', 'login', 'settings', 'nodes', 'discovery', 'agent-enrollment', 'alerts', 'operations', 'updates', 'feeds', 'host-management'] as const
export type ViewScreen = typeof viewScreens[number]
export interface ViewRoute { screen: ViewScreen; nodeId?: string; section?: string; tab?: string; filePath?: string }
export interface ViewNavigation {
  route: Readonly<Ref<ViewRoute>>
  go: (route: ViewRoute, replace?: boolean) => void
}
export interface NodeNavigation {
  owner: symbol
  nodeId: string
  title: string
  activeSection: string
  sections: readonly { id: string; label: string; icon: string }[]
}
export const nodeNavigationKey: InjectionKey<Ref<NodeNavigation | null>> = Symbol('node-navigation')
export const navigationKey: InjectionKey<ViewNavigation> = Symbol('view-navigation')
const screens = new Set<ViewScreen>(viewScreens)

// This is browser-local view state. Neither URLs nor history.state grant access,
// and no form, credential, task submission or terminal action is serialized.
export function readViewRoute(): ViewRoute {
  const [path, query = ''] = window.location.hash.slice(1).split('?')
  const screen = path?.replace(/^\//, '') as ViewScreen
  const route: ViewRoute = { screen: screens.has(screen) ? screen : 'nodes' }
  const params = new URLSearchParams(query)
  const field = (name: string) => {
    const value = params.get(name)
    return value && value.length <= 200 && !/[\\/\0\r\n]/.test(value) ? value : undefined
  }
  if (route.screen === 'nodes' || route.screen === 'host-management') route.nodeId = field('node')
  if (route.screen === 'nodes' && route.nodeId) route.section = field('section')
  if(route.screen==='nodes'&&route.nodeId&&route.section==='files'){const value=params.get('path');if(value&&value.startsWith('/')&&value.length<=4096&&!/[\\\0\r\n]/.test(value)&&!value.split('/').includes('..'))route.filePath=value}
  if (route.screen === 'alerts' || route.screen === 'host-management') route.tab = field('tab')
  return route
}

export function viewHash(route: ViewRoute): string {
  const query = new URLSearchParams()
  if(route.screen==='nodes'&&route.nodeId&&route.section==='files'&&route.filePath&&route.filePath!=='/')query.set('path',route.filePath)
  if (route.nodeId && (route.screen === 'nodes' || route.screen === 'host-management')) query.set('node', route.nodeId)
  if (route.screen === 'nodes' && route.nodeId && route.section && route.section !== 'overview') query.set('section', route.section)
  if ((route.screen === 'alerts' || route.screen === 'host-management') && route.tab) query.set('tab', route.tab)
  return '#/' + route.screen + (query.size ? '?' + query.toString() : '')
}

export function writeViewRoute(route: ViewRoute, replace = false): void {
  const hash = viewHash(route)
  if (window.location.hash === hash) return
  const url = window.location.pathname + window.location.search + hash
  window.history[replace ? 'replaceState' : 'pushState']({ nodedance: true }, '', url)
}

export function useViewNavigation(): ViewNavigation {
  const navigation = inject(navigationKey)
  if (!navigation) throw new Error('Missing view navigation')
  return navigation
}

export function useViewTab<T extends string>(field: 'section' | 'tab', fallback: T, choices: () => readonly T[]) {
  const navigation = useViewNavigation()
  return computed<T>({
    get: () => {
      const value = navigation.route.value[field] as T | undefined
      return value && choices().includes(value) ? value : fallback
    },
    set: value => navigation.go({ ...navigation.route.value, [field]: value === fallback ? undefined : value }),
  })
}
