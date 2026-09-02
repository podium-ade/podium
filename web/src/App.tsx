import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { Header } from "./components/Header";
import { ToastHost } from "./components/Toast";
import { TokenGate } from "./components/TokenGate";
import { NodesPage } from "./pages/NodesPage";
import { TaskDetailPage } from "./pages/TaskDetailPage";
import { TasksPage } from "./pages/TasksPage";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastHost>
        <TokenGate>
          <BrowserRouter>
            <div className="flex h-full flex-col">
              <Header />
              <main className="mx-auto w-full max-w-7xl flex-1 overflow-y-auto p-4">
                <Routes>
                  <Route path="/" element={<TasksPage />} />
                  <Route path="/tasks/:id" element={<TaskDetailPage />} />
                  <Route path="/nodes" element={<NodesPage />} />
                  <Route path="*" element={<Navigate to="/" replace />} />
                </Routes>
              </main>
            </div>
          </BrowserRouter>
        </TokenGate>
      </ToastHost>
    </QueryClientProvider>
  );
}
