import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { ErrorBoundary } from './app/ErrorBoundary'
import { logger } from './app/logging'

// React only catches render errors inside a boundary; everything else (event
// handlers, promises, third-party code) surfaces here. Without these two there
// is no trace of a failure beyond a console line nobody reads.
window.addEventListener('error', (event) => {
  logger.error('uncaught error', event.error ?? event.message)
})

window.addEventListener('unhandledrejection', (event) => {
  logger.error('unhandled promise rejection', event.reason)
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
