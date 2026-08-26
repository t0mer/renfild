import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { Activity, History, ListChecks, Mic, Moon, Settings as SettingsIcon, Sun } from "lucide-react";

import { Button } from "@/components/ui/primitives";
import { useTheme } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import Dashboard from "@/pages/Dashboard";
import Speakers from "@/pages/Speakers";
import Intents from "@/pages/Intents";
import HistoryPage from "@/pages/History";
import SettingsPage from "@/pages/Settings";

const NAV = [
  { to: "/dashboard", label: "Dashboard", icon: Activity },
  { to: "/speakers", label: "Speakers", icon: Mic },
  { to: "/intents", label: "Intents", icon: ListChecks },
  { to: "/history", label: "History", icon: History },
  { to: "/settings", label: "Settings", icon: SettingsIcon },
];

export default function App() {
  const { dark, toggle } = useTheme();

  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-10 border-b border-[var(--color-line)] bg-[var(--color-surface)]/95 backdrop-blur">
        <div className="mx-auto flex max-w-6xl items-center gap-4 px-4 py-3">
          <span className="text-lg font-semibold tracking-tight">
            Renfild<span className="text-[var(--color-accent)]">.</span>
          </span>
          <nav className="flex flex-1 flex-wrap gap-1">
            {NAV.map(({ to, label, icon: Icon }) => (
              <NavLink
                key={to}
                to={to}
                className={({ isActive }) =>
                  cn(
                    "inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition",
                    isActive
                      ? "bg-[var(--color-accent)]/12 text-[var(--color-accent)]"
                      : "text-[var(--color-ink-muted)] hover:bg-[var(--color-surface-muted)]",
                  )
                }
              >
                <Icon size={16} />
                {label}
              </NavLink>
            ))}
          </nav>
          <Button variant="ghost" size="sm" onClick={toggle} aria-label="Toggle dark mode">
            {dark ? <Sun size={16} /> : <Moon size={16} />}
          </Button>
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-4 py-6">
        <Routes>
          <Route path="/" element={<Navigate to="/dashboard" replace />} />
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/speakers" element={<Speakers />} />
          <Route path="/intents" element={<Intents />} />
          <Route path="/history" element={<HistoryPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="*" element={<Navigate to="/dashboard" replace />} />
        </Routes>
      </main>
    </div>
  );
}
