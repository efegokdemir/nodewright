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

package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/interrupts"
	interruptsmock "github.com/NVIDIA/nodewright/agent/internal/interrupts/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

type resumableTestInterrupt struct {
	operationCount int
	runPending     func([]bool, func(int) error) (execution.Status, error)
}

func (r *resumableTestInterrupt) Type() interrupts.InterruptType {
	return interrupts.ServiceRestartType
}

func (r *resumableTestInterrupt) Run(
	context.Context,
	execution.Config,
) (execution.Status, error) {
	return execution.StatusFailed, errors.New("unexpected non-resumable interrupt execution")
}

func (r *resumableTestInterrupt) Serialize() ([]byte, error) { return nil, nil }

func (r *resumableTestInterrupt) OperationCount() int { return r.operationCount }

func (r *resumableTestInterrupt) RunPending(
	_ context.Context,
	_ execution.Config,
	completed []bool,
	markCompleted func(int) error,
) (execution.Status, error) {
	return r.runPending(completed, markCompleted)
}

func newMockInterrupt(
	interruptType interrupts.InterruptType,
	status execution.Status,
) *interruptsmock.MockInterrupt {
	value := interruptsmock.NewMockInterrupt(GinkgoT())
	value.EXPECT().Type().Return(interruptType).Maybe()
	value.EXPECT().
		Run(mock.Anything, mock.Anything).
		Return(status, nil).
		Once()
	return value
}

var _ = Describe("interrupt orchestration", func() {
	It("preserves underscores in package names parsed from resource IDs", func() {
		cfg, err := configFromResourceID("nodewright-uid-1_package_with_underscores_1.2.3")

		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(config.Config{
			PackageName:    "package_with_underscores",
			PackageVersion: "1.2.3",
		}))
	})

	It("marks a successful interrupt and skips it on the next invocation", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			writeLogs:  true,
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		value := newMockInterrupt(interrupts.NoOpType, execution.StatusSuccess)

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		status, err = runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "no_op.complete",
		)).To(BeAnExistingFile())
		logEntries, err := os.ReadDir(filepath.Join(
			layout.LogDir(), "package", "1.0.0", interruptsDirName,
		))
		Expect(err).NotTo(HaveOccurred())
		Expect(logEntries).To(HaveLen(1))
	})

	It("does not expose an ordinary interrupt as complete while it is running", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		marker := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.ServiceRestartType).Maybe()
		value.EXPECT().Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				Expect(marker).NotTo(BeAnExistingFile())
			}).
			Return(execution.StatusSuccess, nil).
			Once()

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(marker).To(BeAnExistingFile())
	})

	It("does not mark a failed ordinary interrupt complete", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		value := newMockInterrupt(interrupts.ServiceRestartType, execution.StatusFailed)

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)).NotTo(BeAnExistingFile())
	})

	It("recognizes legacy interrupt completion markers", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		marker := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)
		Expect(os.MkdirAll(filepath.Dir(marker), 0o755)).To(Succeed())
		Expect(os.WriteFile(marker, nil, 0o600)).To(Succeed())
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.ServiceRestartType).Maybe()

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("resumes multi-command interrupts from indexed legacy markers", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		markerDirectory := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName, runtime.resourceID,
		)
		Expect(os.MkdirAll(markerDirectory, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(markerDirectory, "service_restart_0.complete"), nil, 0o600)).To(Succeed())
		value := &resumableTestInterrupt{operationCount: 3}
		value.runPending = func(completed []bool, markCompleted func(int) error) (execution.Status, error) {
			Expect(completed).To(Equal([]bool{true, false, false}))
			Expect(markCompleted(1)).To(Succeed())
			return execution.StatusFailed, nil
		}

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(filepath.Join(markerDirectory, "service_restart_1.complete")).To(BeAnExistingFile())
		Expect(filepath.Join(markerDirectory, "service_restart_2.complete")).NotTo(BeAnExistingFile())

		value.runPending = func(completed []bool, markCompleted func(int) error) (execution.Status, error) {
			Expect(completed).To(Equal([]bool{true, true, false}))
			Expect(markCompleted(2)).To(Succeed())
			return execution.StatusSuccess, nil
		}
		status, err = runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(markerDirectory, "service_restart_2.complete")).To(BeAnExistingFile())
	})

	It("retains interrupt completion when log cleanup fails", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			writeLogs:  true,
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		logDirectory := filepath.Join(layout.LogDir(), "package", "1.0.0", interruptsDirName)
		savedLogDirectory := logDirectory + "-saved"
		outside := GinkgoT().TempDir()
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.ServiceRestartType).Maybe()
		value.EXPECT().Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				Expect(os.Rename(logDirectory, savedLogDirectory)).To(Succeed())
				Expect(os.Symlink(outside, logDirectory)).To(Succeed())
			}).
			Return(execution.StatusSuccess, nil).
			Once()

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("cleaning old logs for interrupt")))
		marker := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)
		Expect(marker).To(BeAnExistingFile())
		status, err = runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("retries a canceled node restart until the host boot ID changes", func() {
		root := GinkgoT().TempDir()
		bootIDPath := filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id")
		Expect(os.MkdirAll(filepath.Dir(bootIDPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(bootIDPath, []byte("boot-a\n"), 0o600)).To(Succeed())
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		markerBase := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "node_restart_0",
		)
		pendingMarker := markerBase + ".pending"
		completeMarker := markerBase + ".complete"
		canceled := interruptsmock.NewMockInterrupt(GinkgoT())
		canceled.EXPECT().Type().Return(interrupts.NodeRestartType).Maybe()
		canceled.EXPECT().Run(mock.Anything, mock.Anything).
			Return(execution.StatusFailed, context.Canceled).
			Once()

		status, err := runInterrupt(context.Background(), req, runtime, layout, canceled)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(pendingMarker).To(BeAnExistingFile())
		Expect(completeMarker).NotTo(BeAnExistingFile())

		retried := newMockInterrupt(interrupts.NodeRestartType, execution.StatusSuccess)
		status, err = runInterrupt(context.Background(), req, runtime, layout, retried)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(pendingMarker).To(BeAnExistingFile())
		Expect(completeMarker).NotTo(BeAnExistingFile())

		Expect(os.WriteFile(bootIDPath, []byte("boot-b\n"), 0o600)).To(Succeed())
		completed := interruptsmock.NewMockInterrupt(GinkgoT())
		completed.EXPECT().Type().Return(interrupts.NodeRestartType).Maybe()
		status, err = runInterrupt(context.Background(), req, runtime, layout, completed)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(pendingMarker).NotTo(BeAnExistingFile())
		Expect(completeMarker).To(BeAnExistingFile())
	})

	It("decodes the operator wire payload before running an interrupt", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		encoded, err := interrupts.Encode(interrupts.NoOp{})
		Expect(err).NotTo(HaveOccurred())
		req := request{
			rootMount:     root,
			copyDir:       "/package",
			interruptData: encoded,
		}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		status, err := runDecodedInterrupt(context.Background(), req, runtime, layout)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})
})

var _ = Describe("concrete interrupt orchestration", func() {
	var (
		root    string
		layout  flags.Layout
		req     request
		runtime runtimeConfig
		runner  *commandmock.MockRunner
		ctx     context.Context
	)
	BeforeEach(func() {
		root = GinkgoT().TempDir()
		layout = flags.DefaultLayout(root)
		req = request{rootMount: root, copyDir: "/package"}
		ctx = context.Background()
		runner = commandmock.NewMockRunner(GinkgoT())
		runtime = runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard, stderr: io.Discard,
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			runnerFactory: func(options ...command.RunnerOption) command.Runner {
				Expect(command.NewRunner(options...)).To(Equal(command.NewRunner(command.WithChroot(root))))
				return runner
			},
		}
	})
	writeBootID := func(id string) {
		path := filepath.Join(root, hostBootIDPath)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(id+"\n"), 0o600)).To(Succeed())
	}
	expectedCommand := func(executable string, arguments ...string) command.Command {
		if arguments == nil {
			arguments = []string{}
		}
		return command.NewCommand(executable, command.WithArguments(arguments...),
			command.WithWorkingDirectory(req.copyDir), command.WithStdout(runtime.stdout), command.WithStderr(runtime.stderr))
	}

	DescribeTable("keeps reboot pending until the boot ID changes",
		func(result command.Result, runErr error, expected execution.Status) {
			writeBootID("boot-a")
			marker := interruptCompletionMarkers(layout, runtime.resourceID, interrupts.NodeRestart{})[0]
			pending := filepath.Join(filepath.Dir(marker), "node_restart_0.pending")
			runner.EXPECT().Run(ctx, expectedCommand("reboot")).Run(func(context.Context, command.Command) {
				data, err := os.ReadFile(pending)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("boot-id:boot-a\n"))
				Expect(marker).NotTo(BeAnExistingFile())
			}).Return(result, runErr).Twice()
			for range 2 {
				status, err := runInterrupt(ctx, req, runtime, layout, interrupts.NodeRestart{})
				Expect(status).To(Equal(expected))
				if runErr != nil {
					Expect(errors.Is(err, runErr)).To(BeTrue())
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(pending).To(BeAnExistingFile())
				Expect(marker).NotTo(BeAnExistingFile())
			}
			writeBootID("boot-b")
			runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
				Fail("confirmed reboot constructed a runner")
				return nil
			}
			for range 2 {
				status, err := runInterrupt(ctx, req, runtime, layout, interrupts.NodeRestart{})
				Expect(err).NotTo(HaveOccurred())
				Expect(status).To(Equal(execution.StatusSuccess))
			}
			Expect(pending).NotTo(BeAnExistingFile())
			Expect(marker).To(BeAnExistingFile())
		},
		Entry("SIGTERM", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}, nil, execution.StatusSuccess),
		Entry("SIGKILL", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGKILL}, nil, execution.StatusFailed),
		Entry("runner error", command.Result{}, errors.New("reboot interrupted"), execution.StatusFailed),
	)

	It("honours a legacy Python node_restart_0.complete without reading the boot ID or running reboot", func() {
		marker := interruptCompletionMarkers(layout, runtime.resourceID, interrupts.NodeRestart{})[0]
		Expect(os.MkdirAll(filepath.Dir(marker), 0o755)).To(Succeed())
		Expect(os.WriteFile(marker, []byte("legacy timestamp"), 0o600)).To(Succeed())
		runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
			Fail("legacy completion constructed a runner")
			return nil
		}
		status, err := runInterrupt(ctx, req, runtime, layout, interrupts.NodeRestart{})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(root, hostBootIDPath)).NotTo(BeAnExistingFile())
	})

	DescribeTable("replaces malformed pending markers before running reboot",
		func(contents string) {
			writeBootID("boot-a")
			marker := interruptCompletionMarkers(layout, runtime.resourceID, interrupts.NodeRestart{})[0]
			pending := filepath.Join(filepath.Dir(marker), "node_restart_0.pending")
			Expect(os.MkdirAll(filepath.Dir(pending), 0o755)).To(Succeed())
			Expect(os.WriteFile(pending, []byte(contents), 0o600)).To(Succeed())
			runner.EXPECT().Run(ctx, expectedCommand("reboot")).Run(func(context.Context, command.Command) {
				data, err := os.ReadFile(pending)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("boot-id:boot-a\n"))
			}).Return(command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}, nil).Once()
			status, err := runInterrupt(ctx, req, runtime, layout, interrupts.NodeRestart{})
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(marker).NotTo(BeAnExistingFile())
		},
		Entry("missing prefix", "old timestamp"),
		Entry("empty boot ID", "boot-id:\n"),
	)

	It("resumes service commands after the last persisted success", func() {
		value := interrupts.ServiceRestart{Services: []string{"a", "b"}}
		markers := interruptCompletionMarkers(layout, runtime.resourceID, value)
		runner.EXPECT().Run(ctx, expectedCommand("systemctl", "daemon-reload")).Run(func(context.Context, command.Command) {
			Expect(markers[0]).NotTo(BeAnExistingFile())
		}).Return(command.Result{}, nil).Once()
		runner.EXPECT().Run(ctx, expectedCommand("systemctl", "restart", "a")).Run(func(context.Context, command.Command) {
			Expect(markers[0]).To(BeAnExistingFile())
			Expect(markers[1]).NotTo(BeAnExistingFile())
		}).Return(command.Result{ExitCode: 1}, nil).Once()
		status, err := runInterrupt(ctx, req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(markers[1]).NotTo(BeAnExistingFile())
		Expect(markers[2]).NotTo(BeAnExistingFile())

		runner.EXPECT().Run(ctx, expectedCommand("systemctl", "restart", "a")).Return(command.Result{}, nil).Once()
		runner.EXPECT().Run(ctx, expectedCommand("systemctl", "restart", "b")).Run(func(context.Context, command.Command) {
			Expect(markers[1]).To(BeAnExistingFile())
			Expect(markers[2]).NotTo(BeAnExistingFile())
		}).Return(command.Result{}, nil).Once()
		status, err = runInterrupt(ctx, req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		for _, marker := range markers {
			Expect(marker).To(BeAnExistingFile())
		}
		runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
			Fail("completed services constructed a runner")
			return nil
		}
		status, err = runInterrupt(ctx, req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	DescribeTable("removes the pending reboot marker if execution cannot begin",
		func(copyDir, resourceID string, writeLogs bool, expectedError string) {
			writeBootID("boot-a")
			req.copyDir, runtime.resourceID, runtime.writeLogs = copyDir, resourceID, writeLogs
			marker := interruptCompletionMarkers(layout, runtime.resourceID, interrupts.NodeRestart{})[0]
			pending := filepath.Join(filepath.Dir(marker), "node_restart_0.pending")
			runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
				Fail("invalid execution setup constructed a runner")
				return nil
			}
			status, err := runInterrupt(ctx, req, runtime, layout, interrupts.NodeRestart{})
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(ContainSubstring(expectedError)))
			Expect(pending).NotTo(BeAnExistingFile())
			Expect(marker).NotTo(BeAnExistingFile())
		},
		Entry("relative package path", "relative", "resource_package_1.0.0", false, "configuring interrupt"),
		Entry("invalid log identity", "/package", "invalid", true, "resolving interrupt log identity"),
	)

	DescribeTable("does not attempt reboot without a valid host boot ID",
		func(missing bool) {
			if !missing {
				writeBootID("")
			}
			runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
				Fail("invalid boot ID constructed a runner")
				return nil
			}
			status, err := runInterrupt(ctx, req, runtime, layout, interrupts.NodeRestart{})
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(ContainSubstring("reading host boot ID")))
			marker := interruptCompletionMarkers(layout, runtime.resourceID, interrupts.NodeRestart{})[0]
			Expect(marker).NotTo(BeAnExistingFile())
			Expect(filepath.Join(filepath.Dir(marker), "node_restart_0.pending")).NotTo(BeAnExistingFile())
		},
		Entry("missing boot ID", true),
		Entry("empty boot ID", false),
	)

	It("reports a conflicting completion path without starting service commands", func() {
		value := interrupts.ServiceRestart{Services: []string{"a"}}
		marker := interruptCompletionMarkers(layout, runtime.resourceID, value)[0]
		Expect(os.MkdirAll(marker, 0o755)).To(Succeed())
		runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
			Fail("invalid completion marker constructed a runner")
			return nil
		}
		status, err := runInterrupt(ctx, req, runtime, layout, value)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("checking interrupt completion marker")))
	})

	It("stops service execution when a completion marker cannot be persisted", func() {
		value := interrupts.ServiceRestart{Services: []string{"a", "b"}}
		markers := interruptCompletionMarkers(layout, runtime.resourceID, value)
		runner.EXPECT().Run(ctx, expectedCommand("systemctl", "daemon-reload")).Run(func(context.Context, command.Command) {
			Expect(os.MkdirAll(markers[0], 0o755)).To(Succeed())
		}).Return(command.Result{}, nil).Once()
		status, err := runInterrupt(ctx, req, runtime, layout, value)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("command 0 complete")))
		Expect(markers[1]).NotTo(BeAnExistingFile())
		Expect(markers[2]).NotTo(BeAnExistingFile())
	})

	It("rejects an invalid interrupt payload before constructing a runner", func() {
		req.interruptData = "invalid-base64"
		runtime.runnerFactory = func(...command.RunnerOption) command.Runner {
			Fail("invalid interrupt payload constructed a runner")
			return nil
		}
		status, err := runDecodedInterrupt(ctx, req, runtime, layout)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("decoding interrupt")))
	})
})
