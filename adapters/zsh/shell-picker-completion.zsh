# Runs only inside a completion widget. Probe in a subshell so normal
# completion functions can add matches without changing the editor or leaving
# temporary function overrides behind.
_shell_picker_completion_context() {
  emulate -L zsh
  setopt extendedglob
  local _sp_mode=cp _sp_prefix=$PREFIX _sp_suffix=$SUFFIX
  local _sp_iprefix=$IPREFIX _sp_isuffix=$ISUFFIX
  integer _sp_path=0 _sp_nonpath=0

  (( CURRENT > 1 )) || [[ $compstate[context] == redirect ]] || return 1
  [[ $PREFIX$SUFFIX != *'$'* && $PREFIX$SUFFIX != *'`'* ]] || return 1

  # zoxide's native multi-word completion can launch fzf and accept-line.
  # Route its directory arguments directly, leaving the command itself intact.
  if [[ $words[1] == (cd|pushd|__zoxide_z) || ${_comps[$words[1]]-} == __zoxide_z_complete ]]; then
    [[ $PREFIX != -* ]] || return 1
    _sp_path=1 _sp_mode=cd
  else
    _path_files() {
      _sp_path=1
      _sp_prefix=$PREFIX _sp_suffix=$SUFFIX
      _sp_iprefix=$IPREFIX _sp_isuffix=$ISUFFIX
      (( ${@[(Ie)-/]} )) && _sp_mode=cd
      return 0
    }
    compadd() {
      local -a original=("$@") flags opts
      local -A values
      integer before=$compstate[nmatches]
      zparseopts -D -a opts -A values -- a k 'f=flags' q Q e n U l 1 2 C \
        F+: P: S: o+:: p: s: i: I: W+: d: J+: V+: X+: x+: r: R: D+: O+: A+: E+: M+:
      if (( ${#flags} )) && [[ -z ${values[-A]-}${values[-O]-}${values[-D]-} ]]; then
        _sp_path=1
        _sp_prefix=$PREFIX _sp_suffix=$SUFFIX
        _sp_iprefix=$IPREFIX _sp_isuffix=$ISUFFIX
      fi
      builtin compadd "${original[@]}"
      local result=$?
      if (( ! ${#flags} && compstate[nmatches] > before )); then
        _sp_nonpath=1
      fi
      return $result
    }
    _main_complete >/dev/null 2>&1
  fi
  (( _sp_path && ! _sp_nonpath )) || return 1
  print -rn -- "$_sp_mode"$'\0'"$_sp_prefix"$'\0'"$_sp_suffix"$'\0'"$_sp_iprefix"$'\0'"$_sp_isuffix"$'\0'
}

_shell_picker_complete() {
  emulate -L zsh
  local value output selected query quoted tail
  local -a context selected_paths insertions
  integer picker_status=1 valid=1
  while IFS= read -r -d '' value; do
    context+=("$value")
  done < <(_shell_picker_completion_context)
  compstate[insert]=''
  compstate[list]=''
  (( ${#context} == 5 )) || return 0
  _shell_picker_handled=1
  query="${(Q)context[2]}${(Q)context[3]}"
  output=$(command mktemp -- "${TMPDIR:-/tmp}/shell-picker-complete.${$}.XXXXXX") || return 0
  {
    command shell-picker "$context[1]" --cwd "$PWD" --home "$HOME" --output nul \
      --completion "$query" --command-line "$BUFFER" --display "${SHELL_PICKER_DISPLAY:-auto}" >| "$output"
    picker_status=$?
    while true; do
      selected=
      if IFS= read -r -d '' selected; then
        [[ -n $selected ]] || valid=0
        selected_paths+=("$selected")
      else
        [[ -z $selected ]] || valid=0
        break
      fi
    done < "$output"
  } always {
    command rm -f -- "$output"
  }
  (( (picker_status == 0 || picker_status == 10) && valid && ${#selected_paths} )) || return 0
  [[ $context[1] != cd || ${#selected_paths} == 1 || $picker_status == 10 ]] || return 0
  PREFIX=$context[2] SUFFIX=$context[3] IPREFIX=$context[4] ISUFFIX=$context[5]
  for selected in "${selected_paths[@]}"; do
    if [[ -n $compstate[quote] ]]; then
      # A quoted tilde is literal in Zsh: use the actual home in this case.
      [[ $selected == '~/'* ]] && selected="$HOME/${selected#\~/}"
      insertions+=("$selected")
    elif [[ $selected == '~/'* ]]; then
      tail=${selected#\~/}
      insertions+=("~/${(q)tail}")
    else
      insertions+=("${(q)selected}")
    fi
  done
  if [[ -n $compstate[quote] ]]; then
    builtin compadd -U -i "$IPREFIX" -I "$ISUFFIX" -S '' -- "${insertions[@]}"
  else
    builtin compadd -Q -U -i "$IPREFIX" -I "$ISUFFIX" -S '' -- "${insertions[@]}"
  fi
  compstate[insert]=all
}
