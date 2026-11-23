import { BrowserRouter, Route, Routes } from 'react-router';
import SearchPage from './pages/SearchPage';
import { useApiStatus } from './app/useApiStatus';
import './App.css'

function ApiStatusBanner() {
  const { status, detail } = useApiStatus();

  if (status !== 'down') {
    return null;
  }

  return (
    <div className="app-banner" role="alert">
      <span className="app-banner-title">API tidak terjangkau.</span>
      <span>
        Pencarian tidak akan berfungsi sampai layanan tersedia kembali. Halaman
        ini mencoba lagi secara otomatis setiap 30 detik.
      </span>
      {detail && <span className="app-banner-detail">{detail}</span>}
    </div>
  );
}

function App() {
  return (
    <>
      <ApiStatusBanner />
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<SearchPage />} />
          <Route path="/search" element={<SearchPage />} />
          {/*<Route path="/documents/:id" element={<DocumentPage />} />
          <Route path="/about" element={<AboutPage />} />*/}
        </Routes>
      </BrowserRouter>
    </>
  );

}

export default App
