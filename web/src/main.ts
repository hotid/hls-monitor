import { createApp } from 'vue'
import { createPinia } from 'pinia'
// Only the scripts the interface is written in: Latin and Cyrillic.
import '@fontsource/manrope/latin-400.css'
import '@fontsource/manrope/latin-500.css'
import '@fontsource/manrope/latin-600.css'
import '@fontsource/manrope/latin-700.css'
import '@fontsource/manrope/cyrillic-400.css'
import '@fontsource/manrope/cyrillic-500.css'
import '@fontsource/manrope/cyrillic-600.css'
import '@fontsource/manrope/cyrillic-700.css'
import '@fontsource/jetbrains-mono/latin-400.css'
import '@fontsource/jetbrains-mono/cyrillic-400.css'
import 'uplot/dist/uPlot.min.css'
import './styles.css'
import App from './App.vue'
import { i18n } from './i18n'
import { router } from './router'
import { setUnauthorizedHandler } from './api'
import { useSession } from './stores/session'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(i18n)

// A session that expired while the page was open sends the user to sign in.
setUnauthorizedHandler(() => {
  const session = useSession()
  if (session.user) {
    session.user = null
    void router.push({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
  }
})

app.mount('#app')
