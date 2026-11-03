import { createContext, useContext, type ReactNode } from "react";
import type { MeResponse } from "./api/types";

const SessionContext = createContext<MeResponse | undefined>(undefined);

export function SessionProvider({ value, children }: { value: MeResponse; children: ReactNode }) {
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): MeResponse {
  const value = useContext(SessionContext);
  if (!value) throw new Error("useSession must be used inside a protected route");
  return value;
}

export function hasCapability(me: MeResponse, capability: string): boolean {
  return me.scope.capabilities.includes(capability);
}

export function usePermissions() {
  const me = useSession();
  const canRead = hasCapability(me, "knowledge_base:read");
  const canWrite = hasCapability(me, "knowledge_base:write");
  const canManage = hasCapability(me, "knowledge_base:manage_members");
  const canDelete = hasCapability(me, "knowledge_base:delete");
  return {
    me,
    canRead,
    canWrite,
    canManage,
    canDelete,
    canChat: canRead,
    canOperate: canManage || canDelete,
  };
}
