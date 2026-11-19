export type Account = {
  id: string;
  email: string;
  name: string;
  role?: string;
  emailVerified?: boolean;
};
export type Session = {
  id: string;
  token: string;
  expiresAt: string;
  revokeToken?: string;
};
export type AuthResult = { account: Account; session: Session };
export type Challenge = {
  id: string;
  email: string;
  expiresAt: string;
  resendAvailableAt: string;
  serverNow: string;
  status?: string;
};
export type Registration = { challenge: Challenge; registrationToken: string };
export type Provider = "yandex" | "vk";
export type OAuthPending = {
  flowId: string;
  provider: Provider;
  state: string;
  codeVerifier: string;
  expiresAt: string;
  mode: "login" | "link";
};
export type Monitor = {
  id: string;
  checkType: "http" | "tcp" | "icmp";
  name: string;
  url: string;
  host: string;
  port: number;
  method: "GET" | "HEAD";
  expectedStatus: number;
  bodyContains: string;
  timeZone: string;
  intervalMinutes: number;
  failureThreshold: number;
  enabled: boolean;
  version: number;
  state: string;
  lastCheckedAt: string | null;
  nextCheckAt: string | null;
  provider: { names: string[]; status: string; checkedAt: string | null };
  notificationChannels?: string[];
};
export type Result = {
  id: string;
  outcome: string;
  errorCode?: string;
  checkedAt: string;
  httpStatus?: number;
  latencyMs: number;
  roundTripMs?: number | null;
};
export type Incident = {
  id: string;
  number: number;
  openedAt: string;
  confirmedAt: string;
  closedAt?: string | null;
  closeReason?: string | null;
};
export type Balance = {
  availableMinor: number;
  positiveBelowCent: boolean;
  currency: "RUB";
  enabledMonitors: number;
  asOf: string;
  fundedUntil: string | null;
  suspended: boolean;
  pricePerMonitorMinor: number;
  priceVersion: number;
};
export type BalanceEntry = {
  id: string;
  kind: "top_up" | "admin_credit" | "usage" | "refund";
  amountMinor: number;
  positiveBelowCent?: boolean;
  test?: boolean;
  createdAt: string;
  periodStart?: string;
  periodEnd?: string;
};
export type BalanceHistory = {
  entries: BalanceEntry[];
  pagination: {
    page: number;
    pageSize: number;
    total: number;
    totalPages: number;
  };
};
export type Order = {
  id: string;
  purchaseKind: string;
  status: string;
  amountMinor: number;
  confirmationUrl?: string;
  fulfillmentStatus: string;
  createdAt: string;
  test?: boolean;
};
export type Billing = {
  canPay: boolean;
  test?: boolean;
  orders: Order[];
};
