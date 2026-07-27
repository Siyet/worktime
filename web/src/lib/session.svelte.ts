// Session guard: detects who is signed in and prevents mixing two accounts'
// data inside one browser profile.
import { getMeta, setMeta, wipeLocalData } from "./db";
import type { User } from "./types";

export const session = $state({
  user: null as User | null,
  checked: false,
  googleAvailable: false,
});

export async function initSession(): Promise<void> {
  try {
    const configResponse = await fetch("/auth/config");
    if (configResponse.ok) {
      const authConfig = await configResponse.json();
      session.googleAvailable = Boolean(authConfig.google);
    }
    const meResponse = await fetch("/api/me");
    if (meResponse.ok) {
      const user: User = await meResponse.json();
      const storedUserID = await getMeta<string>("user_id");
      if (storedUserID && storedUserID !== user.id) {
        // Another account signed in on this browser: drop the previous user's data.
        await wipeLocalData();
        window.location.reload();
        return;
      }
      await setMeta("user_id", user.id);
      session.user = user;
    }
  } catch {
    // Offline start: IndexedDB data belongs to the last signed-in user, keep going.
  } finally {
    session.checked = true;
  }
}

export async function logout(): Promise<void> {
  await fetch("/auth/logout", { method: "POST" });
  await wipeLocalData();
  window.location.href = "/";
}
