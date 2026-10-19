export type Webhook = {
  id: string;
  monitorId: string;
  url: string;
  enabled: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type WebhookDelivery = {
  id: string;
  eventId: string;
  type: string;
  status: string;
  attempts: number;
  occurredAt: string;
  createdAt: string;
  completedAt?: string;
  lastHttpStatus?: number;
  lastErrorCode?: string;
};

export type PutWebhook = {
  id?: string;
  version: number;
  url: string;
  enabled: boolean;
  rotateSecret?: boolean;
};

export type DeleteWebhook = {
  id: string;
  version: number;
};
