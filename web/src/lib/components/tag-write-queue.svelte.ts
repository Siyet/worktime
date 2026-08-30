export type TagsWriter = (tags: string[]) => Promise<void>;

// Keeps tag toggles responsive while serializing IndexedDB writes. A failed
// write invalidates every optimistic change derived from it: the queue rolls
// back to the latest external props, then accepts the next user action normally.
export class TagWriteQueue {
  draftTags = $state<string[]>([]);
  queuedTags = $state<string[] | null>(null);
  writing = $state(false);

  private drainPromise: Promise<void> | null = null;

  constructor(
    initialTags: string[],
    private readonly readExternalTags: () => string[],
    private readonly writeTags: TagsWriter,
  ) {
    this.draftTags = [...initialTags];
  }

  reconcile(externalTags: string[]): void {
    if (this.writing || this.queuedTags !== null || tagsEqual(externalTags, this.draftTags)) return;
    this.draftTags = [...externalTags];
  }

  change(nextTags: string[]): Promise<void> {
    this.draftTags = [...nextTags];
    this.queuedTags = [...nextTags];
    this.drainPromise ??= this.drain();
    return this.drainPromise;
  }

  private async drain(): Promise<void> {
    this.writing = true;
    try {
      while (this.queuedTags !== null) {
        const nextTags = this.queuedTags;
        this.queuedTags = null;
        await this.writeTags(nextTags);
      }
    } catch {
      // Later queued values were calculated from the failed optimistic write,
      // so retrying them could persist a state the user never actually had.
      this.queuedTags = null;
      this.draftTags = [...this.readExternalTags()];
    } finally {
      this.writing = false;
      this.reconcile(this.readExternalTags());
      this.drainPromise = null;
    }
  }
}

function tagsEqual(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((tag, index) => tag === right[index]);
}
