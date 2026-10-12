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
	"encoding/base64"
	"encoding/json"
	"errors"
	"syscall"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ServiceRestart", func() {
	It("uses the service_restart wire type", func() {
		Expect(ServiceRestart{}.Type()).To(Equal(ServiceRestartType))
	})

	It("decodes a service payload into a ServiceRestart value", func() {
		payload := map[string]any{
			"type":     ServiceRestart{}.Type(),
			"services": []string{"containerd", "kubelet"},
		}
		data, err := json.Marshal(payload)
		Expect(err).NotTo(HaveOccurred())

		interrupt, err := Decode(base64.StdEncoding.EncodeToString(data))
		Expect(err).NotTo(HaveOccurred())
		Expect(interrupt).To(Equal(ServiceRestart{Services: []string{"containerd", "kubelet"}}))
	})

	It("rejects a payload whose services field is the wrong type", func() {
		encoded := base64.StdEncoding.EncodeToString([]byte(`{"type":"service_restart","services":"kubelet"}`))

		_, err := Decode(encoded)
		Expect(err).To(MatchError(errInvalidSerializedInterrupt))
	})

	It("emits an empty services array when Services is nil, not null", func() {
		data, err := ServiceRestart{}.Serialize()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"services":[]`))
		Expect(string(data)).NotTo(ContainSubstring("null"))
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := ServiceRestart{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = ServiceRestart{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = ServiceRestart{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})
})

var _ = Describe("ServiceRestart command execution", func() {
	It("reloads the daemon even when no services are configured", func() {
		runner := commandmock.NewMockRunner(GinkgoT())
		config := newInterruptRunConfig(withInterruptRunner(runner))
		ctx := context.Background()
		value := ServiceRestart{}
		Expect(value.OperationCount()).To(Equal(1))
		runner.EXPECT().Run(ctx, interruptCommand(config, "systemctl", "daemon-reload")).Return(command.Result{}, nil).Once()
		status, err := value.Run(ctx, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("reloads and restarts services in order with the configured streams", func() {
		runner := commandmock.NewMockRunner(GinkgoT())
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		config := newInterruptRunConfig(withInterruptRunner(runner), execution.WithRunOutput(stdout, stderr))
		ctx := context.Background()
		expected := [][]string{{"daemon-reload"}, {"restart", "a"}, {"restart", "b"}}
		next := 0
		for index, arguments := range expected {
			runner.EXPECT().Run(ctx, interruptCommand(config, "systemctl", arguments...)).
				Run(func(context.Context, command.Command) {
					Expect(next).To(Equal(index))
					next++
				}).Return(command.Result{}, nil).Once()
		}
		status, err := (ServiceRestart{Services: []string{"a", "b"}}).Run(ctx, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(next).To(Equal(3))
	})

	DescribeTable("stops on a failed operation without marking it or starting later commands",
		func(failedIndex int, result command.Result, runErr error) {
			runner := commandmock.NewMockRunner(GinkgoT())
			config := newInterruptRunConfig(withInterruptRunner(runner))
			ctx := context.Background()
			arguments := [][]string{{"daemon-reload"}, {"restart", "a"}, {"restart", "b"}}
			marked := []int{}
			for index := 0; index <= failedIndex; index++ {
				operationResult, operationErr := command.Result{}, error(nil)
				if index == failedIndex {
					operationResult, operationErr = result, runErr
				}
				runner.EXPECT().Run(ctx, interruptCommand(config, "systemctl", arguments[index]...)).
					Run(func(context.Context, command.Command) {
						Expect(marked).To(HaveLen(index))
					}).Return(operationResult, operationErr).Once()
			}
			status, err := (ServiceRestart{Services: []string{"a", "b"}}).RunPending(ctx, config, []bool{false, false, false},
				func(index int) error { marked = append(marked, index); return nil })
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(marked).To(HaveLen(failedIndex))
			for index, operation := range marked {
				Expect(operation).To(Equal(index))
			}
			if runErr != nil {
				Expect(errors.Is(err, runErr)).To(BeTrue())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("daemon-reload error", 0, command.Result{}, errors.New("reload error")),
		Entry("first service error", 1, command.Result{}, errors.New("restart error")),
		Entry("first service exit code", 1, command.Result{ExitCode: 1}, nil),
		Entry("first service signal", 1, command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}, nil),
		Entry("last service error", 2, command.Result{}, errors.New("last restart error")),
	)

	It("resumes only the unfinished service and marks it after success", func() {
		runner := commandmock.NewMockRunner(GinkgoT())
		config := newInterruptRunConfig(withInterruptRunner(runner))
		ctx := context.Background()
		marked := []int{}
		runner.EXPECT().Run(ctx, interruptCommand(config, "systemctl", "restart", "b")).
			Run(func(context.Context, command.Command) { Expect(marked).To(BeEmpty()) }).Return(command.Result{}, nil).Once()
		value := ServiceRestart{Services: []string{"a", "b"}}
		status, err := value.RunPending(ctx, config, []bool{true, true, false},
			func(index int) error { marked = append(marked, index); return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(marked).To(Equal([]int{2}))
		status, err = value.RunPending(ctx, config, []bool{true, true, true},
			func(int) error { Fail("completed operation was marked again"); return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("stops when persisting a successful operation fails", func() {
		runner := commandmock.NewMockRunner(GinkgoT())
		config := newInterruptRunConfig(withInterruptRunner(runner))
		ctx := context.Background()
		markerErr := errors.New("marker write failed")
		runner.EXPECT().Run(ctx, interruptCommand(config, "systemctl", "daemon-reload")).Return(command.Result{}, nil).Once()
		status, err := (ServiceRestart{Services: []string{"a", "b"}}).RunPending(ctx, config, []bool{false, false, false},
			func(index int) error { Expect(index).To(Equal(0)); return markerErr })
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, markerErr)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("command 0 complete"))
	})

	It("rejects incomplete resume inputs before creating a runner", func() {
		config := newInterruptRunConfig(execution.WithRunnerFactory(func(...command.RunnerOption) command.Runner {
			Fail("invalid resume state created a runner")
			return nil
		}))
		value := ServiceRestart{Services: []string{"a", "b"}}
		status, err := value.RunPending(context.Background(), config, []bool{false}, func(int) error { return nil })
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("1 completion states for 3 operations")))
		status, err = value.RunPending(context.Background(), config, []bool{false, false, false}, nil)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("completion callback is nil")))
	})
})
