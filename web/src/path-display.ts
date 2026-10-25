export function pathDisplay(value: string, directory = false) {
  const slash = value.lastIndexOf("/");
  const parent = slash >= 0 ? value.slice(0, slash + 1) : "";
  const name = slash >= 0 ? value.slice(slash + 1) : value;
  const compound = name.match(/\.(?:tar\.(?:gz|bz2|xz|zst)|d\.ts)$/i)?.[0];
  const dot = name.lastIndexOf(".");
  const extension = directory ? "" : compound ?? (dot > 0 ? name.slice(dot) : "");
  return { parent, stem: extension ? name.slice(0, -extension.length) : name, extension };
}
