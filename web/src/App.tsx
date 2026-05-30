import { Route, Routes } from 'react-router-dom';
import { Dashboard } from './pages/Dashboard';
import { TerminalPage } from './pages/Terminal';

export function App() {
  return (
    <Routes>
      <Route path="/" element={<Dashboard />} />
      <Route path="/term" element={<TerminalPage />} />
    </Routes>
  );
}
