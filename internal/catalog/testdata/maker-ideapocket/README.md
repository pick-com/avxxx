# Maker catalog fixtures (SYNTHETIC)

**Every file in this directory is synthetic.** Nothing was fetched from the
studio's site; the markup is hand-written and the titles, names, product
numbers and images are fictional. Each HTML file starts with a
`SYNTHETIC FIXTURE` comment and every manifest row has
`"origin": "synthetic"`, so none counts toward the recorded-fixture target.

The layout is the same as `../maker-s1/` (see its README for the per-file
coverage table) with this profile's values: own codes `IPZZ-900..902`, a
foreign `SONE-123` title that must be skipped, actress `50001`.

The built-in `ideapocket` profile shares the `s1` selectors on purpose: it
shows that a second studio is a profile, not new code. Whether the real
site matches is unverified; follow `docs/MAKER_CATALOG.md` before enabling.
