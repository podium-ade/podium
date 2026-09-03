## What this changes

<!-- One paragraph. What behaviour is different afterwards, and why. -->

## What I ran

<!--
Paste the output, not a claim. `make e2e` is the contract and is NOT in CI, so run it locally.
Delete lines that genuinely do not apply and say why.
-->

```
make lint test build
go test -tags integration ./... -count=1
make e2e
cd web && pnpm lint && pnpm typecheck && pnpm test
```

## Checklist

- [ ] Every changed line traces to the change described above — no drive-by reformatting or
      renaming.
- [ ] No test was skipped, loosened or deleted to get green.
- [ ] No secret, token or key material is in the diff, and no new spelling of one escapes
      `.gitignore`.
- [ ] A new `PODIUM_*` variable is in `deploy/.env.example` (`go test ./deploy/...` enforces it
      both ways).
- [ ] A new proto field: `make proto` was run and the generated Go **and** TypeScript are
      committed. `buf breaking` is clean.
- [ ] A new migration takes the next free number and no applied migration was edited.
- [ ] Docs are updated. If this proves something currently marked **UNVERIFIED**, that marker is
      deleted; if it adds a new unproven path, a marker is added.
- [ ] Nothing pulls, builds, tags or removes a Docker image that did not have to.
- [ ] Every container, network and volume a new test creates is torn down.

## Anything a reviewer should know

<!--
Deviations, things you decided not to do, and anything you could not verify. Say it here rather
than letting it be discovered.
-->
