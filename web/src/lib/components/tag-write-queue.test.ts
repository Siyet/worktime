import { describe, expect, it } from "vitest";
import { TagWriteQueue } from "./tag-write-queue.svelte";

function deferred(): { promise: Promise<void>; reject: (error: Error) => void } {
  let rejectPromise!: (error: Error) => void;
  return {
    promise: new Promise<void>((_resolve, reject) => {
      rejectPromise = reject;
    }),
    reject: rejectPromise,
  };
}

describe("TagWriteQueue", () => {
  it("rolls a rejected queue back to external tags and accepts the next toggle", async () => {
    let externalTags = ["initial"];
    let persistedTags = [...externalTags];
    const firstWrite = deferred();
    let writeCount = 0;
    const queue = new TagWriteQueue(externalTags, () => externalTags, async (nextTags) => {
      writeCount++;
      if (writeCount === 1) return firstWrite.promise;
      persistedTags = [...nextTags];
      externalTags = [...nextTags];
    });

    const drain = queue.change(["development"]);
    void queue.change(["development", "review"]);
    externalTags = ["external"];
    queue.reconcile(externalTags);
    expect(queue.draftTags).toEqual(["development", "review"]);

    firstWrite.reject(new Error("injected IndexedDB failure"));
    await expect(drain).resolves.toBeUndefined();
    expect(writeCount).toBe(1);
    expect(queue.writing).toBe(false);
    expect(queue.queuedTags).toBeNull();
    expect(queue.draftTags).toEqual(["external"]);
    expect(persistedTags).toEqual(["initial"]);

    await queue.change(["external", "focus"]);
    expect(queue.draftTags).toEqual(["external", "focus"]);
    expect(persistedTags).toEqual(["external", "focus"]);
  });
});
