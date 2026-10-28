# Bundled fonts

Latin subsets of EB Garamond (display), Inter (body) and JetBrains Mono (code), plus a login-only Geist Mono subset, served by NodeDance itself. DESIGN.md explicitly permits EB Garamond and Inter as substitutes for licensed typefaces. Chinese text uses the system serif/sans fallback stacks.

Sources: [Google Fonts](https://github.com/google/fonts), fetched through its CSS API on 2026-10-28. The Geist Mono file is the [exact-glyph 600-weight response](https://fonts.googleapis.com/css2?family=Geist+Mono:wght@600&display=swap&text=%3A%3Bt%25SX8%23%40.%27%2C%60o-*%C2%B7) for the login canvas. Each family’s SIL Open Font License is included here. Each family uses one font file. No external font requests are made at runtime.

## Subset profile

| Family | Weights | Original Latin source | Bundled subset |
| --- | --- | ---: | ---: |
| EB Garamond | 400 | 44,336 B | 19,328 B |
| Inter | 400–700 | 48,256 B | 26,032 B |
| JetBrains Mono | 400 | 21,168 B | 8,032 B |
| Geist Mono | 600 (login canvas) | exact 18-codepoint subset | 1,808 B |
| Total | | | 55,200 B |

The shared UI fonts retain printable ASCII, Latin-1 and common punctuation for dynamic node names, paths and terminal output. The terminal has no ligature addon, so its unused code ligatures are removed. The login-only Geist Mono file contains just the glyphs drawn by the canvas, including `-`, `·`, `*` and the shark texture characters. Chinese and other absent glyphs fall back to system fonts. Licenses are retained separately.

Rebuild with FontTools 4.66.1 and Brotli 1.1.0:

```sh
python3 scripts/subset-web-fonts.py --source-dir /path/to/upstream-latin-woff2
```

The script verifies the shared UI font source SHA256s, checks preserved character coverage and validates the resulting weight axes. Source checksums are recorded in the script. EB Garamond and JetBrains Mono are static fonts; Inter retains only the 400–700 variable range. Geist Mono is pinned to the Google Fonts API's exact-glyph WOFF2 response; its SHA-256 is `f77b7f94705bc2693d1067a2f4e83a372afed16cceb1f77391c3ca4e7301c114`.

Pinned Latin source files: [EB Garamond](https://fonts.gstatic.com/s/ebgaramond/v33/SlGUmQSNjdsmc35JDF1K5GR1SDk.woff2), [Inter](https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa1ZL7.woff2), [JetBrains Mono](https://fonts.gstatic.com/s/jetbrainsmono/v24/tDbY2o-flEEny0FZhsfKu5WU4zr3E_BX0PnT8RD8yKxTOlOV.woff2).

The variable Inter range includes 600 and 700 so the Claude Figma kit’s semibold/bold roles use real font weights. Anthropic Sans Text is referenced in CSS but is not bundled; Inter remains the licensed local fallback.
