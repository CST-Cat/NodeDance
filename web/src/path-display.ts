export function pathDisplay(value: string, directory = false) {
  const slash = value.lastIndexOf("/");
  const parent = slash >= 0 ? value.slice(0, slash + 1) : "";
  const name = slash >= 0 ? value.slice(slash + 1) : value;
  const compound = name.match(/\.(?:tar\.(?:gz|bz2|xz|zst)|d\.ts)$/i)?.[0];
  const dot = name.lastIndexOf(".");
  const extension = directory ? "" : compound ?? (dot > 0 ? name.slice(dot) : "");
  return { parent, stem: extension ? name.slice(0, -extension.length) : name, extension };
}

// elFinder's documented cwd template extension uses the same name parts as
// React paths. Escape every part before entering its HTML template.
export function fileNameMarkup(name: string, directory = false) {
  const { stem, extension } = pathDisplay(name, directory);
  const escape = (value: string) => value.replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
  return `<span class="ui-path-name"><span class="ui-path-stem">${escape(stem)}</span><span class="ui-path-extension">${escape(extension)}</span></span>`;
}
