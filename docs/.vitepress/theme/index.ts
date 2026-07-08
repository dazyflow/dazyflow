// Dazyflow docs theme = VitePress default theme + custom.css, which ports the
// web app's design tokens (web/src/theme.css) onto VitePress's variables so the
// docs read as part of the product: same violet brand, fonts, surfaces, and a
// nav bar that echoes the app top bar.
import DefaultTheme from 'vitepress/theme'
import './custom.css'

export default DefaultTheme
