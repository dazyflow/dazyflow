<script setup>
// Wrap VitePress's default Layout and inject the marketing-site chrome
// (../dazyflow-web): the gradient announcement strip above the nav, the
// "Request an invite" CTA in the top bar, and the centered footer — so the docs
// read as the same site. Styling lives in custom.css (.dz-announce/.dz-cta/
// .dz-footer), ported from that site's style.css.
import DefaultTheme from 'vitepress/theme'
const { Layout } = DefaultTheme

const INVITE = 'mailto:hi@dazyflow.app?subject=Dazyflow%20early%20access'
</script>

<template>
  <Layout>
    <template #layout-top>
      <div class="dz-announce">
        Dazyflow is in early access.
        <a :href="INVITE">Request an invite</a>
      </div>
    </template>

    <template #nav-bar-content-after>
      <a class="dz-cta" :href="INVITE">Request an invite</a>
    </template>

    <template #layout-bottom>
      <footer class="dz-footer">
        <p class="dz-footer-line">
          <a href="https://dazyflow.app/pricing">Pricing</a> ·
          <a href="https://dazyflow.app/privacy">Privacy</a> ·
          <a href="https://dazyflow.app/terms">Terms</a> ·
          <a href="mailto:hi@dazyflow.app">Contact</a> ·
          <a href="https://dazyflow.app/">Home</a>
        </p>
        <p class="dz-footer-line dz-footer-line--muted">Copyright © 2026 Dazyflow.</p>
      </footer>
    </template>
  </Layout>
</template>
