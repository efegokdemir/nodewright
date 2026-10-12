#!/usr/bin/env bash
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

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

write_values() {
	cat >"$tmp_dir/values.yaml"
}

run_target() {
	local yq="$1"
	make --no-print-directory -s -C "$script_dir" print-chart-agent-image \
		"YQ=$yq" "CHART_VALUES=$tmp_dir/values.yaml"
}

assert_output() {
	local expected="$1" yq="$2" actual
	actual="$(run_target "$yq")"
	if [[ "$actual" != "$expected" ]]; then
		printf 'expected %q, got %q\n' "$expected" "$actual" >&2
		exit 1
	fi
}

assert_agent_image_accepted() {
	make --no-print-directory -s -C "$script_dir" check-agent-image "AGENT_IMAGE=$1"
}

yq="${YQ:?YQ must point to the yq binary}"

write_values <<'EOF'
controllerManager:
  manager:
    agent:
      repository: example.com/agent
      tag: v1.2.3
      digest: sha256:abc123
EOF
assert_output 'example.com/agent:v1.2.3@sha256:abc123' "$yq"
assert_agent_image_accepted 'example.com/agent:v1.2.3@sha256:abc123'

write_values <<'EOF'
controllerManager:
  manager:
    agent:
      repository: example.com/agent
      digest: sha256:abc123
EOF
assert_output 'example.com/agent@sha256:abc123' "$yq"
assert_agent_image_accepted 'example.com/agent@sha256:abc123'

write_values <<'EOF'
controllerManager:
  manager:
    agent:
      repository: example.com/agent
      tag: v1.2.3
EOF
assert_output 'example.com/agent:v1.2.3' "$yq"
assert_agent_image_accepted 'example.com/agent:v1.2.3'

write_values <<'EOF'
controllerManager:
  manager:
    agent:
      repository: ""
      tag: v1.2.3
EOF
if run_target "$yq" >"$tmp_dir/output" 2>&1; then
	echo "expected a missing repository to fail" >&2
	exit 1
fi

write_values <<'EOF'
controllerManager:
  manager:
    agent:
      repository: example.com/agent
EOF
if run_target "$yq" >"$tmp_dir/output" 2>&1; then
	echo "expected missing tag and digest to fail" >&2
	exit 1
fi

cat >"$tmp_dir/failing-yq" <<'EOF'
#!/usr/bin/env sh
echo "synthetic yq failure" >&2
exit 23
EOF
chmod +x "$tmp_dir/failing-yq"
if output="$(run_target "$tmp_dir/failing-yq" 2>&1)"; then
	echo "expected a yq failure to fail the target" >&2
	exit 1
fi
if [[ "$output" != *"synthetic yq failure"* ]]; then
	echo "expected the yq error to be preserved" >&2
	printf '%s\n' "$output" >&2
	exit 1
fi

echo "print-chart-agent-image tests passed"
