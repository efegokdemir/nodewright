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
	"context"
	"errors"
	"syscall"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("NodeRestart", func() {
	It("uses the node_restart wire type", func() {
		Expect(NodeRestart{}.Type()).To(Equal(NodeRestartType))
	})

	It("recognizes only SIGTERM as reboot completion", func() {
		Expect(nodeRestartCompleted(command.Result{
			ExitCode: command.SignalExitCode,
			Signal:   syscall.SIGTERM,
		})).To(BeTrue())
		Expect(nodeRestartCompleted(command.Result{
			ExitCode: command.SignalExitCode,
			Signal:   syscall.SIGKILL,
		})).To(BeFalse())
		Expect(nodeRestartCompleted(command.Result{ExitCode: 15})).To(BeFalse())
		Expect(nodeRestartCompleted(command.Result{ExitCode: command.SuccessExitCode})).To(BeFalse())
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := NodeRestart{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = NodeRestart{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = NodeRestart{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})

	It("reports when an enqueued reboot does not restart the host", func() {
		err := waitForNodeRestart(context.Background(), time.Millisecond)

		Expect(err).To(MatchError(
			"reboot was enqueued but the host did not restart within 1ms",
		))
	})

	It("stops waiting for a host restart when execution is canceled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := waitForNodeRestart(ctx, time.Hour)

		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})
})

var _ = Describe("NodeRestart command execution", func() {
	DescribeTable("interprets the reboot command result",
		func(result command.Result, runErr error, expected execution.Status) {
			runner := commandmock.NewMockRunner(GinkgoT())
			config := newInterruptRunConfig(withInterruptRunner(runner))
			ctx := context.Background()
			runner.EXPECT().Run(ctx, interruptCommand(config, "reboot")).Return(result, runErr).Once()

			status, err := (NodeRestart{}).Run(ctx, config)
			Expect(status).To(Equal(expected))
			if runErr != nil {
				Expect(errors.Is(err, runErr)).To(BeTrue())
				Expect(err.Error()).To(ContainSubstring(`command "reboot"`))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("SIGTERM confirms shutdown", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}, nil, execution.StatusSuccess),
		Entry("SIGKILL does not confirm shutdown", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGKILL}, nil, execution.StatusFailed),
		Entry("non-zero exit fails", command.Result{ExitCode: 2}, nil, execution.StatusFailed),
		Entry("runner errors are preserved", command.Result{}, errors.New("reboot unavailable"), execution.StatusFailed),
		Entry("SIGTERM with a runner error still fails", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}, errors.New("interrupted runner"), execution.StatusFailed),
	)

	It("waits for confirmation after exit zero and honours cancellation", func() {
		runner := commandmock.NewMockRunner(GinkgoT())
		config := newInterruptRunConfig(withInterruptRunner(runner))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runner.EXPECT().Run(ctx, interruptCommand(config, "reboot")).
			RunAndReturn(func(context.Context, command.Command) (command.Result, error) {
				cancel()
				return command.Result{ExitCode: command.SuccessExitCode}, nil
			}).Once()

		status, err := (NodeRestart{}).Run(ctx, config)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("waiting for host restart"))
	})
})
