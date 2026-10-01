import { createContext, useContext, type ReactNode } from 'react';
import type { ConsoleApi } from '../api/console';

const ConsoleContext = createContext<ConsoleApi | undefined>(undefined);

export function ConsoleProvider({ api, children }: { api: ConsoleApi; children: ReactNode }) {
  return <ConsoleContext.Provider value={api}>{children}</ConsoleContext.Provider>;
}

export function useConsole(): ConsoleApi {
  const api = useContext(ConsoleContext);
  if (!api) {
    throw new Error('useConsole must be used within a ConsoleProvider');
  }
  return api;
}