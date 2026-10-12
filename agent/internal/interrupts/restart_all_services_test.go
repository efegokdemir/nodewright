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

package interrupts

import (
	"bytes"
	"context"
	"errors"
	"syscall"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RestartAllServices", func() {
	It("uses the restart_all_services wire type", func() {
		Expect(RestartAllServices{}.Type()).To(Equal(RestartAllServicesType))
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := RestartAllServices{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = RestartAllServices{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = RestartAllServices{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})
})

var _ = Describe("RestartAllServices command execution", func() {
	It("reloads procps with the configured streams", func() {
		runner := commandmock.NewMockRunner(GinkgoT())
		config := newInterruptRunConfig(withInterruptRunner(runner), execution.WithRunOutput(&bytes.Buffer{}, &bytes.Buffer{}))
		ctx := context.Background()
		runner.EXPECT().Run(ctx, interruptCommand(config, "service", "procps", "force-reload")).Return(command.Result{}, nil).Once()
		status, err := (RestartAllServices{}).Run(ctx, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	DescribeTable("marks completion only after a successful reload",
		func(result command.Result, runErr error, markerErr error, expected execution.Status, shouldMark bool) {
			runner := commandmock.NewMockRunner(GinkgoT())
			config := newInterruptRunConfig(withInterruptRunner(runner))
			ctx := context.Background()
			marked := false
			runner.EXPECT().Run(ctx, interruptCommand(config, "service", "procps", "force-reload")).
				Run(func(context.Context, command.Command) { Expect(marked).To(BeFalse()) }).Return(result, runErr).Once()
			status, err := (RestartAllServices{}).RunPending(ctx, config, []bool{false}, func(index int) error {
				Expect(index).To(Equal(0))
				marked = true
				return markerErr
			})
			Expect(status).To(Equal(expected))
			Expect(marked).To(Equal(shouldMark))
			if runErr != nil {
				Expect(errors.Is(err, runErr)).To(BeTrue())
			} else if markerErr != nil {
				Expect(errors.Is(err, markerErr)).To(BeTrue())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("success", command.Result{}, nil, nil, execution.StatusSuccess, true),
		Entry("non-zero exit", command.Result{ExitCode: 1}, nil, nil, execution.StatusFailed, false),
		Entry("signal", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}, nil, nil, execution.StatusFailed, false),
		Entry("runner error", command.Result{}, errors.New("reload failed"), nil, execution.StatusFailed, false),
		Entry("marker error", command.Result{}, nil, errors.New("marker failed"), execution.StatusFailed, true),
	)

	It("skips a completed reload and rejects invalid resume inputs without creating a runner", func() {
		config := newInterruptRunConfig(execution.WithRunnerFactory(func(...command.RunnerOption) command.Runner {
			Fail("runner should not be constructed")
			return nil
		}))
		value := RestartAllServices{}
		status, err := value.RunPending(context.Background(), config, []bool{true}, func(int) error {
			Fail("completed reload was marked again")
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		status, err = value.RunPending(context.Background(), config, nil, func(int) error { return nil })
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("0 completion states for 1 operations")))
		status, err = value.RunPending(context.Background(), config, []bool{false}, nil)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("completion callback is nil")))
	})
})
