import { androidBridge } from "@/shared/android-bridge";

export function GET() {
  return androidBridge("/mobile/challenge");
}
