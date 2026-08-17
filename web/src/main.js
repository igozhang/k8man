import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './styles.css'

const app = createApp(App)
app.config.errorHandler = (err, _instance, info) => {
  // 避免未捕获渲染/生命周期错误导致白屏；控制台保留线索
  console.error('[k8man]', info, err)
}
app.use(router).mount('#app')
