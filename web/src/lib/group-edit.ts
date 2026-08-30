import type { TrackedLocalMutationReceipt } from "./mutation-progress";
import type { GroupEntryPatch } from "./state/app.svelte";

export interface GroupEditSaveResult {
  receipt: TrackedLocalMutationReceipt;
  patch: GroupEntryPatch;
}
