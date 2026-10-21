import { androidBridge } from "@/shared/android-bridge";

export function GET() {
  return androidBridge("/android/auth/verify");
}
