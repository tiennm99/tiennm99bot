# tiennm99bot

## Git

Commit and push directly to `main` when asked to commit or push. Do not create
a feature branch first, and do not open a pull request unless asked for one.
This overrides the default "if on the default branch, branch first" behaviour.

`main` is this repository's working branch: it is single-maintainer, not
protected, and deploys from Coolify on push. Feature branches are still fine
when the work genuinely warrants review — ask rather than assume.

The rest of the commit rules still apply: conventional commit format, no AI
references, focused commits, and never commit secrets or `.env` files.
