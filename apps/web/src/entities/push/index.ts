export type PushInstallationStatus =
  | "pending"
  | "ready"
  | "permission_denied"
  | "session_expired"
  | "session_revoked"
  | "invalid_token"
  | "provider_unavailable";

export const pushStatusLabels: Record<PushInstallationStatus, string> = {
  pending: "Ожидает настройки в приложении",
  ready: "Настроен для уведомлений",
  permission_denied: "Уведомления запрещены на телефоне",
  session_expired: "Срок подключения истёк",
  session_revoked: "Подключение отозвано",
  invalid_token: "Нужно обновить подключение в приложении",
  provider_unavailable: "Сервис push временно недоступен",
};

export interface PushInstallation {
  id: string;
  provider: "rustore" | "fcm" | "huawei";
  deviceLabel: string;
  status: PushInstallationStatus;
  bindingGeneration: number;
  registrationVersion: number;
  leaseExpiresAt: string | null;
  lastPairingId: string | null;
  lastSeenAt: string;
}

export interface PushPairing {
  id: string;
  deviceLabel: string;
  status: "pending" | "confirmed";
  expiresAt: string;
}

export interface PushPairingConfirmation {
  pairingId: string;
  completionCode: string;
  clientState: string;
  expiresAt: string;
}

export interface PushMonitorSettings {
  enabled: boolean;
  version: number;
}
