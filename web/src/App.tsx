import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { fetchHealth, fetchMe, getToken, setToken } from "./api/client";
import type { WhoAmI } from "./api/client";
import {
  IconBook,
  IconChat,
  IconLayers,
  IconModels,
  IconNodes,
  IconSettings,
  IconTasks,
} from "./components/icons";
import { ChatPage } from "./pages/ChatPage";
import { DownloadPage } from "./pages/DownloadPage";
import { MinePage } from "./pages/MinePage";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { JobsPage } from "./pages/JobsPage";
import { KnowledgePage } from "./pages/KnowledgePage";
import { LoginPage } from "./pages/LoginPage";
import { ModelsPage } from "./pages/ModelsPage";
import { NodesPage } from "./pages/NodesPage";
import { SettingsPage } from "./pages/SettingsPage";
import { TopologyPage } from "./pages/TopologyPage";

type Route = "chat" | "knowledge" | "mine" | "download" | "nodes" | "models" | "topology" | "jobs" | "settings";

interface NavItem {
  id: Route;
  label: string;
  icon: ReactNode;
  adminOnly?: boolean;
}

const navItems: NavItem[] = [
  { id: "chat", label: "Чат", icon: <IconChat /> },
  { id: "mine", label: "Мой узел", icon: <IconNodes /> },
  { id: "download", label: "Программа", icon: <IconModels /> },
  { id: "knowledge", label: "Знания", icon: <IconBook /> },
  { id: "nodes", label: "Участники", icon: <IconNodes />, adminOnly: true },
  { id: "models", label: "Модели", icon: <IconModels /> },
  { id: "topology", label: "Топология", icon: <IconLayers />, adminOnly: true },
  { id: "jobs", label: "Задания", icon: <IconTasks />, adminOnly: true },
  { id: "settings", label: "Настройки", icon: <IconSettings /> },
];

/** Состояние входа: проверяем сохранённую сессию до первой отрисовки страниц. */
type AuthState = "checking" | "signed-in" | "signed-out";

function readRoute(): Route {
  const hash = window.location.hash.replace("#", "");
  const known = navItems.some((item) => item.id === hash);
  return known ? (hash as Route) : "chat";
}

export default function App() {
  const [auth, setAuth] = useState<AuthState>(() => (getToken() ? "checking" : "signed-out"));
  const [me, setMe] = useState<WhoAmI | null>(null);
  const [route, setRoute] = useState<Route>(readRoute);
  const [health, setHealth] = useState("проверка…");

  const signOut = useCallback(() => {
    setToken("");
    setMe(null);
    setAuth("signed-out");
  }, []);

  // Сессия проверяется запросом /v1/auth/me: истёкшую или отозванную панель
  // нужно показать входом, а не пустыми таблицами с ошибками.
  useEffect(() => {
    if (auth !== "checking") return;
    let cancelled = false;
    fetchMe()
      .then((who) => {
        if (cancelled) return;
        setMe(who);
        setAuth("signed-in");
      })
      .catch(() => {
        if (cancelled) return;
        signOut();
      });
    return () => {
      cancelled = true;
    };
  }, [auth, signOut]);

  useEffect(() => {
    const onHashChange = () => setRoute(readRoute());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  useEffect(() => {
    const onUnauthorized = () => signOut();
    window.addEventListener("cn-unauthorized", onUnauthorized);
    return () => window.removeEventListener("cn-unauthorized", onUnauthorized);
  }, [signOut]);

  useEffect(() => {
    let cancelled = false;
    fetchHealth()
      .then((data) => {
        if (!cancelled) setHealth(`${data.status} · ${data.version}`);
      })
      .catch(() => {
        if (!cancelled) setHealth("недоступен");
      });
    return () => {
      cancelled = true;
    };
  }, [auth]);

  // Раздел, который участнику недоступен, не открываем даже по прямой ссылке.
  useEffect(() => {
    if (auth !== "signed-in" || !me) return;
    const item = navItems.find((entry) => entry.id === route);
    if (item?.adminOnly && me.role !== "admin") {
      window.location.hash = "chat";
      setRoute("chat");
    }
  }, [auth, me, route]);

  if (auth === "checking") {
    return (
      <div className="login-wrap">
        <div className="login-card">
          <div className="brand">
            <span className="brand-mark" aria-hidden="true" />
            <span className="brand-name">Compute Network</span>
          </div>
          <p className="muted">Проверка входа…</p>
        </div>
      </div>
    );
  }

  if (auth === "signed-out") {
    return (
      <LoginPage
        onSignedIn={() => {
          setAuth("checking");
        }}
      />
    );
  }

  const visible = navItems.filter((item) => !item.adminOnly || me?.role === "admin");

  const go = (id: Route) => {
    window.location.hash = id;
    setRoute(id);
  };

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true" />
          <span className="brand-name">Compute Network</span>
        </div>

        <nav className="nav" aria-label="Разделы">
          <span className="nav-label">Панель</span>
          {visible.map((item) => (
            <button
              key={item.id}
              className="nav-item"
              aria-current={route === item.id ? "page" : undefined}
              onClick={() => go(item.id)}
            >
              {item.icon}
              <span className="nav-item-text">{item.label}</span>
            </button>
          ))}
        </nav>

        <div className="sidebar-foot">
          <span>{me?.email ?? "Control Plane"}</span>
          <span className="num">{health}</span>
        </div>
      </aside>

      <main className="content">
        <div className="content-inner">
          {/* Граница на каждый раздел: поломка одного не гасит остальные. */}
          <ErrorBoundary resetKey={route}>
            {route === "chat" && <ChatPage />}
            {route === "mine" && <MinePage />}
            {route === "download" && <DownloadPage />}
            {route === "knowledge" && <KnowledgePage />}
            {route === "nodes" && <NodesPage />}
            {route === "models" && <ModelsPage admin={me?.role === "admin"} />}
            {route === "topology" && <TopologyPage />}
            {route === "jobs" && <JobsPage />}
            {route === "settings" && <SettingsPage onSignedOut={signOut} />}
          </ErrorBoundary>
        </div>
      </main>
    </div>
  );
}
