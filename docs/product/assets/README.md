# M.A.R.C.U.S. brand assets

Files used by the M.A.R.C.U.S. documentation pack.

## Files

| File | Size | Use |
|---|---|---|
| `involve-icon.png` | ~200 × 200 | Involve product mark. Embedded on the DOCX cover pages and pre-rendered banner. Also lives at `../involve-icon.png` for backwards compatibility. |
| `marcus-header-banner.png` | 1800 × 380 | Full-width M.A.R.C.U.S. banner. Used as the cover-page hero on the Product Overview and Datasheet. Also standalone-usable in any Word document — Insert → Pictures → this file. |
| `marcus-header-band.png` | 1800 × 120 | Slim variant of the banner. Suitable for repeating page headers or slide masters. |

## Regenerating

The banners are generated from a Python script — regenerate any time
the logo or brand values change:

```
python \
  "%LOCALAPPDATA%/Temp/claude/c--Code-av-bridge/<session-id>/scratchpad/make_marcus_banner.py"
```

The DOCX regeneration script picks the banner up automatically:

```
python \
  "%LOCALAPPDATA%/Temp/claude/c--Code-av-bridge/<session-id>/scratchpad/marcus_md_to_docx.py"
```

## Brand values baked in

| | |
|---|---|
| Indigo (primary) | `#2A1B5E` |
| Magenta (bold accent) | `#E91E7D` |
| Cyan (soft accent) | `#4FC3E0` |
| Body font | Segoe UI |

Colours match the M.A.R.C.U.S. PowerPoint slide. If involve.vc's canonical
purple differs, update `INDIGO` in `make_marcus_banner.py` and re-run.
