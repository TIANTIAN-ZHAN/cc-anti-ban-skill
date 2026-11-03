# Source this file from an ALREADY RUNNING zsh:
#   source scripts/build-safe.zsh [/absolute/output/cc-health]
# Do not launch a new interpreter from a DYLD-contaminated environment.
# This gate uses syntax and genuine zsh metadata before dispatching any command.
# It does not trust `builtin`/`command` until their resolution has been checked.
# The caller's parser, lexical aliases, traps and tracing expansion must be trusted;
# code that executes before this gate cannot be made safe by this sourced file.
if [[ -n ${ZSH_VERSION-} && ${ZSH_EVAL_CONTEXT-} == *:file
      && "-${(t)ZSH_EVAL_CONTEXT}-" == *-special-*
      && ${(t)functions} == association* && "-${(t)functions}-" == *-special-*
      && ${(t)parameters} == association* && "-${(t)parameters}-" == *-special-*
      && ${(t)builtins} == association* && "-${(t)builtins}-" == *-special-*
      && ${+functions[builtin]} == 0 && ${+functions[command]} == 0
      && ${builtins[builtin]-} == defined && ${builtins[command]-} == defined
      && ${builtins[emulate]-} == defined && ${builtins[setopt]-} == defined
      && ${builtins[unset]-} == defined && ${builtins[return]-} == defined
      && ${builtins[print]-} == defined && ${builtins[trap]-} == defined ]]; then
(
  \builtin \emulate -L zsh || \builtin \return 2
  \builtin \setopt ERR_EXIT NO_UNSET PIPE_FAIL || \builtin \return 2
  # Builtins only until every loader name is removed. Do not expand its value.
  for cc_loader_name in ${(k)parameters}; do
    if [[ $cc_loader_name == DYLD_* ]]; then
      \builtin \unset -- "$cc_loader_name" || \builtin \return 2
    fi
  done
  # Verify the postcondition by names before the first external command.
  for cc_loader_name in ${(k)parameters}; do
    if [[ $cc_loader_name == DYLD_* ]]; then
      \builtin \print -u2 '加载器环境清理未确认；没有启动外部命令。'
      \builtin \return 2
    fi
  done
  cc_source_root=${${(%):-%N}:A:h:h}
  cc_output=${1:-$cc_source_root/.build/cc-health}
  if [[ $cc_output != /* ]]; then
    \builtin \print -u2 '输出路径必须为绝对路径。'
    \builtin \return 2
  fi
  if [[ $(/usr/bin/uname -s) != Darwin ]]; then
    \builtin \print -u2 '此受支持构建入口仅适用于 macOS。'
    \builtin \return 2
  fi
  # Security patch floors reviewed 2026-11-03, distinct from go.mod's language
  # minimum. Query the installed toolchain without GOENV/GOFLAGS/workspace or
  # automatic toolchain selection; use the same policy for the build below.
  cc_go_version=$(GOENV=off GOFLAGS= GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \command \go env GOVERSION) || \builtin \return 2
  if [[ $cc_go_version =~ '^go1\.([1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,4})$' ]]; then
    cc_go_minor=$match[1]
    cc_go_patch=$match[2]
  else
    \builtin \print -u2 '无法确认标准稳定版 Go；预发布、开发版和未知版本不能用于此构建入口。'
    \builtin \return 2
  fi
  if ! [[ $cc_go_minor -ge 28 || ( $cc_go_minor -eq 27 && $cc_go_patch -ge 3 ) || ( $cc_go_minor -eq 26 && $cc_go_patch -ge 10 ) ]]; then
    \builtin \print -u2 'Go 未达到本次已核最低修复版本：需要 1.26.10、1.27.3 或更新稳定分支；请自行升级本地工具链后重试。'
    \builtin \return 2
  fi
  /bin/mkdir -p "${cc_output:h}" || \builtin \return 2
  cc_stage=$(/usr/bin/mktemp -d "${cc_output:h}/.cc-health-build.XXXXXX") || \builtin \return 2
  \builtin \trap '/bin/rm -f -- "$cc_binary"; /bin/rmdir -- "$cc_stage"' EXIT
  cc_binary=$cc_stage/cc-health
  # No install, credential lookup, network dependency download or Developer ID.
  MACOSX_DEPLOYMENT_TARGET=13.0 CGO_CFLAGS="-O2 -g -mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" CGO_ENABLED=1 GOENV=off GOFLAGS= GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \command \go build -C "$cc_source_root" -o "$cc_binary" ./cmd/cc-health || \builtin \return 2
  /usr/bin/codesign --force --sign - --options runtime "$cc_binary" || \builtin \return 2
  /usr/bin/codesign --verify --strict "$cc_binary" || \builtin \return 2
  cc_signature=$(/usr/bin/codesign -d --verbose=4 "$cc_binary" 2>&1) || \builtin \return 2
  if [[ $cc_signature != *'flags='*'(adhoc,runtime)'* ]]; then
    \builtin \print -u2 '未确认 hardened runtime；不要启动此产物。'
    \builtin \return 2
  fi
  /bin/mv -f -- "$cc_binary" "$cc_output" || \builtin \return 2
  \builtin \print '本地 ad-hoc hardened runtime 构建已验证；没有 Developer ID 或公证。'
  \builtin \print -r -- "$cc_output"
)
else
  # In particular, do not call return/exit/print when their dispatcher is untrusted.
  [[ 1 == 0 ]]
fi
