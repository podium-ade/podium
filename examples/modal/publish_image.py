"""Build an agent image in Modal and publish it by name, for `image: modal:<name>`.

Podium does not build images. On Modal you build them with Modal's own SDK, which builds in
Modal's cloud and keeps the result there: a sandbox then starts from it without pulling from
any registry, so an image can come from a Dockerfile on your machine, or a base in a registry
Modal can reach, and never needs a registry of yours at all.

    pip install modal          # and `modal token new`, once
    python examples/modal/publish_image.py agent/runtime/Dockerfile.dev \
        --context agent/runtime \
        --build-arg RUNTIME_IMAGE=ghcr.io/podium-ade/podium-agent-runtime:0.7.3 \
        --name podium-agent-runtime-dev:0.7.3

then point a playbook (or a provider's Ready sandbox image)
at `modal:podium-agent-runtime-dev:0.7.3`.

A base in a private registry Modal cannot reach on its own is pulled with a login:
--registry-username names the user and REGISTRY_PASSWORD holds the password, which goes to
Modal as an ephemeral secret for this build only. On Google Artifact Registry a short-lived
token does, since the pull happens once, at build:

    REGISTRY_PASSWORD=$(gcloud auth print-access-token) python examples/modal/publish_image.py \
        examples/modal/docker-engine.Dockerfile \
        --build-arg BASE=us-docker.pkg.dev/<project>/<repo>/<image>:<tag> \
        --registry-username oauth2accesstoken --name <image>-docker:<tag>

Keep the Dockerfile single-stage, FROM the published runtime: Modal documents no multi-stage
support. Build FROM ghcr.io/podium-ade/podium-agent-runtime and the runtime's own build is
already done. Modal ignores USER (its sandboxes run as root; Podium runs the turn as an
account of its own) and Podium names the runtime's command itself, so neither matters here.
"""

import argparse
import os
import re
import tempfile

import modal


def with_from_args(dockerfile: str, build_args: dict[str, str]) -> str:
    """A copy of dockerfile with build args substituted into its FROM lines.

    Modal forwards build_args to RUN steps but resolves a FROM line's ${ARG} from the ARG's
    default alone, so `FROM ${RUNTIME_IMAGE}` would build on the default rather than the
    image asked for. Substituting them here makes FROM mean what `docker build` makes it mean.
    """
    with open(dockerfile) as f:
        lines = f.read().splitlines(keepends=True)
    values = {}
    for line in lines:
        if m := re.match(r"\s*ARG\s+(\w+)=(\S+)", line):
            values.setdefault(m[1], m[2])
    values.update(build_args)

    def sub(line: str) -> str:
        if not re.match(r"\s*FROM\s", line, re.I):
            return line
        return re.sub(r"\$\{(\w+)\}|\$(\w+)", lambda m: values.get(m[1] or m[2], m[0]), line)

    fd, path = tempfile.mkstemp(suffix=".Dockerfile")
    with os.fdopen(fd, "w") as f:
        f.writelines(sub(line) for line in lines)
    return path


def from_private_base(dockerfile: str, context: str, build_args: dict[str, str], username: str) -> modal.Image:
    """The Dockerfile's base pulled with a registry login, and the rest of it applied on top.

    Modal pulls a Dockerfile's FROM with no credentials, so the base is pulled the way
    Image.from_registry pulls one, and the lines after FROM are run as the Dockerfile's own.
    """
    password = os.environ.get("REGISTRY_PASSWORD", "")
    if not password:
        raise SystemExit("--registry-username needs REGISTRY_PASSWORD in the environment")
    with open(dockerfile) as f:
        lines = f.read().splitlines()
    froms = [i for i, line in enumerate(lines) if re.match(r"\s*FROM\s", line, re.I)]
    if len(froms) != 1:
        raise SystemExit("a private base needs a single-stage Dockerfile: exactly one FROM")
    base = lines[froms[0]].split()[1]
    login = modal.Secret.from_dict({"REGISTRY_USERNAME": username, "REGISTRY_PASSWORD": password})
    rest = [line for i, line in enumerate(lines) if i > froms[0]]
    return modal.Image.from_registry(base, secret=login).dockerfile_commands(
        rest, context_dir=context, build_args=build_args
    )


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("dockerfile")
    ap.add_argument("--context", default=".", help="the build context COPY reads from")
    ap.add_argument("--name", required=True, help="what to publish it as, name[:tag]")
    ap.add_argument("--build-arg", action="append", default=[], metavar="KEY=VALUE")
    ap.add_argument("--app", default="podium-sandbox", help="the Modal app the build runs in")
    ap.add_argument("--env", default=None, help="the Modal environment; the profile's by default")
    ap.add_argument(
        "--registry-username",
        help="pull the FROM image with this user and REGISTRY_PASSWORD, for a private registry",
    )
    args = ap.parse_args()

    build_args = dict(kv.split("=", 1) for kv in args.build_arg)
    dockerfile = with_from_args(args.dockerfile, build_args)
    if args.registry_username:
        image = from_private_base(dockerfile, args.context, build_args, args.registry_username)
    else:
        image = modal.Image.from_dockerfile(dockerfile, context_dir=args.context, build_args=build_args)
    app = modal.App.lookup(args.app, create_if_missing=True, environment_name=args.env)
    with modal.enable_output():
        image.build(app)
    image.publish(args.name, environment_name=args.env)
    print(f"published {image.object_id} as {args.name}: use image: modal:{args.name}")


if __name__ == "__main__":
    main()
