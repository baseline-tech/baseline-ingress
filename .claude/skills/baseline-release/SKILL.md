---
name: baseline-release
description: Release a new baseline-ingress Docker image to GitHub Packages (ghcr.io/baseline-tech/baseline-ingress). Use for /baseline-release, "release ingress", "deploy a new ingress image", or "publish the GPU ingress image".
---

# baseline-release

Publishes `ghcr.io/baseline-tech/baseline-ingress` by pushing a version tag to the
`baseline` remote. The `Baseline release` GitHub Actions workflow
(`.github/workflows/baseline-release.yaml`) runs the checks on a native amd64 runner, then
builds and pushes the image. Nothing is built locally: amd64 builds under emulation on Apple
Silicon take far too long.

## Version scheme

Tags look like `v<upstream>-gpu.<n>`, for example `v1.5.0-gpu.3`. `<upstream>` is the
livekit/ingress release the fork is based on (`git describe --tags --abbrev=0 --exclude '*-gpu.*'`).
`<n>` counts fork releases on that base and restarts at 1 when the base changes after an
upstream merge. Each release pushes the image tags `<tag>`, `sha-<short sha>` and `latest`.

## Steps

Stop and report at the first failing step. Never move or delete an existing tag.

1. **Find the remote.** `git remote -v` and pick the remote whose URL is
   `github.com/baseline-tech/baseline-ingress` (usually `baseline`). If none exists, add it:
   `git remote add baseline https://github.com/baseline-tech/baseline-ingress.git`.
   Then `git fetch <remote> --tags` and `git fetch origin --tags` (origin is livekit/ingress;
   it supplies the upstream base tags).

2. **Check what is being released.** The release branch is `gpu-transcoding` unless the user
   names another. Require all of:
   - `git status --porcelain` is empty;
   - HEAD is that branch and equals `<remote>/<branch>` (push first if the user asks, never force);
   - the latest `Baseline release` run for HEAD succeeded:
     `gh run list -R baseline-tech/baseline-ingress -w "Baseline release" -c $(git rev-parse HEAD) --json status,conclusion,url`.
     If it is still running, `gh run watch <id> -R baseline-tech/baseline-ingress --exit-status`.
     If there is no run, the commit was never pushed; go back to the previous check.

3. **Compute the next tag.**
   - `base=$(git describe --tags --abbrev=0 --exclude '*-gpu.*' HEAD)`
   - `last=$(git tag -l "$base-gpu.*" --sort=-v:refname | head -1)`
   - next is `$base-gpu.1` when `last` is empty, otherwise `last` with `<n>+1`.
   - The user may pass an explicit tag as the argument; it must match `v*-gpu.*` and not exist.

4. **Confirm with the user.** Show the tag, the commit, and the changelog since the previous
   fork tag (`git log --oneline <last>..HEAD`, or `<base>..HEAD` for the first release).
   A pushed tag publishes an image, so wait for an explicit yes.

5. **Tag and push.** `git tag -a <tag> -m "baseline-ingress <tag>"` then `git push <remote> <tag>`.

6. **Watch the release.** Find the run for the tag
   (`gh run list -R baseline-tech/baseline-ingress -w "Baseline release" -b <tag> --limit 1`)
   and `gh run watch <id> -R baseline-tech/baseline-ingress --exit-status`. On failure, show
   `gh run view <id> -R baseline-tech/baseline-ingress --log-failed | tail -60`. Do not delete
   the tag; fix forward with the next `<n>`.

7. **Verify the image.** `docker manifest inspect ghcr.io/baseline-tech/baseline-ingress:<tag>`
   (needs `docker login ghcr.io`, see below) or
   `gh api /orgs/baseline-tech/packages/container/baseline-ingress/versions --jq '.[0].metadata.container.tags'`.

8. **Report** the tag, the run URL, the image references
   (`ghcr.io/baseline-tech/baseline-ingress:<tag>` and `:latest`), the digest, and the pull
   command for GPU hosts.

## Access

- Releasing needs `gh` logged in with the `repo` and `workflow` scopes. The workflow itself
  pushes with its own `GITHUB_TOKEN`, so no secret is configured in the repo.
- The package is private. To pull on a server, create a classic token with `read:packages`
  and run `echo <token> | docker login ghcr.io -u <github user> --password-stdin`.
- GPU hosts run the image with
  `docker run --gpus all -e NVIDIA_DRIVER_CAPABILITIES=compute,video,utility ghcr.io/baseline-tech/baseline-ingress:<tag>`
  and `gpu.enabled: true` in the ingress config.
