import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import App from './App';
import { AuthProvider } from './auth/AuthContext';
import { ToastProvider } from './components/ToastProvider';
import './styles.css';

const router = createBrowserRouter([
  {
    path: '*',
    element: <ToastProvider><AuthProvider><App/></AuthProvider></ToastProvider>,
  },
]);

createRoot(document.getElementById('root')!).render(<StrictMode><RouterProvider router={router}/></StrictMode>);
