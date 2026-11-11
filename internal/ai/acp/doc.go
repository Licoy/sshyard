// Package acp is SSHYard's client for one external local agent process
// speaking the Agent Client Protocol (github.com/coder/acp-go-sdk).
//
// The agent process and every byte it sends are untrusted. Paths, sizes,
// enums and ids are checked here. Its reason, optionId, allow_always and
// mode requests never decide anything; the pane's policy.Gate does.
//
// Decisions the spec leaves open (conservative):
//   - A mode id skips approval when, with case and '-' / '_' removed, it
//     contains bypass, fullaccess, auto, agent, acceptedits, alwaysapprove
//     or skippermission. "agent" is the spec's word and also matches ids
//     such as "my-agent". An id that is a URI with a fragment is judged by
//     the fragment only.
//   - An omitted terminal outputByteLimit is 1 MiB. A larger limit is
//     clamped to 1 MiB. Zero keeps nothing.
//   - At most two terminals run per session (the spec's command cap).
//   - The child environment is a whitelist. DISPLAY and XDG_RUNTIME_DIR
//     are not passed. SSH_AUTH_SOCK, SSH_AGENT_PID, SSHYARD_* and cloud
//     credential variables are removed even if the agent config sets them.
//   - A clipped tool reason is forwarded for display. The gate must ignore it.
//   - A POSIX absolute path outside the sandbox is a remote path. A drive
//     path, a relative path, or a path with a NUL byte is an error.
//   - Clearing a file is not done with fs/write_text_file. The SDK rejects
//     empty content (-32602). The agent clears with a terminal command,
//     which is graded like any other command.
//
// Merge points (do not invent a second path):
//   - Gate: the app's App.newGate. This package never builds a gate.
//   - Exec: policy.Exec. It has no environment. An implementation may also
//     implement EnvRunner; otherwise LANG / LC_* / TERM are dropped.
//   - Files: the Files interface matches A1's *remote.Files (sftpsvc.Version).
//     Nil means "not connected yet" after Authorize.
//   - Write diffs: agent.Unified.
//   - policy.Action grading stays in internal/ai/policy (A1). Do not edit it.
package acp

import sdk "github.com/coder/acp-go-sdk"

// ProtocolVersion is the only ACP version SSHYard speaks.
const ProtocolVersion = sdk.ProtocolVersionNumber
