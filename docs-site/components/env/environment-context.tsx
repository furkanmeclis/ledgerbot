'use client';

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import type { Environment } from '@/lib/environments';

const STORAGE_KEY = 'ledgerbot-docs-env';

export const PLACEHOLDERS: Record<Environment['id'], { apiUrl: string; hostUrl: string }> = {
  production: { apiUrl: 'https://ledgerbot.example.com', hostUrl: 'https://app.easytrade.example/ledgerbot' },
  staging: { apiUrl: 'https://stage.ledgerbot.example.com', hostUrl: 'https://stage.easytrade.example/ledgerbot' },
};

interface EnvironmentState {
  environments: Environment[];
  selected: Environment;
  select: (id: Environment['id']) => void;
  loaded: boolean;
}

const fallback: Environment[] = [
  { id: 'production', label: 'Production', apiUrl: '', hostUrl: '' },
  { id: 'staging', label: 'Staging', apiUrl: '', hostUrl: '' },
];

const Ctx = createContext<EnvironmentState | null>(null);

let request: Promise<Environment[]> | null = null;

function loadEnvironments(): Promise<Environment[]> {
  request ??= fetch('/api/environments', { credentials: 'same-origin' })
    .then((r) => (r.ok ? (r.json() as Promise<Environment[]>) : fallback))
    .catch(() => fallback);
  return request;
}

export function EnvironmentProvider({ children }: { children: ReactNode }) {
  const [environments, setEnvironments] = useState<Environment[]>(fallback);
  const [selectedId, setSelectedId] = useState<Environment['id']>('production');
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    try {
      const saved = localStorage.getItem(STORAGE_KEY);
      if (saved === 'production' || saved === 'staging') setSelectedId(saved);
    } catch {
      // storage unavailable: keep the default
    }
    let live = true;
    void loadEnvironments().then((envs) => {
      if (!live) return;
      setEnvironments(envs);
      setLoaded(true);
    });
    return () => {
      live = false;
    };
  }, []);

  const select = useCallback((id: Environment['id']) => {
    setSelectedId(id);
    try {
      localStorage.setItem(STORAGE_KEY, id);
    } catch {
      // ignore
    }
  }, []);

  const value = useMemo<EnvironmentState>(
    () => ({ environments, selected: environments.find((e) => e.id === selectedId) ?? environments[0], select, loaded }),
    [environments, selectedId, select, loaded],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useEnvironments(): EnvironmentState {
  const v = useContext(Ctx);
  if (!v) throw new Error('useEnvironments outside EnvironmentProvider');
  return v;
}

/** The configured URL, or a clearly fake placeholder when the deployment did not set one. */
export function resolveUrl(env: Environment, kind: 'api' | 'host'): { url: string; configured: boolean } {
  const url = kind === 'api' ? env.apiUrl : env.hostUrl;
  if (url) return { url, configured: true };
  return { url: PLACEHOLDERS[env.id][kind === 'api' ? 'apiUrl' : 'hostUrl'], configured: false };
}
