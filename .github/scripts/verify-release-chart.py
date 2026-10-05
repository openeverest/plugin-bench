"""Check the packaged chart's metadata and rendered image references before upload."""

import subprocess
import sys

import yaml


def check(actual, expected, field):
    if actual != expected:
        raise ValueError(f"{field}: expected {expected!r}, got {actual!r}")


def verify(chart, version, image):
    metadata = yaml.safe_load(subprocess.check_output(["helm", "show", "chart", chart]))
    check(metadata["version"], version, "chart version")
    check(metadata["appVersion"], version, "appVersion")

    values = yaml.safe_load(subprocess.check_output(["helm", "show", "values", chart]))
    for name, config, repository in [
        ("backend", values["image"], image),
        ("runner", values["runner"]["image"], f"{image}-pgbench"),
    ]:
        check(config["repository"], repository, f"{name} repository")
        check(config["tag"], version, f"{name} tag")

    rendered = subprocess.check_output([
        "helm", "template", "plugin-bench", chart, "--namespace", "everest-system",
    ])
    resources = [resource for resource in yaml.safe_load_all(rendered) if resource]
    deployments = [resource for resource in resources if resource["kind"] == "Deployment"]
    plugins = [resource for resource in resources if resource["kind"] == "Plugin"]
    check(len(deployments), 1, "Deployment count")
    check(len(plugins), 1, "Plugin count")
    containers = deployments[0]["spec"]["template"]["spec"]["containers"]
    backend = next(container for container in containers if container["name"] == "plugin-bench")
    check(backend["image"], f"{image}:{version}", "Deployment image")
    environment = {item["name"]: item.get("value") for item in backend["env"]}
    check(environment.get("RUNNER_IMAGE"), f"{image}-pgbench:{version}", "RUNNER_IMAGE")
    check(plugins[0]["spec"]["version"], version, "Plugin version")


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("Usage: verify-release-chart.py CHART VERSION IMAGE_REPOSITORY")
    verify(*sys.argv[1:])
    print("Packaged chart versions and image references verified")
