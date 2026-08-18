---
name: cut-release
description: Cut a tagged release of db-query - find the last tag, bump the version, summarise what landed since it into an annotated tag message, push the tag, watch the GoReleaser workflow, and verify both the GitHub release and the Homebrew tap formula. Use this whenever the user wants to ship, tag, release or publish a new version, or mentions cutting a release, bumping the version for a release, pushing a release tag, triggering the release workflow, or getting a new version into Homebrew. Use it even for short asks like "release this", "tag v0.12.0", "ship it", "push a new release" or "cut 0.11", and whether they ask for a bump or name an explicit version.
---

# Cut a tagged release

One tag push is the whole release mechanism here. Pushing `vX.Y.Z` fires
`.github/workflows/release.yml`, which runs GoReleaser, which builds the
binaries, publishes a GitHub Release, and commits a formula to
`geraldcsoftware/homebrew-tap`. Nothing else triggers a release and nothing
else needs editing first.

**Invoking this skill is the release approval.** Run it through to the end
without stopping to ask whether to push. That is the point of the invocation.

This does not mean run past problems. Stop and report if a preflight check
fails, if the workflow fails, or if the tap does not update. Those are broken
states, not decisions the user needs to make, and pushing a tag is awkward to
undo once GoReleaser has published against it.

## The version is not in the code

Worth knowing before you go hunting: `db-query` has no version constant. The
binary gets its version from `-ldflags -X main.version=...`, which GoReleaser
fills in from the tag (`cmd/db-query/main.go`, `internal/cli/cli.go` keeps
`"dev"` as the fallback for local builds). The tag is the single source of
truth, so there is no file to bump and no commit to make before tagging.

## Steps

### 1. Preflight

Every one of these has bitten a real release. Run them before anything else
and stop if any fails.

```bash
git fetch --tags -q                              # else you bump a stale view
git status --porcelain                           # must be empty
git rev-parse --abbrev-ref HEAD                  # must be main
git rev-parse HEAD; git rev-parse origin/main    # must match
git log --oneline $(git tag --sort=-v:refname | head -1)..HEAD | wc -l   # must be > 0
```

A dirty tree or unpushed commits matter more than they look: GoReleaser builds
from the pushed tag, so anything only on your disk silently will not be in the
release, and the published binary will not match what you tested.

Zero commits since the last tag means there is nothing to release, usually
because one was just cut. Say so and stop rather than publishing a version
identical to the one before it.

### 2. Work out the version

```bash
git tag --sort=-v:refname | head -5
```

Bump the **minor** and reset the patch: `v0.10.0` becomes `v0.11.0`. That is
the default because feature work is what usually accumulates here.

An explicit version in the user's request always wins. "tag v1.0.0", "cut
0.10.2" and "make it a major" all override the default, and no argument means
bump the minor.

Say which commits you saw before committing to a number, e.g. "4 feat, 1 fix,
nothing breaking, so minor". If that reading contradicts the default - a
fix-only run heading for a minor, or a breaking change heading for anything
below major - say so in one line, then follow the default anyway unless the
user asked otherwise. They can override; a silent wrong bump they cannot.

Refuse to reuse a tag:

```bash
git rev-parse -q --verify refs/tags/vX.Y.Z && echo "EXISTS - stop"
```

### 3. Summarise what landed

```bash
git log --oneline <last-tag>..HEAD
git log <last-tag>..HEAD --format='--- %h %s%n%b'   # bodies carry the reasoning
git diff --stat <last-tag>..HEAD | tail -3
```

Read the bodies, not just the subjects. The subject says what changed; the
body says what it fixes and what it costs, which is what a reader of the tag
actually needs.

Then write the message as **prose grouped by feature**, not a replayed commit
list. Someone running `git tag -n30 vX.Y.Z` wants to know what they are
getting and what will break, and a list of subjects makes them reconstruct
that themselves. Six commits from one pull request are one feature; say it
once.

Shape that has worked:

- A one-line summary naming the headline features.
- A paragraph per feature: what it does now, and what it did before if the
  change fixes something the user would have noticed.
- A closing paragraph for anything that changes existing behaviour - new
  minimum versions, keys that used to do something else, defaults that moved.
  Do not bury these; they are the reason someone reads a tag message.

Match the house style: British English, plain prose, no emoji, no dashes as
punctuation (commas, brackets or colons instead).

### 4. Tag and push

Annotated, always, because every previous tag is annotated and `git tag -n`
shows nothing useful for a lightweight one.

```bash
git tag -a vX.Y.Z -F - <<'EOF'
<message>
EOF

git tag -l vX.Y.Z -n30                                   # read it back
git for-each-ref refs/tags/vX.Y.Z --format='%(objecttype) -> %(*objectname:short)'
git push origin vX.Y.Z
```

The read-back is worth the second: a local tag is free to delete with
`git tag -d`, a pushed one is not.

### 5. Watch the workflow

The run needs a moment to appear after the push.

```bash
sleep 12
gh run list --workflow=release.yml --limit 3 \
  --json databaseId,status,conclusion,headBranch \
  --jq '.[] | "\(.databaseId)  \(.status)/\(.conclusion // "-")  ref:\(.headBranch)"'

gh run watch <id> --exit-status --interval 15
```

Confirm the run's ref is the tag you just pushed before watching it, or you
may be watching the previous release.

On failure, `gh run view <id> --log-failed` gives the reason. The tag is
already public at this point, so fix forward with a new patch tag rather than
deleting the tag and re-pushing it: a deleted tag that GoReleaser already
published against leaves a release pointing at nothing.

### 6. Verify both ends

The workflow going green is not proof the release is usable. Check what it
produced.

```bash
gh release view vX.Y.Z --json tagName,isDraft,publishedAt,assets \
  --jq '"\(.tagName) draft:\(.isDraft) assets:\(.assets|length)"'
gh release view vX.Y.Z --json assets --jq '.assets[].name'
```

Then the tap. Read the owner, repo and formula name from the `brews:` block in
`.goreleaser.yaml` rather than assuming them, so this keeps working if the tap
moves:

```bash
gh api repos/geraldcsoftware/homebrew-tap/commits \
  --jq '.[0] | "\(.sha[0:7])  \(.commit.message | split("\n")[0])  \(.commit.author.date)"'

gh api repos/geraldcsoftware/homebrew-tap/contents/Formula/db-query.rb \
  --jq '.content' | base64 -d | grep -E '^\s+(version|url) '
```

The formula's `version` must be the new number and its URLs must point at the
new tag. A green workflow with a stale formula usually means
`HOMEBREW_TAP_GITHUB_TOKEN` has expired: it is a personal access token with
write access to the tap, and `GITHUB_TOKEN` cannot stand in for it because it
cannot write to another repository.

Expect **darwin only**, `amd64` and `arm64`, three assets with `checksums.txt`.
That is what `.goreleaser.yaml` builds, so it is correct rather than a missing
platform; do not report it as a failure. If the user ever wants
`brew install` to work on Linux, that is a change to `.goreleaser.yaml`, not
to this process.

## Report at the end

Give the user the version, what went into it, and proof both ends landed:
the workflow conclusion and run URL, the release asset count, and the tap's
formula version and commit. If anything looks off - an unexpected asset list,
a tap commit older than the release - say so plainly rather than rounding up
to success.
