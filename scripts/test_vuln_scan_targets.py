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

import unittest

from vuln_scan_targets import latest_newer_prerelease, resolve_targets


class LatestPrereleaseTests(unittest.TestCase):
    def test_selects_newest_prerelease_when_ahead_of_latest_stable(self):
        tags = [
            "operator/v0.18.0",
            "operator/v0.20.0-rc.1",
            "operator/v0.20.0-rc.3",
            "operator/v0.20.0-rc.2",
        ]

        self.assertEqual(latest_newer_prerelease(tags, "operator"), "v0.20.0-rc.3")

    def test_does_not_select_prerelease_behind_latest_stable(self):
        tags = ["agent/v0.20.0-rc.2", "agent/v0.20.0"]

        self.assertIsNone(latest_newer_prerelease(tags, "agent"))

    def test_compares_release_lines_numerically(self):
        tags = ["agent/v0.9.0", "agent/v0.10.0-rc.1"]

        self.assertEqual(latest_newer_prerelease(tags, "agent"), "v0.10.0-rc.1")

    def test_ignores_unsupported_or_other_component_tags(self):
        tags = [
            "operator/v0.21.0-beta.1",
            "agent/v1.0.0-rc.1",
        ]

        self.assertIsNone(latest_newer_prerelease(tags, "operator"))


class ResolveTargetsTests(unittest.TestCase):
    def test_main_scans_use_resolved_build_tags_and_distinct_categories(self):
        matrix = resolve_targets({"operator": "abc1234", "agent": "def5678"}, [])

        self.assertEqual(
            matrix["include"],
            [
                {
                    "name": "operator-main",
                    "image": "ghcr.io/nvidia/nodewright/operator",
                    "tag": "abc1234",
                    "category": "grype-operator-main",
                },
                {
                    "name": "agent-main",
                    "image": "ghcr.io/nvidia/nodewright/agent",
                    "tag": "def5678",
                    "category": "grype-agent-main",
                },
            ],
        )

    def test_missing_main_build_is_skipped_without_losing_other_targets(self):
        matrix = resolve_targets({"agent": "def5678"}, ["operator/v0.21.0-rc.1"])

        self.assertEqual(matrix["include"][0]["tag"], "")
        self.assertEqual(matrix["include"][1]["tag"], "v0.21.0-rc.1")
        self.assertEqual(matrix["include"][2]["tag"], "def5678")

    def test_newer_prerelease_gets_its_own_category(self):
        matrix = resolve_targets(
            {"operator": "abc1234", "agent": "def5678"},
            ["operator/v0.20.0", "operator/v0.21.0-rc.1"],
        )

        self.assertEqual(matrix["include"][1]["tag"], "v0.21.0-rc.1")
        self.assertEqual(matrix["include"][1]["category"], "grype-operator-prerelease")


if __name__ == "__main__":
    unittest.main()
