let pending: Promise<any> | undefined;
export function loadImageEngine(): Promise<any> {
  if (!pending) pending = (async () => {
    for (const file of ["tui-color-picker.min.js", "tui-image-editor.min.js"]) {
      await new Promise<void>((resolve, reject) => {
        const script = document.createElement("script");
        script.src = `/vendor/file-editors/${file}`;
        script.onload = () => resolve();
        script.onerror = () => { script.remove(); reject(Error("Image engine could not load")); };
        document.head.append(script);
      });
    }
    return (window as any).tui.ImageEditor;
  })().catch(reason => { pending = undefined; throw reason; });
  return pending;
}
export function imageDataURL(blob: Blob, type: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(new Blob([blob], { type }));
  });
}
