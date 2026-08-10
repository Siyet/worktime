// Saving generated text as a file. Local-first: nothing here asks the server for
// something the page already holds.
export function downloadText(filename: string, mimeType: string, text: string): void {
  const blobURL = URL.createObjectURL(new Blob([text], { type: mimeType }));
  const anchor = document.createElement("a");
  anchor.href = blobURL;
  anchor.download = filename;
  // The download is handed to the browser asynchronously, so the URL is released on
  // the next tick - revoking it in the same task can invalidate the blob before the
  // fetch behind the download starts. The anchor goes into the document because a
  // synthetic click on a detached element does not navigate everywhere.
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(blobURL), 0);
}
