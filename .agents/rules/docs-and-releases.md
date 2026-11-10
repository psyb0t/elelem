# Docs and releases

- **DR1:** The documentation site is `docs/`, published by GitHub Pages from `main` at https://psyb0t.github.io/elelem/ with the Just the Docs theme (`remote_theme` pinned in `docs/_config.yml`, colors in `docs/_sass/`, the shared visitor tracker in `docs/_includes/head_custom.html`). The site title equals the README H1.
- **DR2:** Every page has front matter with `title`, a 50 to 170 character `description` and `nav_order`. A child page sets `parent` to the parent's exact title.
- **DR3:** Links from `docs/` to files outside it use full `https://github.com/psyb0t/elelem/blob/main/<path>` URLs (`tree/` for directories). README links to the site use full `https://psyb0t.github.io/elelem/...` URLs.
- **DR4:** Wrap code blocks containing `{{` or `{%` in `<!-- {% raw %} -->` and `<!-- {% endraw %} -->`.
- **DR5:** After a docs change, run `make docs` and check the built site in `.testing/docs-site`.
- **DR6:** A public API or behavior change updates the matching docs page in the same change.
- **DR7:** Versions follow semver pre-1.0: a minor bump may break the API and says so; a patch is docs, build or fixes only. Commit subjects use conventional prefixes (`feat:`, `fix:`, `docs:`, `ci:`), with `!` for a breaking change.
- **DR8:** `CHANGELOG.md` entries are `## vX.Y.Z (YYYY-MM-DD)`, a one-line summary that states whether migration is needed, then bullets starting with "Adds", "Changes", "Fixes" or "Removes".
- **DR9:** A release is an annotated `vX.Y.Z` tag on a commit whose `main` pipeline run passed. The tag message's first line is exactly `vX.Y.Z`, then a blank line, then that version's CHANGELOG section. The pipeline waits for the `main` run and cuts the GitHub release from the tag message.
