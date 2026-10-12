/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package step

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("shared step behavior", func() {
	It("applies every construction option and the remaining defaults", func() {
		configured := newStepOptions(
			"step.sh",
			WithName("custom"),
			WithArguments([]string{"argument"}),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithEnv(map[string]string{"KEY": "value"}),
			WithOnHost(false),
			WithIdempotence(Disabled),
			WithRequiresInterrupt(true),
		)

		Expect(configured.name).To(Equal("custom"))
		Expect(configured.arguments).To(Equal([]string{"argument"}))
		Expect(configured.returncodes).To(Equal([]command.ExitCode{0, 2}))
		Expect(configured.env).To(Equal(map[string]string{"KEY": "value"}))
		Expect(configured.onHost).To(BeFalse())
		Expect(configured.idempotence).To(Equal(Disabled))
		Expect(configured.requiresInterrupt).To(BeTrue())

		defaults := newStepOptions("step.sh")
		Expect(defaults.name).To(Equal("step.sh"))
		Expect(defaults.arguments).To(Equal([]string{}))
		Expect(defaults.returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(defaults.env).To(Equal(map[string]string{}))
		Expect(defaults.onHost).To(BeTrue())
		Expect(defaults.idempotence).To(Equal(Auto))
	})

	It("normalizes nil and empty values when fingerprinting", func() {
		nilFingerprint, err := stepFingerprint("step.sh", nil, nil, nil, false)
		Expect(err).NotTo(HaveOccurred())

		emptyFingerprint, err := stepFingerprint(
			"step.sh",
			[]string{},
			[]command.ExitCode{},
			map[string]string{},
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(nilFingerprint).To(Equal(emptyFingerprint))

		changedFingerprint, err := stepFingerprint(
			"other.sh",
			[]string{},
			[]command.ExitCode{},
			map[string]string{},
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(changedFingerprint).NotTo(Equal(emptyFingerprint))
	})

	It("resolves environment arguments and reports each missing variable once", func() {
		const (
			present = "NODEWRIGHT_SHARED_STEP_PRESENT"
			missing = "NODEWRIGHT_SHARED_STEP_MISSING"
		)
		GinkgoT().Setenv(present, "resolved")
		previous, existed := os.LookupEnv(missing)
		Expect(os.Unsetenv(missing)).To(Succeed())
		DeferCleanup(func() {
			if existed {
				Expect(os.Setenv(missing, previous)).To(Succeed())
			}
		})

		arguments, missingVariables := resolveArguments([]string{
			"literal",
			"env:" + present,
			"env:" + missing,
			"env:" + missing,
		})

		Expect(arguments).To(Equal([]string{
			"literal",
			"resolved",
			"env:" + missing,
			"env:" + missing,
		}))
		Expect(missingVariables).To(Equal([]string{missing}))
	})

	It("wraps JSON encoding failures", func() {
		_, err := encodeStep(make(chan struct{}), Auto)
		Expect(err).To(MatchError(ContainSubstring("marshal step:")))
	})

	It("allocates a nil command environment before adding runtime values", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()

		status, err := runStep(
			context.Background(),
			newStepRunConfig(stepRoot, skyhookDir),
			filepath.Base(executable),
			[]string{"-test.run=^TestStep$", "--", "inspect"},
			[]command.ExitCode{command.SuccessExitCode},
			false,
			nil,
			&stepVersions{previous: "1.0.0", current: "2.0.0"},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})
})

var _ = Describe("step runner selection", func() {
	DescribeTable("uses the appropriate runner and paths",
		func(onHost bool) {
			root := GinkgoT().TempDir()
			hostRunner := commandmock.NewMockRunner(GinkgoT())
			plainRunner := commandmock.NewMockRunner(GinkgoT())
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			chrootCalls, plainCalls := 0, 0
			config, err := execution.NewConfig(execution.WithRootMount(root), execution.WithStepRoot("/steps"),
				execution.WithSkyhookDir("/package"), execution.WithRunOutput(stdout, stderr),
				execution.WithRunnerFactory(func(options ...command.RunnerOption) command.Runner {
					if len(options) == 0 {
						plainCalls++
						return plainRunner
					}
					Expect(command.NewRunner(options...)).To(Equal(command.NewRunner(command.WithChroot(root))))
					chrootCalls++
					return hostRunner
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			stepRoot, packageDir, runner := "/steps", "/package", hostRunner
			if !onHost {
				stepRoot, packageDir, runner = filepath.Join(root, "steps"), filepath.Join(root, "package"), plainRunner
			}
			ctx := context.Background()
			runner.EXPECT().Run(ctx, command.NewCommand(filepath.Join(stepRoot, "run.sh"),
				command.WithArguments("arg"), command.WithWorkingDirectory(packageDir),
				command.WithEnvironment(map[string]string{"KEY": "value", "STEP_ROOT": stepRoot, "SKYHOOK_DIR": packageDir}),
				command.WithStdout(stdout), command.WithStderr(stderr), command.WithRequiredPermissions(0o111),
			)).Return(command.Result{}, nil).Once()
			value := NewRegularStep("run.sh", WithOnHost(onHost), WithArguments([]string{"arg"}), WithEnv(map[string]string{"KEY": "value"}))
			status, err := value.Run(ctx, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(chrootCalls).To(Equal(1))
			if onHost {
				Expect(plainCalls).To(BeZero())
			} else {
				Expect(plainCalls).To(Equal(1))
			}
		},
		Entry("host step", true),
		Entry("container step", false),
	)
})

var _ = Describe("step fingerprint stability", func() {
	It("preserves the on-disk digest and ignores map insertion order and display metadata", func() {
		value := NewRegularStep("step.sh", WithArguments([]string{"one", "two"}), WithReturncodes([]command.ExitCode{0, 2}),
			WithEnv(map[string]string{"Z": "last", "A": "first"}), WithOnHost(true))
		fingerprint, err := value.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		Expect(fingerprint).To(Equal("78890f1ac2fcfde8b35c68a06e0f3cb7d94746d2934ef553fcac703dda87fa0f"))
		value.Env = map[string]string{"A": "first", "Z": "last"}
		value.Name, value.IdempotenceMode, value.RequiresInterrupt = "renamed", Disabled, true
		reordered, err := value.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		Expect(reordered).To(Equal(fingerprint))
	})

	DescribeTable("changes when execution inputs change",
		func(option Option) {
			original, err := NewRegularStep("step.sh").Fingerprint()
			Expect(err).NotTo(HaveOccurred())
			changed, err := NewRegularStep("step.sh", option).Fingerprint()
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).NotTo(Equal(original))
		},
		Entry("arguments", WithArguments([]string{"arg"})),
		Entry("return codes", WithReturncodes([]command.ExitCode{2})),
		Entry("environment", WithEnv(map[string]string{"KEY": "value"})),
		Entry("execution root", WithOnHost(false)),
	)

	It("shares the same digest between regular and upgrade steps with identical execution inputs", func() {
		regular := NewRegularStep("upgrade.sh")
		upgrade, err := NewUpgradeStep("upgrade.sh")
		Expect(err).NotTo(HaveOccurred())
		regularFingerprint, err := regular.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		upgradeFingerprint, err := upgrade.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		Expect(upgradeFingerprint).To(Equal(regularFingerprint))
	})
})
