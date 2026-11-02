import { secrets } from "bun";
import type { CaptureError, CredentialStore, Result } from "./types";
/** OS-protected native storage only. No shell, argv, environment, or file fallback.
 * Qualified against Bun1.4.3 API and official runtime/secrets documentation.
 */
type NativeStore = {
  get(options: { service: string; name: string }): Promise<string | null>;
  set(options: { service: string; name: string; value: string }): Promise<void>;
};
export class NativeCredentials implements CredentialStore {
  constructor(private readonly store: NativeStore = secrets) {}
  async get(profile: string): Promise<Result<string, CaptureError>> {
    if (!validProfile(profile)) return unavailable();
    try {
      const value = await this.store.get({
        service: "com.beaudry.kb.capture",
        name: profile,
      });
      return value && validBearer(value) ? { ok: true, value } : unavailable();
    } catch {
      return unavailable();
    }
  }
  async set(
    profile: string,
    bearer: string,
  ): Promise<Result<void, CaptureError>> {
    if (!validProfile(profile) || !validBearer(bearer)) return unavailable();
    try {
      await this.store.set({
        service: "com.beaudry.kb.capture",
        name: profile,
        value: bearer,
      });
      return { ok: true, value: undefined };
    } catch {
      return unavailable();
    }
  }
}
const unavailable = (): Result<never, CaptureError> => ({
  ok: false,
  error: { code: "credential_unavailable" },
});
export const validProfile = (profile: string) =>
  /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(profile);
export const validBearer = (value: string) => /^[a-f0-9]{64}$/.test(value);
