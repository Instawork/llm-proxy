import { useMemo } from "react";

import { useBanBYOKey, useBYOBans, useMe, useUnbanBYOKey } from "./queries";
import { permissions } from "../lib/permissions";
import type { BYOBanRecord, Provider } from "../types";

export interface ByoBanActions {
  canManage: boolean;
  pending: boolean;
  findBan: (provider: string, hash: string) => BYOBanRecord | undefined;
  ban: (provider: Provider, maskedId: string) => void;
  unban: (provider: string, hash: string) => void;
}

export function useByoBanActions(): ByoBanActions {
  const { data: me } = useMe();
  const { data: bans = [] } = useBYOBans();
  const banKey = useBanBYOKey();
  const unbanKey = useUnbanBYOKey();

  const banByKey = useMemo(() => {
    const map = new Map<string, BYOBanRecord>();
    for (const ban of bans) {
      map.set(`${ban.provider}:${ban.hash}`, ban);
    }
    return map;
  }, [bans]);

  const canManage = permissions.canManageByo(me?.role);
  const pending = banKey.isPending || unbanKey.isPending;

  return {
    canManage,
    pending,
    findBan: (provider, hash) => banByKey.get(`${provider}:${hash}`),
    ban: (provider, maskedId) => {
      banKey.mutate({ provider, masked_id: maskedId });
    },
    unban: (provider, hash) => {
      unbanKey.mutate({ provider: provider as Provider, hash });
    },
  };
}
