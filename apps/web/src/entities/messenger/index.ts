export type Provider = "telegram" | "max" | "email";
export type DestinationKind = "private" | "group" | "topic" | "email";
export type Candidate = {
  label: string;
  chatId: string;
  userId: string;
  threadId: string | null;
};
export type MessengerLink = {
  id: string;
  provider: Provider;
  kind: DestinationKind;
  status: "pending" | "observed" | "candidate" | "confirmed" | "expired";
  expiresAt: string;
  candidate: Candidate | null;
  destinationId: string | null;
};
export type Destination = Candidate & {
  id: string;
  provider: Provider;
  kind: DestinationKind;
  email?: string;
  ready: boolean;
  version: number;
};
export type MessengerRoute = {
  provider: Provider;
  destinationId: string;
  enabled: boolean;
  reminderMinutes: number;
  version: number;
};
export const providerName = {
  telegram: "Telegram",
  max: "MAX",
  email: "Email",
} as const;
export const destinationKindName = {
  private: "личный чат",
  group: "группа",
  topic: "топик группы",
  email: "подтверждённый email",
} as const;
