#!/usr/bin/env python3

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Resolve the versioned image targets for the nightly vulnerability scan."""

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Iterable, Optional, Tuple


IMAGE_PREFIX = "ghcr.io/nvidia/nodewright"
VERSION_PATTERN = re.compile(r"^v(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$")
COMPONENTS = ("operator", "agent")


def parse_version(tag: str, component: str) -> Optional[Tuple[int, int, int, int, int]]:
    """Return a sortable semver key for a supported component tag."""
    prefix = f"{component}/"
    if not tag.startswith(prefix):
        return None

    match = VERSION_PATTERN.fullmatch(tag[len(prefix) :])
    if match is None:
        return None

    major, minor, patch = (int(part) for part in match.group(1, 2, 3))
    rc = match.group(4)
    # A stable version sorts after every RC of the same version.
    return (major, minor, patch, 0 if rc is not None else 1, int(rc or 0))


def latest_newer_prerelease(tags: Iterable[str], component: str) -> Optional[str]:
    """Return the newest RC only when it is newer than the latest stable tag."""
    stable: list[tuple[tuple[int, int, int, int, int], str]] = []
    prereleases: list[tuple[tuple[int, int, int, int, int], str]] = []

    for tag in tags:
        version = parse_version(tag, component)
        if version is None:
            continue
        destination = prereleases if "-rc." in tag else stable
        destination.append((version, tag))

    if not prereleases:
        return None

    newest_prerelease = max(prereleases)
    newest_stable = max(stable, default=None)
    if newest_stable is not None and newest_prerelease[0] <= newest_stable[0]:
        return None

    return newest_prerelease[1][len(component) + 1 :]


def resolve_targets(main_tags: dict[str, str], release_tags: Iterable[str]) -> dict[str, list[dict[str, str]]]:
    """Build the Grype matrix, retaining stable rows when no main build exists."""
    targets = []
    release_tags = list(release_tags)

    for component in COMPONENTS:
        image = f"{IMAGE_PREFIX}/{component}"
        main_tag = main_tags.get(component, "")
        targets.append(
            {
                "name": f"{component}-main",
                "image": image,
                "tag": main_tag,
                "category": f"grype-{component}-main",
            }
        )

        prerelease = latest_newer_prerelease(release_tags, component)
        if prerelease is not None:
            targets.append(
                {
                    "name": f"{component}-prerelease",
                    "image": image,
                    "tag": prerelease,
                    "category": f"grype-{component}-prerelease",
                }
            )

    return {"include": targets}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--main-tags-json", required=True, help="JSON object mapping component names to image tags")
    parser.add_argument("--release-tags-file", required=True, type=Path, help="File containing one Git tag per line")
    args = parser.parse_args()

    main_tags = json.loads(args.main_tags_json)
    if not isinstance(main_tags, dict) or any(component not in COMPONENTS for component in main_tags):
        parser.error("main tags must be a JSON object keyed by operator and/or agent")

    targets = resolve_targets(main_tags, args.release_tags_file.read_text().splitlines())
    json.dump(targets, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
