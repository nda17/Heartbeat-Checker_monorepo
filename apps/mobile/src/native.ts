import { NativeModules } from "react-native";
import type { AuthResult, OAuthPending, Registration } from "./types";

type Bridge = {
  API_ORIGIN: string;
  CHALLENGE_ORIGIN: string;
  DEV_CLIENT: boolean;
  restore(): Promise<AuthResult | null>;
  authEpoch(): Promise<number>;
  saveSession(value: AuthResult, expectedEpoch: number): Promise<void>;
  logout(): Promise<void>;
  beginOAuth(): Promise<{ state: string; codeChallenge: string }>;
  saveOAuth(value: OAuthPending): Promise<void>;
  takeOAuth(flowId: string, state: string): Promise<OAuthPending>;
  clearOAuth(flowId: string, state: string): Promise<void>;
  saveOAuthRegistration(value: {
    registrationToken: string;
    provider: string;
    email: string;
    name: string;
    expiresAt: string;
  }): Promise<void>;
  restoreOAuthRegistration(): Promise<{
    registrationToken: string;
    provider: string;
    email: string;
    name: string;
    expiresAt: string;
  } | null>;
  clearOAuthRegistration(): Promise<void>;
  saveRegistration(value: Registration): Promise<void>;
  restoreRegistration(): Promise<Registration | null>;
  clearRegistration(): Promise<void>;
  openCustomTab(url: string): Promise<void>;
  takeInitialLink(): Promise<string | null>;
  randomUUID(): Promise<string>;
  randomToken(): Promise<string>;
  savePaymentIntent(value: PaymentIntent): Promise<void>;
  restorePaymentIntent(): Promise<PaymentIntent | null>;
  clearPaymentIntent(): Promise<void>;
  sdkCapabilities(): Promise<{
    configured: boolean;
    shopId: string;
    environment: "test" | "live";
  }>;
  sdkTokenize(input: {
    accountId: string;
    sessionId: string;
    authEpoch: number;
    orderId: string;
    shopId: string;
    environment: "test" | "live";
    amountMinor: number;
    currency: "RUB";
    title: string;
    subtitle: string;
    allowedPaymentMethods: ["BANK_CARD"];
    savePaymentMethod: "off";
    draftExpiresAt: string;
  }): Promise<{
    status: "tokenized" | "canceled" | "error" | "unknown";
    paymentToken?: string;
    paymentMethodType?: "BANK_CARD";
  }>;
  sdkConfirm(input: {
    orderId: string;
    accountId: string;
    sessionId: string;
    authEpoch: number;
    shopId: string;
    environment: "test" | "live";
    confirmationUrl: string;
    paymentMethodType: "BANK_CARD";
  }): Promise<{ status: "completed" | "canceled" | "error" | "unknown" }>;
  pushStatus(): Promise<{
    enabled: boolean;
    optedOut: boolean;
    provider: string;
    status: string;
  }>;
  pushToggle(enabled: boolean, allowAccountChange?: boolean): Promise<void>;
};

export type PaymentIntent = {
  accountId: string;
  sessionId: string;
  idempotencyKey: string;
  checkoutKind: "android_sdk" | "redirect";
  purchaseKind: "add" | "renew";
  packageId: string;
  monitorCount: number;
  priceVersion: number;
  orderId?: string;
  createdAt: string;
};

export const native = NativeModules.HeartbeatBridge as Bridge;
