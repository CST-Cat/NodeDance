# Bundled fonts

Latin subsets of EB Garamond (display), Inter (body) and JetBrains Mono (code), served by NodeDance itself. DESIGN.md explicitly permits EB Garamond and Inter as substitutes for licensed typefaces. Chinese text uses the system serif/sans fallback stacks.

Source: [Google Fonts](https://github.com/google/fonts), fetched 2026-10-11 through its CSS API. Each family’s SIL Open Font License is included here. Each family uses one font file. No external font requests are made at runtime.

## Subset profile

| Family | Weights | Original Latin source | Bundled subset |
| --- | --- | ---: | ---: |
| EB Garamond | 400 | 44,336 B | 19,328 B |
| Inter | 400–500 | 48,256 B | 24,268 B |
| JetBrains Mono | 400 | 21,168 B | 8,032 B |
| Total | | 113,760 B | 51,628 B |

This removes unused weight ranges and glyphs, while retaining printable ASCII, Latin-1 and common punctuation for dynamic node names, paths and terminal output. The terminal has no ligature addon, so its unused code ligatures are removed. Chinese and other absent glyphs fall back to system fonts. Font payload is reduced by 54.6%; licenses are retained separately.

Rebuild with FontTools 4.66.1 and Brotli 1.1.0:

```sh
python3 scripts/subset-web-fonts.py --source-dir /path/to/upstream-latin-woff2
```

The script verifies each upstream source SHA256, checks preserved character coverage and validates the resulting weight axes. Source checksums are recorded in the script. EB Garamond and JetBrains Mono are static fonts; Inter retains only the 400–500 variable range.

Pinned Latin source files: [EB Garamond](https://fonts.gstatic.com/s/ebgaramond/v33/SlGUmQSNjdsmc35JDF1K5GR1SDk.woff2), [Inter](https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa1ZL7.woff2), [JetBrains Mono](https://fonts.gstatic.com/s/jetbrainsmono/v24/tDbY2o-flEEny0FZhsfKu5WU4zr3E_BX0PnT8RD8yKxTOlOV.woff2).
