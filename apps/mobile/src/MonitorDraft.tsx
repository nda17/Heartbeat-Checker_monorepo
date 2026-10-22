import React, {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import { errorText, session, sessionEpoch } from "./api";
import { native } from "./native";
import type { AuthResult } from "./types";

export type MonitorForm = {
  checkType: "http" | "tcp" | "icmp";
  name: string;
  url: string;
  host: string;
  port: string;
  method: "GET" | "HEAD";
  expectedStatus: string;
  bodyContains: string;
  intervalMinutes: string;
  failureThreshold: string;
  timeZone: string;
  enabled: boolean;
};
export type MonitorDraft = {
  schemaVersion: 1;
  form: MonitorForm;
  editId: string | null;
  editVersion?: number;
  savedAt?: number;
};
function isDraft(value: unknown): value is MonitorDraft {
  if (!value || typeof value !== "object") return false;
  const item = value as MonitorDraft;
  const form = item.form;
  return (
    item.schemaVersion === 1 &&
    typeof item.savedAt === "number" &&
    item.savedAt <= Date.now() + 60000 &&
    Date.now() - item.savedAt <= 7 * 86400000 &&
    (item.editId === null || typeof item.editId === "string") &&
    (!item.editId ||
      (/^[0-9a-f-]{36}$/i.test(item.editId) &&
        Number.isInteger(item.editVersion) &&
        item.editVersion! > 0)) &&
    !!form &&
    ["http", "tcp", "icmp"].includes(form.checkType) &&
    ["GET", "HEAD"].includes(form.method) &&
    typeof form.enabled === "boolean" &&
    [
      form.name,
      form.url,
      form.host,
      form.port,
      form.expectedStatus,
      form.bodyContains,
      form.intervalMinutes,
      form.failureThreshold,
      form.timeZone,
    ].every((value) => typeof value === "string" && value.length <= 4096)
  );
}
const Context = createContext<{
  draft: MonitorDraft | null;
  ready: boolean;
  error: string;
  update: (value: MonitorDraft | null) => void;
  flush: () => Promise<void>;
}>({
  draft: null,
  ready: false,
  error: "",
  update: () => {},
  flush: async () => {},
});
export function MonitorDraftProvider({
  auth,
  children,
}: {
  auth: AuthResult;
  children: React.ReactNode;
}) {
  const [draft, setDraft] = useState<MonitorDraft | null>(null);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const latestDraft = useRef<MonitorDraft | null>(null);
  latestDraft.current = draft;
  const mounted = useRef(true);
  const writes = useRef(Promise.resolve());
  const writeFailure = useRef<unknown>(null);
  const ids = useRef({
    accountId: auth.account.id,
    sessionId: auth.session.id,
    epoch: sessionEpoch(),
  });
  useEffect(() => {
    mounted.current = true;
    const captured = ids.current;
    void native
      .restoreMonitorDraft(captured.accountId, captured.sessionId)
      .then((value) => {
        const active = session();
        if (
          mounted.current &&
          active?.account.id === captured.accountId &&
          active?.session.id === captured.sessionId
        ) {
          const restored = isDraft(value) ? value : null;
          latestDraft.current = restored;
          setDraft(restored);
        }
      })
      .catch((cause) => {
        if (mounted.current) setError(errorText(cause));
      })
      .finally(() => {
        if (mounted.current) setReady(true);
      });
    return () => {
      mounted.current = false;
    };
  }, []);
  const update = (value: MonitorDraft | null) => {
    const saved = value ? { ...value, savedAt: Date.now() } : null;
    latestDraft.current = saved;
    setDraft(saved);
    setError("");
    const captured = ids.current;
    // Serial writes keep a late keystroke from resurrecting a cleared draft.
    writes.current = writes.current
      .catch(() => {})
      .then(async () => {
        if (sessionEpoch() !== captured.epoch) return;
        writeFailure.current = null;
        if (saved)
          await native.saveMonitorDraft(
            saved,
            captured.accountId,
            captured.sessionId,
          );
        else
          await native.clearMonitorDraft(
            captured.accountId,
            captured.sessionId,
          );
      })
      .catch((cause) => {
        writeFailure.current = cause;
        if (mounted.current)
          setError(`Черновик не сохранён на телефоне. ${errorText(cause)}`);
      });
  };
  useEffect(() => {
    const active = session();
    if (
      !ready ||
      !active ||
      active.account.id !== auth.account.id ||
      active.session.id !== auth.session.id ||
      ids.current.epoch === sessionEpoch()
    )
      return;
    // OAuth may rotate the token without changing the session ID. Preserve the
    // latest in-memory form and append its write after all older queued writes.
    ids.current = {
      accountId: active.account.id,
      sessionId: active.session.id,
      epoch: sessionEpoch(),
    };
    update(latestDraft.current);
  }, [auth.session.token, ready]);
  return (
    <Context.Provider
      value={{
        draft,
        ready,
        error,
        update,
        flush: async () => {
          await writes.current;
          if (writeFailure.current) throw writeFailure.current;
        },
      }}
    >
      {children}
    </Context.Provider>
  );
}
export const useMonitorDraft = () => useContext(Context);
