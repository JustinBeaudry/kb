import { secrets } from "bun";
import { validBearer, validProfile } from "../capture/credentials";
import type { CaptureError, CredentialStore, Result } from "../capture/types";

type NativeStore = {
  get(options: { service: string; name: string }): Promise<string | null>;
  set(options: { service: string; name: string; value: string }): Promise<void>;
};
const service = "com.beaudry.kb.remote";
const unavailable = (): Result<never, CaptureError> => ({ ok: false, error: { code: "credential_unavailable" } });

/** Remote credentials never share capture's OS-service namespace. */
export class RemoteCredentials implements CredentialStore {
  constructor(private readonly store: NativeStore = secrets) {}
  async get(account: string): Promise<Result<string, CaptureError>> {
    if (!validProfile(account)) return unavailable();
    try {
      const value = await this.store.get({ service, name: account });
      return value && validBearer(value) ? { ok: true, value } : unavailable();
    } catch { return unavailable(); }
  }
  async set(account: string, value: string): Promise<Result<void, CaptureError>> {
    if (!validProfile(account) || !validBearer(value)) return unavailable();
    try {
      await this.store.set({ service, name: account, value });
      return { ok: true, value: undefined };
    } catch { return unavailable(); }
  }
}
