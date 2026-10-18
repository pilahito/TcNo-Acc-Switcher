import { get } from "svelte/store";
import { Events } from "@wailsio/runtime";
import * as AccountRefreshService from "../../bindings/TcNo-Acc-Switcher/internal/accountrefresh/accountrefreshservice.js";
import { t } from "../stores/i18n";
import { pushToast } from "../stores/toast";
import { requestPlatformAccountsRefresh } from "../stores/platformPage";
import { formatToastWithError } from "./formatWailsError";

/** Mirrors accountrefresh.ProgressEvent in Go. */
export const REFRESH_ALL_PROGRESS_EVENT = "accounts-refresh-all-progress";

type RefreshProgress = { platform: string; done: number; total: number; error?: string };

let running = false;

/**
 * Refreshes profile images, names and game stats for every saved account on
 * every platform shown on the home screen - the per-platform "Refresh accounts"
 * (F5), run across all of them in one action.
 */
export async function refreshAllPlatformsAccounts(): Promise<void> {
  const tr = get(t);
  if (running) {
    pushToast({ type: "info", message: tr("Toast_RefreshAllAccountsBusy"), duration: 4000 });
    return;
  }
  running = true;
  const off = Events.On(REFRESH_ALL_PROGRESS_EVENT, (ev: { data: unknown }) => {
    const p = ev.data as RefreshProgress | undefined;
    if (!p || !p.total || p.total < 2) return;
    pushToast({
      type: "info",
      message: tr("Toast_RefreshAllAccountsProgress", {
        done: String(p.done),
        total: String(p.total),
        platform: p.platform,
      }),
      duration: 2500,
    });
  });
  try {
    const sum = await AccountRefreshService.RefreshAllAccounts();
    if (sum.busy) {
      pushToast({ type: "info", message: tr("Toast_RefreshAllAccountsBusy"), duration: 4000 });
      return;
    }
    if (sum.offline) {
      pushToast({ type: "error", message: tr("Toast_RefreshAllAccountsOffline"), duration: 6000 });
      return;
    }
    for (const name of sum.refreshed ?? []) requestPlatformAccountsRefresh(name);
    const failed = sum.failed ?? [];
    if ((sum.refreshed ?? []).length === 0 && failed.length === 0) {
      pushToast({ type: "info", message: tr("Toast_RefreshAllAccountsNothing"), duration: 5000 });
      return;
    }
    if ((sum.refreshed ?? []).length > 0) {
      pushToast({
        type: "success",
        message: tr("Toast_RefreshingAllAccounts", {
          count: String(sum.accountCount ?? 0),
          platforms: String(sum.refreshed.length),
        }),
        duration: 6000,
      });
    }
    if (failed.length > 0) {
      pushToast({
        type: "error",
        message: tr("Toast_RefreshAllAccountsPartial", {
          platforms: failed.map((f) => (f ? `${f.platform} (${f.error})` : "")).filter(Boolean).join(", "),
        }),
        duration: 10000,
      });
    }
  } catch (e) {
    pushToast({ type: "error", message: formatToastWithError(tr("Toast_RefreshAccountsFailed"), e), duration: 8000 });
  } finally {
    off?.();
    running = false;
  }
}
