// Risk, highest first: Critical, Unknown, High, Medium, Low, ReadOnly.
// Rank implements that order. The Risk constants do not (Unknown is last
// so a zero Grade is read-only). Equal risk keeps the earlier command,
// unless the later category is more severe.
//
// A command that is not in the built-in table is Unknown, never ReadOnly.
// Aliases are not expanded, and the text produced by a command substitution
// is not simulated: cat $(ls) stays read-only, but a bare $VAR given to a
// path reader is Unknown. python -c and the other interpreters are not
// parsed; piping curl or wget into one of them is Critical.
//
// curl or wget with no save path is Medium "network". A save (-o, -O,
// --output, --output-document, wget -P / --directory-prefix, curl
// --output-dir) or a shell redirect of that output is graded as a write
// of the target, and the grade is at least the network grade. Under /tmp
// that stays Medium, not a low write. curl -P is the FTP port, not a
// directory. .ssh, shadow, and sudoers follow the write rules (High).
// A pipe into sh, bash, or another interpreter is Critical, including
// sh -c "$(curl …)". bash -c "curl URL" is only the inner grade.
//
// Wrappers (sudo, doas, env, timeout, nice, nohup, xargs, command, exec,
// stdbuf, setsid, time, ionice) are peeled at any depth, including a
// leading VAR=value. The grade is the max of the inner command and a
// dangerous assignment. An option list that does not parse is Unknown.
// The wrapper still raises the result to at least Medium. sudo of a
// read-only command is "permission". sh -c is parsed, not raised. sudo
// with no command, sudo -i/-s, su, sudo su, and sudo of a shell with no
// -c (sudo bash) are High "permission".
//
// sed is read-only only when every script is visible and has no e command,
// s///e flag, or w/W write. A w target is graded as a file write (low under
// a writable root). sed -f, or a script that does not parse, is Unknown.
// git -C is a directory. git -c and --config-env are graded by the key:
// a command, pager, alias, editor, helper, hook, textconv, filter, merge
// driver, include.path, or includeIf.<cond>.path is High "exec-config".
// http.proxy (including http.<url>.proxy) and protocol.*.allow=always are
// Medium "network". Any other key keeps the subcommand. A -c that is not
// key=value, or --exec-path, is Unknown. The value is not parsed as a
// shell command. ModeFull auto-runs High.
// git log and git diff -o/--output are file writes. git format-patch -o is
// a write; format-patch with no -o stays Unknown. sort --compress-program
// is Unknown.
//
// An assignment that can change what runs (LD_*, DYLD_*, PATH, BASH_ENV,
// ENV, IFS, PROMPT_COMMAND, SHELLOPTS, PS4, a name ending in PAGER, EDITOR,
// VISUAL, GIT_*, PYTHONPATH, PYTHONSTARTUP, PERL5OPT, NODE_OPTIONS,
// LESSOPEN, LESSCLOSE, GETCONF_DIR), including bare assignments, export,
// env NAME=value cmd and the same words after sudo or another wrapper,
// is Unknown. Unknown ranks above High, so
// LD_PRELOAD, LD_LIBRARY_PATH, LD_AUDIT, and DYLD_* are not lowered to a
// High "exec-inject" grade. LANG, TZ, and LC_ALL stay neutral. Ordinary bare
// assignments with no command stay read-only. The command is still graded,
// so a visible critical stays critical.
//
// Writes under a writable root are Low for mkdir, touch, and redirections.
// write_file is Low only for a new file under /tmp. mv, cp, sed -i, ln,
// and install stay at least Medium. Shell startup files (~/.bashrc,
// ~/.profile, ~/.zshrc, fish config.fish, /etc/profile*, /etc/bash.bashrc),
// systemd units, cron files, ~/.gitconfig, ~/.npmrc, ~/.pip, and
// ~/.config/autostart are at least Medium even inside a writable root.
// Writes of /etc/shadow, /etc/gshadow, /etc/sudoers*, authorized_keys, or
// anything under .ssh are High "permission". Other sensitive writes stay
// Medium. /dev/null and the standard character devices are not writes.
// /dev/sd*, /dev/nvme*, and the other disk devices are Critical.
//
// Also sensitive to read: /proc/*/environ (not /proc/*/cmdline), a name
// matching .*_history, ~/.docker/config.json, ~/.git-credentials,
// ~/.config/gh/hosts.yml, and /etc/ssl/private.
//
// mv of a critical top-level path, recursive cp of one (-r/-a), and
// rsync --delete of one are Critical. less and more with a + command
// or -k are Unknown.
//
// Recursive rm of /, /*, ~, $HOME, the session home, or a top-level
// directory is Critical. The top-level set is /bin /boot /dev /etc /home
// /lib /lib32 /lib64 /libx32 /opt /proc /root /run /sbin /sys /usr /var
// /tmp /mnt /media /srv and the macOS roots /System /Users /Applications
// /Volumes /private /Library. rm -rf /tmp is Critical; rm -rf /tmp/x is
// High. apt-get update is Unknown (it is not an upgrade); yum, dnf,
// zypper, brew, and snap update are Medium "install". npm install is
// Medium only with -g.
//
// Local reads inside Scope.Sandbox are "local-read", unless the path is
// sensitive. A read outside the sandbox is Low "local". An edit inside
// the sandbox is a low write. Deleting a Windows drive root or anything
// under \Windows is Critical.
//
// A shell read of file content (cat, head, tail, less, grep, sort, wc,
// cut, sed, awk and the like, or a < redirect) is read-only only when every
// file is a literal, non-sensitive path under /etc, /var/log, /sys, /usr,
// /lib, /www/server or /www/wwwlogs, or a /proc entry that cannot reach
// another process's environment, memory or files. Globs, braces and
// backslashes, and files elsewhere (home, /tmp), are Medium
// "sensitive-read": a link there could lead to a secret. Standard input is
// not a file. awk is read-only when its program cannot run a command or
// write a file (system, getline, a pipe, print > file, a gawk @ directive);
// -f and other options are Unknown, and ENVIRON is a sensitive read.
// nginx, apachectl, httpd and php-fpm configuration tests and versions are
// read-only; their signals and service verbs are Medium "service".
//
// ModeAsk does not offer "allow similar" for a file write, so the diff is
// shown every time. That is stricter than the spec table and is an open
// question. Session grants never skip the review model.
// ModeReview auto-runs ReadOnly and sends every other grade except
// Critical to a configured model, including Unknown; Critical always asks
// the user. The model is told to let work that moves the user's task
// forward run, writes, restarts and clean-ups included, and to stop only
// for named harms; it sees the current request and the user's earlier
// messages in the pane, never the assistant's text. Only an explicit allow runs
// automatically; ask, invalid replies, errors and timeouts ask the user.
// After the model asks, a session grant still covers one plain command (no
// list, pipe, redirection or substitution) but never a script. Without a
// model, ReadOnly and Low auto-run and all other grades ask. Grades remain
// metadata and audit context for the model.
// ModeFull auto-runs every grade, Unknown included, so nothing interrupts
// a pane the user set to stop asking. Critical and Unknown still set Red,
// which marks what ran unjudged, and neither offers "allow similar". High,
// including git exec-config, auto-runs.
package policy
